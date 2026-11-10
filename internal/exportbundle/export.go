package exportbundle

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/ingest"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type Stores struct {
	Agents         *agentstore.Store
	Settings       SettingsStore
	MCP            *mcp.Registry
	Boards         *stage.Store
	Tasks          *task.Store
	Projects       *project.Store
	Metrics        *envmetrics.Store
	Ingest         *ingest.Store
	Creds          *credentials.Store
	WorkspaceLabel func(id string) string
}

func (s Stores) workspaceLabel(id string) string {
	if s.WorkspaceLabel == nil {
		return id
	}
	return s.WorkspaceLabel(id)
}

type SettingsStore interface {
	List(ctx context.Context) (map[string]string, error)
	PutRaw(ctx context.Context, key, value string) error
}

var restoreOrderKeys = []string{
	"exportbundle.labels.restore_agents",
	"exportbundle.labels.restore_settings",
	"exportbundle.labels.restore_mcps",
	"exportbundle.labels.restore_boards",
	"exportbundle.labels.restore_projects",
	"exportbundle.labels.restore_init_scripts",
	"exportbundle.labels.restore_environments",
	"exportbundle.labels.restore_metrics",
	"exportbundle.labels.restore_tasks",
	"exportbundle.labels.restore_dependencies",
	"exportbundle.labels.restore_secrets",
}

func restoreOrder() []string {
	out := make([]string, len(restoreOrderKeys))
	for i, k := range restoreOrderKeys {
		out[i] = i18n.Text(k)
	}
	return out
}

func Export(ctx context.Context, s Stores, workspaceID string) (*Bundle, map[string][]byte, error) {
	if workspaceID == "" {
		return nil, nil, apperr.Invalid("exportbundle.errors.workspace_required")
	}
	b := &Bundle{
		Version:      Version,
		CreatedAt:    time.Now().UTC(),
		WorkspaceID:  workspaceID,
		Settings:     map[string]string{},
		RestoreOrder: restoreOrder(),
	}
	attachments := map[string][]byte{}

	if err := exportGlobal(ctx, s, b); err != nil {
		return nil, nil, err
	}
	exportBoards(s, b, workspaceID)
	if err := exportProjects(ctx, s, b, workspaceID, attachments); err != nil {
		return nil, nil, err
	}
	if err := exportTasks(ctx, s, b, workspaceID, attachments); err != nil {
		return nil, nil, err
	}
	if err := exportCredentials(ctx, s, b, workspaceID); err != nil {
		return nil, nil, err
	}
	return b, attachments, nil
}

func exportGlobal(ctx context.Context, s Stores, b *Bundle) error {
	pack, err := s.Agents.ExportCatalog(ctx, b.WorkspaceID)
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.export_agents")
	}
	b.Agents = pack

	if s.Settings != nil {
		set, sErr := s.Settings.List(ctx)
		if sErr != nil {
			return apperr.Wrap(sErr, "exportbundle.errors.export_settings")
		}
		b.Settings = set
	}

	mcps, err := s.MCP.List(ctx)
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.export_mcps")
	}
	b.MCPs = mcps
	return nil
}

func exportBoards(s Stores, b *Bundle, workspaceID string) {
	for _, info := range s.Boards.BoardList() {
		if info.WorkspaceID != workspaceID {
			continue
		}
		b.Boards = append(b.Boards, BoardSection{
			Info:      info,
			Stages:    s.Boards.Board(info.ID).List(),
			TaskTypes: s.Boards.BoardTypes(info.ID),
		})
	}
}

func exportProjects(ctx context.Context, s Stores, b *Bundle, workspaceID string, files map[string][]byte) error {
	projects, err := s.Projects.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.export_projects")
	}
	for i := range projects {
		p := &projects[i]
		ps := ProjectSection{Project: *p}
		code, codeErr := s.Projects.ExportCode(ctx, p)
		if codeErr != nil {
			return apperr.Wrap(codeErr, "exportbundle.errors.export_code", "project", p.ID)
		}
		if len(code) > 0 {
			ps.CodeTarPath = codeTarPath(p.ID)
			files[ps.CodeTarPath] = code
		}
		sc, scErr := s.Projects.InitScript(ctx, p.ID)
		switch {
		case errors.Is(scErr, project.ErrNoInitScript):
		case scErr != nil:
			return apperr.Wrap(scErr, "exportbundle.errors.export_init_script", "project", p.ID)
		default:
			ps.InitScript = sc
		}
		envs, envErr := s.Projects.ListEnvironments(ctx, p.ID)
		if envErr != nil {
			return apperr.Wrap(envErr, "exportbundle.errors.export_environments", "project", p.ID)
		}
		for j := range envs {
			es, esErr := exportEnv(ctx, s, envs[j])
			if esErr != nil {
				return esErr
			}
			ps.Environments = append(ps.Environments, es)
			for k := range es.IngestTokens {
				tok := &es.IngestTokens[k]
				b.SecretChecklist = append(b.SecretChecklist, SecretItem{
					Kind: "ingest_token", Name: tok.Name, Scope: "environment:" + envs[j].Name,
					Identity: tok.Prefix,
					Note:     i18n.Text("exportbundle.hints.ingest_token_reissue"),
				})
			}
		}
		b.Projects = append(b.Projects, ps)
	}
	return nil
}

func exportEnv(ctx context.Context, s Stores, e project.Environment) (EnvSection, error) {
	es := EnvSection{Environment: e}
	var err error
	if es.Mappings, err = s.Metrics.ListMappings(ctx, e.ID); err != nil {
		return es, apperr.Wrap(err, "exportbundle.errors.export_mappings", "environment", e.ID)
	}
	if es.CustomMetrics, err = s.Metrics.ListCustomMetrics(ctx, e.ID); err != nil {
		return es, apperr.Wrap(err, "exportbundle.errors.export_custom_metrics", "environment", e.ID)
	}
	if es.Dashboards, err = s.Metrics.ListDashboards(ctx, e.ID); err != nil {
		return es, apperr.Wrap(err, "exportbundle.errors.export_dashboards", "environment", e.ID)
	}
	if s.Ingest != nil {
		if es.IngestTokens, err = s.Ingest.ListTokens(ctx, e.ID); err != nil {
			return es, apperr.Wrap(err, "exportbundle.errors.export_ingest_tokens", "environment", e.ID)
		}
	}
	return es, nil
}

func exportTasks(ctx context.Context, s Stores, b *Bundle, workspaceID string, attachments map[string][]byte) error {
	tasks, err := s.Tasks.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.export_tasks")
	}
	for i := range tasks {
		ts, tErr := exportTask(ctx, s, tasks[i], attachments)
		if tErr != nil {
			return tErr
		}
		b.Tasks = append(b.Tasks, ts)
	}
	return nil
}

func exportTask(ctx context.Context, s Stores, t task.Task, attachments map[string][]byte) (TaskSection, error) {
	ts := TaskSection{Task: t}
	var err error
	if ts.Comments, err = s.Tasks.ListComments(ctx, t.ID); err != nil {
		return ts, apperr.Wrap(err, "exportbundle.errors.export_comments", "task", t.ID)
	}
	if ts.Reports, err = s.Tasks.ListReports(ctx, t.ID); err != nil {
		return ts, apperr.Wrap(err, "exportbundle.errors.export_reports", "task", t.ID)
	}
	if ts.Events, err = s.Tasks.ListEvents(ctx, t.ID); err != nil {
		return ts, apperr.Wrap(err, "exportbundle.errors.export_events", "task", t.ID)
	}
	if ts.TestCases, err = s.Tasks.ListTestCases(ctx, t.ID); err != nil {
		return ts, apperr.Wrap(err, "exportbundle.errors.export_test_cases", "task", t.ID)
	}
	if ts.Approvals, err = s.Tasks.ListApprovals(ctx, t.ID); err != nil {
		return ts, apperr.Wrap(err, "exportbundle.errors.export_approvals", "task", t.ID)
	}
	// Derived fields: the sections and comments carry the source rows, so the task row must not
	// duplicate them.
	ts.Task.Approvals, ts.Task.Flags, ts.Task.PendingApprovals = nil, nil, 0
	ts.Task.StatusReason = ""
	ts.Task.OpenQuestions = 0
	deps, depErr := s.Tasks.GetDependencies(ctx, t.ID)
	if depErr != nil {
		return ts, apperr.Wrap(depErr, "exportbundle.errors.export_dependencies", "task", t.ID)
	}
	for _, d := range deps {
		ts.Dependencies = append(ts.Dependencies, TaskDep{DependsOn: d.TaskID, Kind: d.Kind})
	}
	if err := exportAttachments(ctx, s, t.ID, &ts, attachments); err != nil {
		return ts, err
	}
	return ts, nil
}

func exportAttachments(ctx context.Context, s Stores, taskID string, ts *TaskSection, attachments map[string][]byte) error {
	atts, err := s.Tasks.ListAttachments(ctx, taskID)
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.export_attachments", "task", taskID)
	}
	for i := range atts {
		a := &atts[i]
		full, readErr := s.Tasks.GetAttachment(ctx, a.ID)
		if readErr != nil {
			continue
		}
		body, fileErr := os.ReadFile(full.Path)
		if fileErr != nil {
			continue
		}
		tp := attachTarPath(taskID, a.ID, a.Filename)
		attachments[tp] = body
		ts.Attachments = append(ts.Attachments, AttachmentSection{Attachment: *a, TarPath: tp})
	}
	return nil
}

func exportCredentials(ctx context.Context, s Stores, b *Bundle, workspaceID string) error {
	if s.Creds == nil {
		return nil
	}
	creds, err := s.Creds.List(ctx, workspaceID)
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.export_credentials")
	}
	for _, c := range creds {
		if c == nil {
			continue
		}
		b.Credentials = append(b.Credentials, *c)
		b.SecretChecklist = append(b.SecretChecklist, SecretItem{
			Kind: "credential", Name: c.Name, Scope: c.Provider + "/" + c.Kind,
			Identity: firstNonEmpty(c.Identity, c.Hint),
			Note:     i18n.Text("exportbundle.hints.credential_reenter"),
		})
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
