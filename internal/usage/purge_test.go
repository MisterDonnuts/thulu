package usage

import (
	"context"
	"testing"
	"time"
)

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), s.rb(query), args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func seedPurgeSessions(t *testing.T, s *Store) {
	t.Helper()
	now := time.Now().UTC()
	for _, row := range []struct{ id, task, ws string }{
		{"run-1", "task-a", "ws-a"},
		{"run-2", "task-a", "ws-a"},
		{"run-3", "task-b", "ws-a"},
		{"run-4", "task-c", "ws-b"},
		{"run-5", "", ""},
	} {
		sess := sessionAt(row.id, now)
		sess.TaskID, sess.WorkspaceID = row.task, row.ws
		sess.LogsFile = "/data/logs/" + row.id + ".log"
		sess.OutputDir = "/agents/dev/runs/" + row.id
		if err := s.Add(t.Context(), sess); err != nil {
			t.Fatalf("Add %s: %v", row.id, err)
		}
		if err := s.AddToolEvents(t.Context(), []ToolEvent{{SessionID: row.id, TS: now, ToolName: "Read"}}); err != nil {
			t.Fatalf("AddToolEvents %s: %v", row.id, err)
		}
	}
}

func TestDeleteByTask(t *testing.T) {
	s := newTestStore(t)
	seedPurgeSessions(t, s)
	rollupBefore := countRows(t, s, `SELECT COUNT(*) FROM usage_daily`)

	files, err := s.DeleteByTask(t.Context(), "task-a")
	if err != nil {
		t.Fatalf("DeleteByTask: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v, want the two task-a runs", files)
	}
	for _, f := range files {
		if f.LogsFile == "" || f.OutputDir == "" {
			t.Errorf("file paths lost: %+v", f)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM sessions WHERE task_id = ?`, "task-a"); n != 0 {
		t.Errorf("task-a sessions left: %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM session_tool_events WHERE session_id IN ('run-1','run-2')`); n != 0 {
		t.Errorf("task-a tool events left: %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM sessions`); n != 3 {
		t.Errorf("other sessions touched: %d left, want 3", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM usage_daily`); n != rollupBefore {
		t.Errorf("usage_daily rows = %d, want %d (task delete keeps spend history)", n, rollupBefore)
	}
	if files, err := s.DeleteByTask(t.Context(), ""); err != nil || len(files) != 0 {
		t.Errorf("DeleteByTask(\"\") = %v, %v; want nothing", files, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM sessions WHERE id = 'run-5'`); n != 1 {
		t.Error("unattributed session was deleted")
	}
}

func TestDeleteByWorkspace(t *testing.T) {
	s := newTestStore(t)
	seedPurgeSessions(t, s)

	files, err := s.DeleteByWorkspace(t.Context(), "ws-a")
	if err != nil {
		t.Fatalf("DeleteByWorkspace: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("files = %+v, want the three ws-a runs", files)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM sessions WHERE workspace_id = ?`, "ws-a"); n != 0 {
		t.Errorf("ws-a sessions left: %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM usage_daily WHERE workspace_id = ?`, "ws-a"); n != 0 {
		t.Errorf("ws-a rollup rows left: %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM session_tool_events`); n != 2 {
		t.Errorf("tool events left = %d, want ws-b's and the unattributed one", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM usage_daily WHERE workspace_id = ?`, "ws-b"); n == 0 {
		t.Error("ws-b rollup was erased too")
	}
	if files, err := s.DeleteByWorkspace(t.Context(), ""); err != nil || len(files) != 0 {
		t.Errorf("DeleteByWorkspace(\"\") = %v, %v; want nothing", files, err)
	}
}
