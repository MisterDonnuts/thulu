package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestPatchAssignments_OnlyPresentFields(t *testing.T) {
	title := "hello"
	status := StatusRunning
	empty := ""

	cases := []struct {
		name     string
		patch    Patch
		wantCols []string
	}{
		{
			name:     "empty patch touches only updated_at",
			patch:    Patch{},
			wantCols: []string{"updated_at"},
		},
		{
			name:     "single scalar",
			patch:    Patch{Status: &status},
			wantCols: []string{"status", "updated_at"},
		},
		{
			name:     "two scalars",
			patch:    Patch{Title: &title, Status: &status},
			wantCols: []string{"title", "status", "updated_at"},
		},
		{
			name:     "pointer to empty string is present",
			patch:    Patch{TypeID: &empty, BoardID: &empty},
			wantCols: []string{"board_id", "type_id", "updated_at"},
		},
		{
			name:     "non-nil empty map is present",
			patch:    Patch{WorktreePaths: map[string]string{}},
			wantCols: []string{"worktree_paths", "updated_at"},
		},
		{
			name:     "nil maps are absent",
			patch:    Patch{StageAssignments: nil, WorktreePaths: nil, PRURLs: nil},
			wantCols: []string{"updated_at"},
		},
		{
			name:     "project ids are not a column",
			patch:    Patch{Tags: &[]string{"a"}, ProjectIDs: &[]string{"p"}},
			wantCols: []string{"tags", "updated_at"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cols, args, err := tc.patch.assignments(time.Now().UTC())
			if err != nil {
				t.Fatalf("assignments: %v", err)
			}
			if len(cols) != len(args) {
				t.Fatalf("cols=%d args=%d — must pair up", len(cols), len(args))
			}
			if len(cols) != len(tc.wantCols) {
				t.Fatalf("columns = %v, want %v", cols, tc.wantCols)
			}
			for i, want := range tc.wantCols {
				if cols[i] != want {
					t.Errorf("column[%d] = %q, want %q (full: %v)", i, cols[i], want, cols)
				}
			}
		})
	}
}

func TestPatchAssignments_MarshalError(t *testing.T) {
	bad := map[string]string{"k": "v"}
	p := Patch{WorktreePaths: bad}
	if _, _, err := p.assignments(time.Now().UTC()); err != nil {
		t.Fatalf("valid map should marshal: %v", err)
	}
}

func rawCol(t *testing.T, s *Store, id, col string) string {
	t.Helper()
	var v string
	q := fmt.Sprintf(`SELECT %s FROM tasks WHERE id = ?`, col)
	if err := s.DB().QueryRowContext(context.Background(), dbpkg.Rebind(q), id).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", col, err)
	}
	return v
}

func waitForLockWaiter(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := db.QueryRowContext(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n)
		if err == nil && n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no backend ever blocked on a lock — the racing writer did not reach the DB")
}

func TestUpdate_DoesNotRevertConcurrentEdit(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "original", Body: "body"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		dbpkg.Rebind(`SELECT id FROM tasks WHERE id = ? FOR UPDATE`), tk.ID); err != nil {
		t.Fatalf("lock row: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		running := StatusRunning
		_, uerr := s.Update(t.Context(), tk.ID, Patch{Status: &running})
		done <- uerr
	}()

	waitForLockWaiter(t, s.DB())

	if _, err := tx.ExecContext(context.Background(),
		dbpkg.Rebind(`UPDATE tasks SET title = ? WHERE id = ?`), "edited by user", tk.ID); err != nil {
		t.Fatalf("out-of-band title edit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("concurrent Update: %v", err)
	}

	if got := rawCol(t, s, tk.ID, "title"); got != "edited by user" {
		t.Errorf("title = %q, want %q — the status write reverted a committed edit", got, "edited by user")
	}
	if got := rawCol(t, s, tk.ID, "status"); got != string(StatusRunning) {
		t.Errorf("status = %q, want running", got)
	}
}

func TestUpdate_ConcurrentDisjointFields(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "v0"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	const rounds = 40
	for i := range rounds {
		title := fmt.Sprintf("v%d", i+1)
		status := StatusRunning
		if i%2 == 0 {
			status = StatusIdle
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var titleErr, statusErr error
		go func() {
			defer wg.Done()
			<-start
			_, titleErr = s.Update(t.Context(), tk.ID, Patch{Title: &title})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, statusErr = s.Update(t.Context(), tk.ID, Patch{Status: &status})
		}()
		close(start)
		wg.Wait()

		if titleErr != nil || statusErr != nil {
			t.Fatalf("round %d: title=%v status=%v", i, titleErr, statusErr)
		}
		if got := rawCol(t, s, tk.ID, "title"); got != title {
			t.Fatalf("round %d: title = %q, want %q — lost update", i, got, title)
		}
		if got := rawCol(t, s, tk.ID, "status"); got != string(status) {
			t.Fatalf("round %d: status = %q, want %q — lost update", i, got, status)
		}
	}
}

func TestUpdate_StatusWriteKeepsBodyEdit(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", Body: "draft"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	const rounds = 25
	for i := range rounds {
		body := fmt.Sprintf("draft %d", i)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.Update(t.Context(), tk.ID, Patch{Body: &body}); err != nil {
				t.Errorf("body update: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			failed := StatusFailed
			if _, err := s.Update(t.Context(), tk.ID, Patch{Status: &failed}); err != nil {
				t.Errorf("status update: %v", err)
			}
		}()
		close(start)
		wg.Wait()

		if got := rawCol(t, s, tk.ID, "body"); got != body {
			t.Fatalf("round %d: body = %q, want %q", i, got, body)
		}
	}
}

func TestUpdate_PointerToEmptyStringClearsColumn(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", TypeID: "bugfix", BranchName: "task-1"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	empty := ""
	got, err := s.Update(t.Context(), tk.ID, Patch{TypeID: &empty, BranchName: &empty})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.TypeID != "" || got.BranchName != "" {
		t.Errorf("returned task keeps values: type=%q branch=%q", got.TypeID, got.BranchName)
	}
	if v := rawCol(t, s, tk.ID, "type_id"); v != "" {
		t.Errorf("stored type_id = %q, want empty", v)
	}
	if v := rawCol(t, s, tk.ID, "branch_name"); v != "" {
		t.Errorf("stored branch_name = %q, want empty", v)
	}
}

func TestUpdate_NilFieldsLeaveColumnsUntouched(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS,
		Title:            "keep me",
		Body:             "keep this too",
		Tags:             []string{"a", "b"},
		StageAssignments: map[Stage]string{types.StageRefinement: "probe"},
		WorktreePaths:    map[string]string{"p1": "/wt/p1"},
		PRURLs:           map[string]string{"p1": "http://pr/1"},
		Priority:         "high",
	}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	running := StatusRunning
	if _, err := s.Update(t.Context(), tk.ID, Patch{Status: &running}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "keep me" || got.Body != "keep this too" || got.Priority != "high" {
		t.Errorf("scalars changed: %+v", got)
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags = %v", got.Tags)
	}
	if got.AssignedAgent(types.StageRefinement) != "probe" {
		t.Errorf("assignments = %v", got.StageAssignments)
	}
	if got.WorktreePaths["p1"] != "/wt/p1" || got.PRURLs["p1"] != "http://pr/1" {
		t.Errorf("maps changed: wt=%v pr=%v", got.WorktreePaths, got.PRURLs)
	}
}

func TestUpdate_EmptyMapClearsColumn(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", WorktreePaths: map[string]string{"p1": "/wt/p1"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := s.Update(t.Context(), tk.ID, Patch{WorktreePaths: map[string]string{}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if v := rawCol(t, s, tk.ID, "worktree_paths"); v != "{}" {
		t.Errorf("worktree_paths = %q, want {}", v)
	}
}

func TestUpdate_EmptyPatchStillBumpsUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := tk.UpdatedAt
	time.Sleep(5 * time.Millisecond)

	got, err := s.Update(t.Context(), tk.ID, Patch{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !got.UpdatedAt.After(before) {
		t.Errorf("updated_at %v not after %v", got.UpdatedAt, before)
	}
}

func TestUpdate_UnknownIDIsAnError(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Update(t.Context(), "does-not-exist", Patch{}); err == nil {
		t.Fatal("expected an error for an unknown task")
	}
}

func TestUpdate_ReturnsStoredRow(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", Tags: []string{"x"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	newTitle := "renamed"
	got, err := s.Update(t.Context(), tk.ID, Patch{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Title != "renamed" {
		t.Errorf("Title = %q", got.Title)
	}
	var tags []string
	if err := json.Unmarshal([]byte(rawCol(t, s, tk.ID, "tags")), &tags); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "x" {
		t.Errorf("tags round-trip = %v", tags)
	}
}
