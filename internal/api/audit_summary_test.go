package api

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestAuditEntryViews_SummaryInReaderLocale(t *testing.T) {
	stored := i18n.T("en", "auth.audit.member_added", "email", "ana@example.com", "role", "admin")
	entries := []audit.Entry{
		{ID: "a", Summary: stored},
		{ID: "b", Summary: "written by hand, no message"},
	}
	views := auditEntryViews(entries, "es", stagepkg.StaticBoards())
	if want := i18n.T("es", "auth.audit.member_added", "email", "ana@example.com", "role", "administrador"); views[0].SummaryText != want {
		t.Errorf("summary_text = %q, want %q", views[0].SummaryText, want)
	}
	if views[0].Summary != stored {
		t.Errorf("the stored summary changed: %q", views[0].Summary)
	}
	if views[1].SummaryText != "written by hand, no message" {
		t.Errorf("unrecognized text changed: %q", views[1].SummaryText)
	}
	if got := auditEntryViews(nil, "es", stagepkg.StaticBoards()); got == nil || len(got) != 0 {
		t.Errorf("no entries should answer an empty list, got %#v", got)
	}
}

func TestAuditEntryViews_ValuesInReaderWords(t *testing.T) {
	cases := []struct{ stored, want string }{
		{
			i18n.T("es", "auth.audit.api_key_created", "name", "CI", "role", "developer",
				"permissions", "tasks:read,tasks:write"),
			"API key creada (rol desarrollador; Lectura: Tareas · Escritura: Tareas): CI",
		},
		{
			i18n.T("es", "auth.audit.api_key_created", "name", "Metabase (vieja)", "role", "viewer",
				"permissions", "tasks:read"),
			"API key creada (rol lector; Lectura: Tareas): Metabase (vieja)",
		},
		{
			i18n.T("en", "auth.audit.api_key_rotated", "name", "CI", "until", "2026-10-18 17:17 UTC"),
			"API key rotada: CI; la anterior funciona hasta 18 oct 2026, 17:17 UTC",
		},
		{
			i18n.T("es", "auth.audit.member_role_changed", "user", "beto@example.com", "role", "viewer"),
			"El rol del miembro beto@example.com cambió a lector",
		},
	}
	for _, c := range cases {
		if got := auditEntryViews([]audit.Entry{{Summary: c.stored}}, "es", stagepkg.StaticBoards())[0].SummaryText; got != c.want {
			t.Errorf("summary_text of %q = %q, want %q", c.stored, got, c.want)
		}
	}
	gh := i18n.T("es", "api.github.audit.delegated_token", "agent", "coder", "person", "Ana",
		"project", "web", "permissions", "contents:write")
	if got := auditEntryViews([]audit.Entry{{Summary: gh}}, "es", stagepkg.StaticBoards())[0].SummaryText; got != gh {
		t.Errorf("github summary changed: %q", got)
	}
}

type labeledBoards struct {
	stagepkg.Boards
	list []stagepkg.BoardInfo
}

func (b labeledBoards) BoardList() []stagepkg.BoardInfo { return b.list }

func TestAuditEntryViews_StageAndBoardNames(t *testing.T) {
	custom := stagepkg.SingleBoard(stagepkg.NewSnapshot([]types.StageInfo{
		{ID: types.StageBacklog, Label: "Backlog", Position: 10, Initial: true},
		{ID: "qa", Label: "QA manual", Position: 20},
		{ID: types.StageDone, Label: "Done", Position: 30, Terminal: true},
	}))
	boards := labeledBoards{Boards: custom, list: []stagepkg.BoardInfo{
		{ID: stagepkg.DefaultBoardID, Label: i18n.T("en", "stage.labels.default_board")},
		{ID: "prod", Label: "Producto"},
	}}
	cases := []struct{ stored, want string }{
		{
			i18n.T("en", "api.stages.audit.stage_updated", "stage", "backlog", "board", "default"),
			i18n.T("es", "api.stages.audit.stage_updated", "stage", "Backlog", "board", "Principal"),
		},
		{
			i18n.T("es", "api.stages.audit.stage_created", "stage", "qa", "board", "prod"),
			i18n.T("es", "api.stages.audit.stage_created", "stage", "QA manual", "board", "Producto"),
		},
		{
			i18n.T("en", "api.stages.audit.stage_deleted", "stage", "review", "board", "default", "count", 2, "dest", "done"),
			i18n.T("es", "api.stages.audit.stage_deleted", "stage", i18n.T("es", "stage.labels.review"),
				"board", "Principal", "count", 2, "dest", i18n.T("es", "stage.labels.done")),
		},
		{
			i18n.T("en", "api.stages.audit.stage_updated", "stage", "qa_gone", "board", "board_gone"),
			i18n.T("es", "api.stages.audit.stage_updated", "stage", "qa_gone", "board", "board_gone"),
		},
		{
			i18n.T("en", "api.boards.audit.board_deleted", "board", "old", "count", 1, "dest", "default"),
			i18n.T("es", "api.boards.audit.board_deleted", "board", "old", "count", 1, "dest", "Principal"),
		},
		{
			i18n.T("en", "api.boards.audit.default_board_changed", "board", "prod"),
			i18n.T("es", "api.boards.audit.default_board_changed", "board", "Producto"),
		},
	}
	for _, c := range cases {
		if got := auditEntryViews([]audit.Entry{{Summary: c.stored}}, "es", boards)[0].SummaryText; got != c.want {
			t.Errorf("summary_text of %q = %q, want %q", c.stored, got, c.want)
		}
	}
}

func TestAuditEntryViews_UpdateResult(t *testing.T) {
	cases := []struct{ stored, want string }{
		{
			i18n.T("en", "api.updates.audit.reported", "from", "dev", "to", "0.3.3", "result", "ok"),
			i18n.T("es", "api.updates.audit.reported", "from", "dev", "to", "0.3.3", "result", "correcta"),
		},
		{
			i18n.T("es", "api.updates.audit.reported", "from", i18n.T("en", "api.updates.labels.unknown_version"),
				"to", "0.3.3", "result", "rolled_back"),
			i18n.T("es", "api.updates.audit.reported", "from", "desconocida", "to", "0.3.3", "result", "revertida"),
		},
	}
	for _, c := range cases {
		if got := auditEntryViews([]audit.Entry{{Summary: c.stored}}, "es", stagepkg.StaticBoards())[0].SummaryText; got != c.want {
			t.Errorf("summary_text of %q = %q, want %q", c.stored, got, c.want)
		}
	}
}

func TestAuditEntryViews_LegacyWording(t *testing.T) {
	stored := "API key created: CI (developer; tasks:read)"
	if old := i18n.T("en", "auth.audit_legacy.api_key_created", "name", "CI", "role", "developer",
		"permissions", "tasks:read"); old != stored {
		t.Fatalf("legacy wording = %q, want the one older daemons wrote, %q", old, stored)
	}
	want := "API key creada (rol desarrollador; Lectura: Tareas): CI"
	if got := auditEntryViews([]audit.Entry{{Summary: stored}}, "es", stagepkg.StaticBoards())[0].SummaryText; got != want {
		t.Errorf("summary_text = %q, want %q", got, want)
	}
}

func TestIsAuditSummaryKey(t *testing.T) {
	for key, want := range map[string]bool{
		"auth.audit.member_added":           true,
		"api.webhooks.audit.secret_rotated": true,
		"audit.comments.retention_purged":   true,
		"audit.errors.schema":               false,
		"auth.errors.internal":              false,
	} {
		if got := isAuditSummaryKey(key); got != want {
			t.Errorf("isAuditSummaryKey(%q) = %v, want %v", key, got, want)
		}
	}
}
