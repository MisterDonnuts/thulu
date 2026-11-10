package exportbundle

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"path"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Bump only on incompatible shape changes; Import and Read refuse any other version.
const Version = 1

const (
	manifestName = "manifest.json"
	bundleName   = "bundle.json"
	attachDir    = "attachments"
	codeDir      = "code"
)

type Bundle struct {
	Version        int       `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	OctopusVersion string    `json:"octopus_version,omitempty"`
	WorkspaceID    string    `json:"workspace_id"`

	Agents   agentstore.Pack    `json:"agents"`
	Settings map[string]string  `json:"settings"`
	MCPs     []mcp.Registration `json:"mcps"`

	Boards   []BoardSection   `json:"boards"`
	Projects []ProjectSection `json:"projects"`
	Tasks    []TaskSection    `json:"tasks"`

	// Inventory only, never re-imported: the secrets are not in the bundle.
	Credentials     []credentials.Credential `json:"credentials"`
	SecretChecklist []SecretItem             `json:"secret_checklist"`

	RestoreOrder []string `json:"restore_order"`
}

type BoardSection struct {
	Info      stage.BoardInfo      `json:"info"`
	Stages    []types.StageInfo    `json:"stages"`
	TaskTypes []stage.TaskTypeInfo `json:"task_types"`
}

type ProjectSection struct {
	Project      project.Project `json:"project"`
	Environments []EnvSection    `json:"environments"`
	// Current revision only. Absent from older bundles, which is not a version change.
	InitScript  *project.InitScript `json:"init_script,omitempty"`
	CodeTarPath string              `json:"code_tar_path,omitempty"`
}

type EnvSection struct {
	Environment   project.Environment    `json:"environment"`
	Mappings      []envmetrics.Mapping   `json:"mappings"`
	CustomMetrics []envmetrics.Metric    `json:"custom_metrics"`
	Dashboards    []envmetrics.Dashboard `json:"dashboards"`
	IngestTokens  []ingest.Token         `json:"ingest_tokens"`
}

type TaskSection struct {
	Task         task.Task            `json:"task"`
	Comments     []task.Comment       `json:"comments"`
	Reports      []task.Report        `json:"reports"`
	Events       []task.WorkflowEvent `json:"events"`
	Dependencies []TaskDep            `json:"dependencies"`
	Attachments  []AttachmentSection  `json:"attachments"`
	TestCases    []task.TestCase      `json:"test_cases"`
	// Absent from bundles older than approvals, which is not a version change.
	Approvals []task.Approval `json:"approvals,omitempty"`
}

type TaskDep struct {
	DependsOn string       `json:"depends_on"`
	Kind      task.DepKind `json:"kind"`
}

type AttachmentSection struct {
	Attachment task.Attachment `json:"attachment"`
	TarPath    string          `json:"tar_path"`
}

type SecretItem struct {
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Identity string `json:"identity,omitempty"`
	Note     string `json:"note,omitempty"`
}

func (b *Bundle) Retarget(workspaceID string) {
	from := b.WorkspaceID
	if workspaceID == "" || workspaceID == from {
		return
	}
	// A task type with no workspace is shared by all workspaces and must stay unscoped.
	move := func(ws *string) {
		if *ws == from {
			*ws = workspaceID
		}
	}
	b.WorkspaceID = workspaceID
	for i := range b.Boards {
		move(&b.Boards[i].Info.WorkspaceID)
		for j := range b.Boards[i].TaskTypes {
			move(&b.Boards[i].TaskTypes[j].WorkspaceID)
		}
	}
	for i := range b.Projects {
		move(&b.Projects[i].Project.WorkspaceID)
	}
	for i := range b.Tasks {
		move(&b.Tasks[i].Task.WorkspaceID)
	}
	for i := range b.Credentials {
		move(&b.Credentials[i].WorkspaceID)
	}
}

func attachTarPath(taskID, attID, filename string) string {
	return path.Join(attachDir, taskID, attID+"-"+filename)
}

func codeTarPath(projectID string) string {
	return path.Join(codeDir, projectID+".bundle")
}

type manifest struct {
	Version         int            `json:"version"`
	CreatedAt       time.Time      `json:"created_at"`
	OctopusVersion  string         `json:"octopus_version,omitempty"`
	WorkspaceID     string         `json:"workspace_id"`
	Counts          map[string]int `json:"counts"`
	RestoreOrder    []string       `json:"restore_order"`
	SecretChecklist []SecretItem   `json:"secret_checklist"`
}

func (b *Bundle) counts() map[string]int {
	tasks, comments, attachments, cases, runs, approvals := len(b.Tasks), 0, 0, 0, 0, 0
	for i := range b.Tasks {
		comments += len(b.Tasks[i].Comments)
		approvals += len(b.Tasks[i].Approvals)
		attachments += len(b.Tasks[i].Attachments)
		cases += len(b.Tasks[i].TestCases)
		for j := range b.Tasks[i].TestCases {
			runs += len(b.Tasks[i].TestCases[j].Runs)
		}
	}
	envs, inits := 0, 0
	for i := range b.Projects {
		envs += len(b.Projects[i].Environments)
		if b.Projects[i].InitScript != nil {
			inits++
		}
	}
	return map[string]int{
		"agents":         len(b.Agents.Agents),
		"settings":       len(b.Settings),
		"mcps":           len(b.MCPs),
		"boards":         len(b.Boards),
		"projects":       len(b.Projects),
		"environments":   envs,
		"init_scripts":   inits,
		"tasks":          tasks,
		"comments":       comments,
		"attachments":    attachments,
		"test_cases":     cases,
		"test_case_runs": runs,
		"approvals":      approvals,
		"credentials":    len(b.Credentials),
		"secret_todo":    len(b.SecretChecklist),
	}
}

func Write(w io.Writer, b *Bundle, attachments map[string][]byte) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	man := manifest{
		Version: b.Version, CreatedAt: b.CreatedAt, OctopusVersion: b.OctopusVersion,
		WorkspaceID: b.WorkspaceID, Counts: b.counts(),
		RestoreOrder: b.RestoreOrder, SecretChecklist: b.SecretChecklist,
	}
	if err := writeJSONEntry(tw, manifestName, man); err != nil {
		return err
	}
	if err := writeJSONEntry(tw, bundleName, b); err != nil {
		return err
	}
	for _, tp := range sortedKeys(attachments) {
		if err := writeBytesEntry(tw, tp, attachments[tp]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return apperr.Wrap(err, "exportbundle.errors.close_tar")
	}
	if err := gz.Close(); err != nil {
		return apperr.Wrap(err, "exportbundle.errors.close_gzip")
	}
	return nil
}

func Read(r io.Reader) (*Bundle, map[string][]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, apperr.Wrap(err, "exportbundle.errors.open_gzip")
	}
	tr := tar.NewReader(gz)

	var b *Bundle
	attachments := map[string][]byte{}
	for {
		hdr, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, nil, apperr.Wrap(nextErr, "exportbundle.errors.read_tar")
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, readErr := io.ReadAll(tr)
		if readErr != nil {
			return nil, nil, apperr.Wrap(readErr, "exportbundle.errors.read_entry", "name", hdr.Name)
		}
		switch {
		case hdr.Name == manifestName:
		case hdr.Name == bundleName:
			var parsed Bundle
			if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
				return nil, nil, apperr.Wrap(jsonErr, "exportbundle.errors.parse_entry", "name", bundleName)
			}
			b = &parsed
		case len(hdr.Name) > len(attachDir) && hdr.Name[:len(attachDir)] == attachDir,
			strings.HasPrefix(hdr.Name, codeDir+"/"):
			attachments[hdr.Name] = body
		}
	}
	if err := gz.Close(); err != nil {
		return nil, nil, apperr.Wrap(err, "exportbundle.errors.close_gzip_truncated")
	}
	if b == nil {
		return nil, nil, apperr.Invalid("exportbundle.errors.archive_missing_entry", "name", bundleName)
	}
	if b.Version != Version {
		return nil, nil, apperr.Invalid("exportbundle.errors.version_unsupported", "version", b.Version, "want", Version)
	}
	return b, attachments, nil
}

func writeJSONEntry(tw *tar.Writer, name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.marshal_entry", "name", name)
	}
	return writeBytesEntry(tw, name, raw)
}

func writeBytesEntry(tw *tar.Writer, name string, body []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		return apperr.Wrap(err, "exportbundle.errors.tar_header", "name", name)
	}
	if _, err := tw.Write(body); err != nil {
		return apperr.Wrap(err, "exportbundle.errors.tar_body", "name", name)
	}
	return nil
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
