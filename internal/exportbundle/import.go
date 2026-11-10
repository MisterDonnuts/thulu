package exportbundle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type Stats struct {
	Agents       int      `json:"agents"`
	Skills       int      `json:"skills"`
	Settings     int      `json:"settings"`
	MCPs         int      `json:"mcps"`
	Boards       int      `json:"boards"`
	Projects     int      `json:"projects"`
	Environments int      `json:"environments"`
	InitScripts  int      `json:"init_scripts"`
	Mappings     int      `json:"mappings"`
	Metrics      int      `json:"custom_metrics"`
	Dashboards   int      `json:"dashboards"`
	Tasks        int      `json:"tasks"`
	Attachments  int      `json:"attachments"`
	Dependencies int      `json:"dependencies"`
	Warnings     []string `json:"warnings,omitempty"`
}

func Import(ctx context.Context, s Stores, b *Bundle, attachments map[string][]byte, attachmentsDir string) (Stats, error) {
	var st Stats
	if b.Version != Version {
		return st, apperr.Invalid("exportbundle.errors.version_unsupported", "version", b.Version, "want", Version)
	}
	if err := checkOwnership(ctx, s, b); err != nil {
		return st, err
	}
	if err := importGlobal(ctx, s, b, &st); err != nil {
		return st, err
	}
	if err := importBoards(ctx, s, b, &st); err != nil {
		return st, err
	}
	if err := importProjects(ctx, s, b, attachments, &st); err != nil {
		return st, err
	}
	if err := importTasks(ctx, s, b, attachments, attachmentsDir, &st); err != nil {
		return st, err
	}
	importDependencies(ctx, s, b, &st)
	return st, nil
}

// Importing into another workspace of the same install would keep existing tasks in place while
// overwriting
// their boards and projects; refuse before writing anything.
func checkOwnership(ctx context.Context, s Stores, b *Bundle) error {
	boards := map[string]string{}
	for _, info := range s.Boards.BoardList() {
		boards[info.ID] = info.WorkspaceID
	}
	for i := range b.Boards {
		info := b.Boards[i].Info
		if have, ok := boards[info.ID]; ok {
			if err := s.ownerConflict(info.ID, have, info.WorkspaceID,
				"exportbundle.errors.board_owned_elsewhere", "exportbundle.errors.board_owned_by_fallback"); err != nil {
				return err
			}
		}
	}
	for i := range b.Projects {
		want := &b.Projects[i].Project
		have, found, err := s.projectOwner(ctx, want.ID)
		if err == nil && found {
			err = s.ownerConflict(want.ID, have, want.WorkspaceID,
				"exportbundle.errors.project_owned_elsewhere", "exportbundle.errors.project_owned_by_fallback")
		}
		if err != nil {
			return err
		}
	}
	for i := range b.Tasks {
		want := &b.Tasks[i].Task
		have, found, err := s.taskOwner(ctx, want.ID)
		if err == nil && found {
			err = s.ownerConflict(want.ID, have, want.WorkspaceID,
				"exportbundle.errors.task_owned_elsewhere", "exportbundle.errors.task_owned_by_fallback")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s Stores) projectOwner(ctx context.Context, id string) (owner string, found bool, err error) {
	have, err := s.Projects.Get(ctx, id)
	if apperr.KindOf(err) == apperr.KindNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return have.WorkspaceID, true, nil
}

func (s Stores) taskOwner(ctx context.Context, id string) (owner string, found bool, err error) {
	have, err := s.Tasks.Get(ctx, id)
	if apperr.KindOf(err) == apperr.KindNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return have.WorkspaceID, true, nil
}

func (s Stores) ownerConflict(id, have, want, elsewhereKey, fallbackKey string) error {
	if have == want {
		return nil
	}
	key := elsewhereKey
	if have == stage.FallbackWorkspaceID {
		key = fallbackKey
	}
	return apperr.Conflict(key, "id", id, "workspace", s.workspaceLabel(have), "target", s.workspaceLabel(want))
}

func importGlobal(ctx context.Context, s Stores, b *Bundle, st *Stats) error {
	packStats, err := s.Agents.ImportCatalog(ctx, b.WorkspaceID, b.Agents, "import")
	if err != nil {
		return apperr.Wrap(err, "exportbundle.errors.import_agents")
	}
	st.Agents, st.Skills = packStats.Agents, packStats.Skills

	if s.Settings != nil {
		for k, v := range b.Settings {
			// ingest.public_url belongs to the destination host; importing it would point telemetry
			// at the old one.
			if k == settings.IngestPublicURLKey {
				st.Warnings = append(st.Warnings, i18n.Text("exportbundle.warnings.public_url_skipped", "key", k))
				continue
			}
			if err := s.Settings.PutRaw(ctx, k, string(agentstore.RenameLegacy([]byte(v)))); err != nil {
				return apperr.Wrap(err, "exportbundle.errors.import_setting", "key", k)
			}
			st.Settings++
		}
	}

	for i := range b.MCPs {
		m := &b.MCPs[i]
		m.Name = string(agentstore.RenameLegacy([]byte(m.Name)))
		m.URL = string(agentstore.RenameLegacy([]byte(m.URL)))
		m.Description = string(agentstore.RenameLegacy([]byte(m.Description)))
		_, regErr := s.MCP.Register(ctx, *m)
		if errors.Is(regErr, mcp.ErrRegistrationExists) {
			if _, upErr := s.MCP.Update(ctx, m.Name, mcp.Patch{
				URL: m.URL, Description: m.Description, ForwardsCredential: &m.ForwardsCredential,
				Transport: &m.Transport, Auth: &m.Auth, CredentialRef: &m.CredentialRef, HeaderTemplate: &m.HeaderTemplate,
			}); upErr != nil {
				return apperr.Wrap(upErr, "exportbundle.errors.update_mcp", "name", m.Name)
			}
		} else if regErr != nil {
			return apperr.Wrap(regErr, "exportbundle.errors.import_mcp", "name", m.Name)
		}
		st.MCPs++
	}
	return nil
}

func importBoards(ctx context.Context, s Stores, b *Bundle, st *Stats) error {
	existing := map[string]bool{}
	for _, info := range s.Boards.BoardList() {
		existing[info.ID] = true
	}
	var defaults []string
	for i := range b.Boards {
		bs := &b.Boards[i]
		if existing[bs.Info.ID] {
			if err := s.Boards.UpdateBoard(ctx, bs.Info); err != nil {
				return apperr.Wrap(err, "exportbundle.errors.update_board", "board", bs.Info.ID)
			}
			if err := s.Boards.SaveBoard(ctx, bs.Info.ID, bs.Stages); err != nil {
				return apperr.Wrap(err, "exportbundle.errors.save_board_stages", "board", bs.Info.ID)
			}
		} else if err := s.Boards.CreateBoard(ctx, bs.Info, bs.Stages); err != nil {
			return apperr.Wrap(err, "exportbundle.errors.create_board", "board", bs.Info.ID)
		}
		if err := s.Boards.SaveBoardTypes(ctx, bs.Info.ID, bs.TaskTypes); err != nil {
			return apperr.Wrap(err, "exportbundle.errors.save_board_types", "board", bs.Info.ID)
		}
		if bs.Info.Default {
			defaults = append(defaults, bs.Info.ID)
		}
		st.Boards++
	}
	for _, id := range defaults {
		if err := s.Boards.SetDefaultBoard(ctx, id); err != nil {
			return apperr.Wrap(err, "exportbundle.errors.set_default_board", "board", id)
		}
	}
	return nil
}

func importProjects(ctx context.Context, s Stores, b *Bundle, files map[string][]byte, st *Stats) error {
	for i := range b.Projects {
		ps := &b.Projects[i]
		if err := s.Projects.ImportProject(ctx, ps.Project); err != nil {
			return err
		}
		st.Projects++
		if err := importCode(ctx, s, ps, files); err != nil {
			return err
		}
		if ps.InitScript != nil {
			sc := *ps.InitScript
			sc.ProjectID = ps.Project.ID
			wrote, err := s.Projects.ImportInitScript(ctx, sc)
			if err != nil {
				return err
			}
			if wrote {
				st.InitScripts++
			}
		}
		for j := range ps.Environments {
			if err := importEnv(ctx, s, &ps.Environments[j], st); err != nil {
				return err
			}
		}
	}
	return nil
}

func importCode(ctx context.Context, s Stores, ps *ProjectSection, files map[string][]byte) error {
	if ps.CodeTarPath == "" {
		return nil
	}
	code, ok := files[ps.CodeTarPath]
	if !ok {
		return apperr.Invalid("exportbundle.errors.code_missing", "project", ps.Project.ID, "path", ps.CodeTarPath)
	}
	return s.Projects.RestoreCode(ctx, &ps.Project, code)
}

func importEnv(ctx context.Context, s Stores, es *EnvSection, st *Stats) error {
	if err := s.Projects.ImportEnvironment(ctx, es.Environment); err != nil {
		return err
	}
	st.Environments++
	for k := range es.Mappings {
		if err := s.Metrics.ImportMapping(ctx, es.Mappings[k]); err != nil {
			return err
		}
		st.Mappings++
	}
	for k := range es.CustomMetrics {
		if err := s.Metrics.ImportCustomMetric(ctx, es.CustomMetrics[k]); err != nil {
			return err
		}
		st.Metrics++
	}
	for k := range es.Dashboards {
		if err := s.Metrics.ImportDashboard(ctx, es.Dashboards[k]); err != nil {
			return err
		}
		st.Dashboards++
	}
	return nil
}

func importTasks(ctx context.Context, s Stores, b *Bundle, attachments map[string][]byte, attachmentsDir string, st *Stats) error {
	for i := range b.Tasks {
		ts := &b.Tasks[i]
		t := ts.Task
		wrote, err := s.Tasks.ImportTask(ctx, &t)
		if err != nil {
			return err
		}
		if wrote {
			st.Tasks++
		}
		if err := importTaskChildren(ctx, s, ts, attachments, attachmentsDir, st); err != nil {
			return err
		}
	}
	return nil
}

func importTaskChildren(ctx context.Context, s Stores, ts *TaskSection, attachments map[string][]byte, attachmentsDir string, st *Stats) error {
	for j := range ts.Attachments {
		if err := importAttachment(ctx, s.Tasks, &ts.Attachments[j], attachments, attachmentsDir); err != nil {
			st.Warnings = append(st.Warnings, apperr.Text(err))
			continue
		}
		st.Attachments++
	}
	for j := range ts.Comments {
		if err := s.Tasks.ImportComment(ctx, ts.Comments[j]); err != nil {
			return err
		}
	}
	for j := range ts.Reports {
		if err := s.Tasks.ImportReport(ctx, ts.Reports[j]); err != nil {
			return err
		}
	}
	for j := range ts.Events {
		if err := s.Tasks.ImportEvent(ctx, ts.Events[j]); err != nil {
			return err
		}
	}
	for j := range ts.Approvals {
		if err := s.Tasks.ImportApproval(ctx, ts.Approvals[j]); err != nil {
			return err
		}
	}
	return importTestCases(ctx, s.Tasks, ts.TestCases, st)
}

func importTestCases(ctx context.Context, store *task.Store, cases []task.TestCase, st *Stats) error {
	for i := range cases {
		kept, err := store.ImportTestCase(ctx, cases[i])
		if err != nil {
			return err
		}
		if !kept {
			st.Warnings = append(st.Warnings, i18n.Text("exportbundle.warnings.test_case_key_taken",
				"key", cases[i].Key, "id", cases[i].ID, "task", cases[i].TaskID))
			continue
		}
		for j := range cases[i].Runs {
			if err := store.ImportTestCaseRun(ctx, cases[i].Runs[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

func importDependencies(ctx context.Context, s Stores, b *Bundle, st *Stats) {
	for i := range b.Tasks {
		ts := &b.Tasks[i]
		for _, d := range ts.Dependencies {
			kind := d.Kind
			if !kind.Valid() {
				kind = task.DepSoft
			}
			if err := s.Tasks.AddDependency(ctx, ts.Task.ID, d.DependsOn, kind); err != nil {
				st.Warnings = append(st.Warnings, i18n.Text("exportbundle.warnings.dependency_failed",
					"task", ts.Task.ID, "depends_on", d.DependsOn, "err", apperr.Text(err)))
				continue
			}
			st.Dependencies++
		}
	}
}

// Must match the daemon's <taskID>/<size>-<filename> attachment layout.
func importAttachment(ctx context.Context, store *task.Store, as *AttachmentSection, attachments map[string][]byte, attachmentsDir string) error {
	body, ok := attachments[as.TarPath]
	if !ok {
		return apperr.Invalid("exportbundle.errors.attachment_bytes_missing", "attachment", as.Attachment.ID, "path", as.TarPath)
	}
	a := as.Attachment
	relDir := filepath.Join(attachmentsDir, a.TaskID)
	if err := os.MkdirAll(relDir, 0o750); err != nil {
		return apperr.Wrap(err, "exportbundle.errors.attachment", "attachment", a.ID)
	}
	name := fmt.Sprintf("%d-%s", a.Size, task.SanitizeFilename(a.Filename))
	dst := filepath.Join(relDir, name)
	if err := os.WriteFile(dst, body, 0o640); err != nil {
		return apperr.Wrap(err, "exportbundle.errors.attachment", "attachment", a.ID)
	}
	a.Path = dst
	return store.ImportAttachmentRow(ctx, a)
}
