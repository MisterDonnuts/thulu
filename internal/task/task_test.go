package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(dir, "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestStageFlow(t *testing.T) {
	reg := stagepkg.Static()
	if !reg.Valid(types.StageBacklog) {
		t.Error("StageBacklog should be valid")
	}
	if reg.Valid(types.Stage("nope")) {
		t.Error("invalid stage should not be valid")
	}
	if next := reg.Next(types.StageBacklog); next != types.StageInProgress {
		t.Errorf("Next(backlog) = %q want in_progress", next)
	}
	if next := reg.Next(types.StageDone); next != "" {
		t.Errorf("Done is terminal, got next %q", next)
	}
}

func TestCreateAndGet(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS,
		Title:      "smoke",
		Body:       "test body",
		ProjectIDs: []string{"proj-1"},
		StageAssignments: map[Stage]string{
			types.StageRefinement: "probe",
		},
		Tags: []string{"foo", "bar"},
	}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tk.ID == "" {
		t.Fatal("ID not set")
	}
	if tk.Stage != types.StageBacklog {
		t.Errorf("default stage = %q want backlog", tk.Stage)
	}
	got, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "smoke" || len(got.ProjectIDs) != 1 || got.ProjectIDs[0] != "proj-1" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.AssignedAgent(types.StageRefinement) != "probe" {
		t.Errorf("StageAssignments not persisted: %v", got.StageAssignments)
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags = %v", got.Tags)
	}
}

func TestDeleteCascades(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"p"}}
	store.Create(t.Context(), tk)
	store.AddComment(t.Context(), tk.ID, SourceUser, "u", "hi")
	store.AddFlag(t.Context(), tk.ID, &Flag{Kind: FlagArchDecision, Label: "pick a db"})
	store.RecordEvent(t.Context(), tk.ID, EventTaskCreated, "", "u", "x", nil)

	if err := store.Delete(t.Context(), tk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(t.Context(), tk.ID); err == nil {
		t.Error("task still exists after delete")
	}
	if cs, _ := store.ListComments(t.Context(), tk.ID); len(cs) != 0 {
		t.Error("comments not cascaded")
	}
	if fs, _ := store.ListFlags(t.Context(), tk.ID); len(fs) != 0 {
		t.Error("flags not cascaded")
	}
	if es, _ := store.ListEvents(t.Context(), tk.ID); len(es) != 0 {
		t.Error("events not cascaded")
	}
}

func TestFlagsLifecycle(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"p"}}
	store.Create(t.Context(), tk)

	f := &Flag{Kind: FlagInfraImpact, Label: "needs new docker image", CreatedBy: "agent:probe"}
	if err := store.AddFlag(t.Context(), tk.ID, f); err != nil {
		t.Fatalf("AddFlag: %v", err)
	}
	if f.ID == "" {
		t.Fatal("flag id not set")
	}
	if f.Status != FlagPending {
		t.Errorf("default status = %q", f.Status)
	}

	pending, _ := store.PendingFlags(t.Context(), tk.ID)
	if len(pending) != 1 {
		t.Fatalf("PendingFlags = %d", len(pending))
	}

	approved := FlagApproved
	user := "alice"
	label := "approved label"
	if _, err := store.UpdateFlag(t.Context(), f.ID, FlagPatch{Status: &approved, ResolvedBy: &user, Label: &label}); err != nil {
		t.Fatalf("UpdateFlag: %v", err)
	}

	pending2, _ := store.PendingFlags(t.Context(), tk.ID)
	if len(pending2) != 0 {
		t.Errorf("after approval, pending = %d", len(pending2))
	}

	got, _ := store.GetFlag(t.Context(), f.ID)
	if got.ResolvedAt == nil {
		t.Error("ResolvedAt not stamped on approval")
	}
	if got.ResolvedBy != "alice" {
		t.Errorf("ResolvedBy = %q", got.ResolvedBy)
	}
	if got.Label != "approved label" {
		t.Errorf("Label patch not applied: %q", got.Label)
	}
}

func TestPendingFlagsCountsRejected(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"p"}}
	store.Create(t.Context(), tk)
	f := &Flag{Kind: FlagUXDesign, Label: "drawer"}
	store.AddFlag(t.Context(), tk.ID, f)

	rejected := FlagRejected
	store.UpdateFlag(t.Context(), f.ID, FlagPatch{Status: &rejected})

	pending, _ := store.PendingFlags(t.Context(), tk.ID)
	if len(pending) != 1 {
		t.Errorf("rejected flag should still block; pending = %d", len(pending))
	}
}

func TestParseTaskInstanceID(t *testing.T) {
	cases := []struct {
		name       string
		instanceID string
		wantTask   string
		wantStage  Stage
		wantOK     bool
	}{
		{
			name:       "canonical",
			instanceID: "probe-task-bbbb000000000001-in_progress-1778106359027",
			wantTask:   "bbbb000000000001",
			wantStage:  "in_progress",
			wantOK:     true,
		},
		{
			name:       "agent name with dashes",
			instanceID: "my-custom-agent-task-abc123-refinement-1234567890",
			wantTask:   "abc123",
			wantStage:  "refinement",
			wantOK:     true,
		},
		{
			name:       "no -task- anchor",
			instanceID: "manual-container-foo",
			wantOK:     false,
		},
		{
			name:       "missing timestamp",
			instanceID: "probe-task-abc-in_progress-",
			wantOK:     false,
		},
		{
			name:       "non-numeric timestamp",
			instanceID: "probe-task-abc-in_progress-notanumber",
			wantOK:     false,
		},
		{
			// A run for a stage deleted mid-flight must still parse so its post-run side effects
			// are processed.
			name:       "unknown but well-formed stage",
			instanceID: "probe-task-abc-not_a_stage-1234567890",
			wantTask:   "abc",
			wantStage:  "not_a_stage",
			wantOK:     true,
		},
		{
			name:       "malformed stage segment",
			instanceID: "probe-task-abc-NOTASTAGE-1234567890",
			wantOK:     false,
		},
		{
			name:       "all canonical stages parse",
			instanceID: "sonnet-task-deadbeef-ai_review-9999",
			wantTask:   "deadbeef",
			wantStage:  "ai_review",
			wantOK:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotTask, gotStage, ok := parseTaskInstanceID(c.instanceID)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if gotTask != c.wantTask {
				t.Errorf("taskID = %q, want %q", gotTask, c.wantTask)
			}
			if gotStage != c.wantStage {
				t.Errorf("stage = %q, want %q", gotStage, c.wantStage)
			}
		})
	}
}

func TestHasFinishEventForInstance(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	store.Create(t.Context(), tk)

	if store.HasFinishEventForInstance(t.Context(), "nonexistent") {
		t.Error("empty store should not report event")
	}
	if store.HasFinishEventForInstance(t.Context(), "") {
		t.Error("empty instanceID must always be false")
	}

	store.RecordEvent(t.Context(), tk.ID, EventAgentRunFinished, "instance-X", "octopus", "done", nil)

	if !store.HasFinishEventForInstance(t.Context(), "instance-X") {
		t.Error("recorded finish should be detected")
	}
	if store.HasFinishEventForInstance(t.Context(), "instance-Y") {
		t.Error("different instanceID should not match")
	}
}

func TestAssignedAgent_NilAssignments(t *testing.T) {
	tk := &Task{WorkspaceID: testWS, StageAssignments: nil}
	if got := tk.AssignedAgent(types.StageBacklog); got != "" {
		t.Errorf("expected empty string for nil StageAssignments, got %q", got)
	}
}

func TestAssignedAgent_WithAssignment(t *testing.T) {
	tk := &Task{WorkspaceID: testWS, StageAssignments: map[Stage]string{types.StageRefinement: "sonnet"}}
	if got := tk.AssignedAgent(types.StageRefinement); got != "sonnet" {
		t.Errorf("expected sonnet, got %q", got)
	}
	if got := tk.AssignedAgent(types.StageBacklog); got != "" {
		t.Errorf("expected empty for unassigned stage, got %q", got)
	}
}

func TestDelete_NonexistentTask(t *testing.T) {
	store := newTestStore(t)
	if err := store.Delete(t.Context(), "nonexistent-id"); err != nil {
		t.Errorf("Delete nonexistent task should not error, got %v", err)
	}
}

func TestGetFlag_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetFlag(t.Context(), "nonexistent-flag-id")
	if err == nil {
		t.Error("expected error for nonexistent flag ID")
	}
}

func TestUpdateFlag_NotFound(t *testing.T) {
	store := newTestStore(t)
	status := FlagApproved
	_, err := store.UpdateFlag(t.Context(), "nonexistent-id", FlagPatch{Status: &status})
	if err == nil {
		t.Error("expected error updating nonexistent flag")
	}
}

func TestUpdateFlag_WithResolvedNote(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	store.Create(t.Context(), tk)
	f := &Flag{Kind: FlagCustom, Label: "test"}
	store.AddFlag(t.Context(), tk.ID, f)

	note := "resolved with note"
	_, err := store.UpdateFlag(t.Context(), f.ID, FlagPatch{ResolvedNote: &note})
	if err != nil {
		t.Fatalf("UpdateFlag with note: %v", err)
	}
	got, _ := store.GetFlag(t.Context(), f.ID)
	if got.ResolvedNote != note {
		t.Errorf("ResolvedNote = %q, want %q", got.ResolvedNote, note)
	}
}

func TestUpdateFlag_StatusBackToPending(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	store.Create(t.Context(), tk)
	f := &Flag{Kind: FlagCustom, Label: "test"}
	store.AddFlag(t.Context(), tk.ID, f)

	approved := FlagApproved
	store.UpdateFlag(t.Context(), f.ID, FlagPatch{Status: &approved})

	pending := FlagPending
	updated, err := store.UpdateFlag(t.Context(), f.ID, FlagPatch{Status: &pending})
	if err != nil {
		t.Fatalf("UpdateFlag back to pending: %v", err)
	}
	if updated.ResolvedAt != nil {
		t.Error("ResolvedAt should be nil when status is pending")
	}
}

func TestRecordEvent_JSONRawMessage(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	store.Create(t.Context(), tk)

	raw := json.RawMessage(`{"key":"value"}`)
	if err := store.RecordEvent(t.Context(), tk.ID, EventTaskCreated, "", "user", "test", raw); err != nil {
		t.Fatalf("RecordEvent with RawMessage: %v", err)
	}
}

func TestRecordEvent_EmptyStringPayload(t *testing.T) {
	store := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	store.Create(t.Context(), tk)

	if err := store.RecordEvent(t.Context(), tk.ID, EventTaskCreated, "", "user", "test", ""); err != nil {
		t.Fatalf("RecordEvent with empty string payload: %v", err)
	}
}

func TestAddDependency_EmptyIDs(t *testing.T) {
	store := newTestStore(t)
	if err := store.AddDependency(t.Context(), "", "some-id", DepHard); err == nil {
		t.Error("expected error for empty taskID")
	}
	if err := store.AddDependency(t.Context(), "some-id", "", DepHard); err == nil {
		t.Error("expected error for empty dependsOnID")
	}
}

func TestAddDependency_InvalidKind(t *testing.T) {
	store := newTestStore(t)
	tk1 := &Task{WorkspaceID: testWS, Title: "a"}
	store.Create(t.Context(), tk1)
	tk2 := &Task{WorkspaceID: testWS, Title: "b"}
	store.Create(t.Context(), tk2)

	if err := store.AddDependency(t.Context(), tk1.ID, tk2.ID, DepKind("invalid")); err == nil {
		t.Error("expected error for invalid dep kind")
	}
}

func TestForkData_WithAttachments(t *testing.T) {
	store := newTestStore(t)

	src := &Task{WorkspaceID: testWS, Title: "src-with-att"}
	store.Create(t.Context(), src)

	attDir := filepath.Join(t.TempDir(), "att", src.ID)
	if err := os.MkdirAll(attDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	attPath := filepath.Join(attDir, "file.txt")
	if err := os.WriteFile(attPath, []byte("attachment content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store.AddAttachment(t.Context(), NewAttachment{TaskID: src.ID, Filename: "file.txt", MimeType: "text/plain", Path: attPath, Size: 18})

	dst := &Task{WorkspaceID: testWS, Title: "fork-att-dst"}
	store.Create(t.Context(), dst)

	if err := store.ForkData(t.Context(), src.ID, dst.ID); err != nil {
		t.Fatalf("ForkData with attachment: %v", err)
	}

	atts, err := store.ListAttachments(t.Context(), dst.ID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(atts) == 0 {
		t.Error("expected forked attachment in dst task")
	}
}

func TestForkData_RemapsCommentAttachments(t *testing.T) {
	store := newTestStore(t)

	src := &Task{WorkspaceID: testWS, Title: "src-comment-att"}
	store.Create(t.Context(), src)

	attDir := filepath.Join(t.TempDir(), "att", src.ID)
	if err := os.MkdirAll(attDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	attPath := filepath.Join(attDir, "shot.png")
	if err := os.WriteFile(attPath, []byte("PNG"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	c, err := store.AddComment(t.Context(), src.ID, SourceUser, "user", "with a file")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if _, err := store.AddAttachment(t.Context(), NewAttachment{
		TaskID: src.ID, CommentID: c.ID, Filename: "shot.png", Path: attPath, Size: 3,
	}); err != nil {
		t.Fatalf("AddAttachment: %v", err)
	}

	dst := &Task{WorkspaceID: testWS, Title: "fork-comment-att"}
	store.Create(t.Context(), dst)
	if err := store.ForkData(t.Context(), src.ID, dst.ID); err != nil {
		t.Fatalf("ForkData: %v", err)
	}

	atts, err := store.ListAttachments(t.Context(), dst.ID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("want 1 forked attachment, got %d", len(atts))
	}
	comments, err := store.ListComments(t.Context(), dst.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("want 1 forked comment, got %d", len(comments))
	}
	if atts[0].CommentID != comments[0].ID {
		t.Errorf("forked attachment points at comment %q, want the fork's own %q",
			atts[0].CommentID, comments[0].ID)
	}
	if atts[0].CommentID == c.ID {
		t.Error("forked attachment still points at the source task's comment")
	}
}

func TestDepsReachable_SameID(t *testing.T) {
	store := newTestStore(t)
	got, err := store.depsReachable(t.Context(), "x", "x")
	if err != nil {
		t.Fatalf("depsReachable same id: %v", err)
	}
	if !got {
		t.Error("depsReachable(x, x) should return true")
	}
}

func TestStore_DBClosed(t *testing.T) {
	store := newTestStore(t)
	store.DB().Close()

	title := "x"
	cases := map[string]func(*Store) error{
		"depsReachable":           func(s *Store) error { _, err := s.depsReachable(t.Context(), "a", "b"); return err },
		"GetDependencies":         func(s *Store) error { _, err := s.GetDependencies(t.Context(), "t1"); return err },
		"GetDependents":           func(s *Store) error { _, err := s.GetDependents(t.Context(), "t1"); return err },
		"IsBlocked":               func(s *Store) error { _, _, err := s.IsBlocked(t.Context(), "t1"); return err },
		"BlockedSummary":          func(s *Store) error { _, err := s.BlockedSummary(t.Context()); return err },
		"JustUnblockedDependents": func(s *Store) error { _, err := s.JustUnblockedDependents(t.Context(), "t1"); return err },
		"AddDependency":           func(s *Store) error { return s.AddDependency(t.Context(), "t1", "t2", DepHard) },
		"RemoveDependency":        func(s *Store) error { return s.RemoveDependency(t.Context(), "t1", "t2") },
		"ListComments":            func(s *Store) error { _, err := s.ListComments(t.Context(), "t1"); return err },
		"AddComment":              func(s *Store) error { _, err := s.AddComment(t.Context(), "t1", SourceUser, "u", "body"); return err },
		"ListAttachments":         func(s *Store) error { _, err := s.ListAttachments(t.Context(), "t1"); return err },
		"AddAttachment": func(s *Store) error {
			_, err := s.AddAttachment(t.Context(), NewAttachment{TaskID: "t1", Filename: "f.txt", MimeType: "text/plain", Path: "/tmp/x", Size: 1})
			return err
		},
		"ListReports": func(s *Store) error { _, err := s.ListReports(t.Context(), "t1"); return err },
		"AddReport": func(s *Store) error {
			_, err := s.AddReport(t.Context(), "t1", SourceAgent, "a", "summary", "title", "body", "i1")
			return err
		},
		"ListFlags":    func(s *Store) error { _, err := s.ListFlags(t.Context(), "t1"); return err },
		"PendingFlags": func(s *Store) error { _, err := s.PendingFlags(t.Context(), "t1"); return err },
		"ListEvents":   func(s *Store) error { _, err := s.ListEvents(t.Context(), "t1"); return err },
		"RecordEvent": func(s *Store) error {
			return s.RecordEvent(t.Context(), "t1", EventTaskCreated, "", "sys", "summary", nil)
		},
		"List":     func(s *Store) error { _, err := s.List(t.Context()); return err },
		"getRaw":   func(s *Store) error { _, err := s.getRaw(t.Context(), "t1"); return err },
		"Update":   func(s *Store) error { _, err := s.Update(t.Context(), "t1", Patch{Title: &title}); return err },
		"Delete":   func(s *Store) error { return s.Delete(t.Context(), "t1") },
		"ForkData": func(s *Store) error { return s.ForkData(t.Context(), "src", "dst") },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(store); err == nil {
				t.Errorf("%s: expected error with closed DB", name)
			}
		})
	}

	if store.HasFinishEventForInstance(t.Context(), "some-instance") {
		t.Error("HasFinishEventForInstance: expected false with closed DB")
	}
	if store.HasMentionFinishEventForInstance(t.Context(), "some-inst-id") {
		t.Error("HasMentionFinishEventForInstance: expected false with closed DB")
	}
}

func TestCopyFile_SrcNotFound(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst.txt")
	err := copyFile("/nonexistent/src/file.txt", dst)
	if err == nil {
		t.Error("expected error when source file does not exist")
	}
}

func TestCopyFile_DstDirNotFound(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(src, []byte("content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err := copyFile(src, "/nonexistent/dir/dst.txt")
	if err == nil {
		t.Error("expected error when destination directory does not exist")
	}
}

func TestCopyFile_Success(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("hello copy"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dst: %v", err)
	}
	if string(got) != "hello copy" {
		t.Errorf("dst content = %q, want %q", got, "hello copy")
	}
}

func TestGetRaw_NullMaps(t *testing.T) {
	store := newTestStore(t)
	_, err := store.DB().Exec(`INSERT INTO tasks
		(id, title, body, stage, stage_assignments,
		 branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
		 status, tags, priority, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW(),NOW())`,
		"raw-null-maps", "title", "", "backlog", "null",
		"", "null", "null", "", "", "", "null", "")
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	task, err := store.getRaw(t.Context(), "raw-null-maps")
	if err != nil {
		t.Fatalf("getRaw: %v", err)
	}
	if task.StageAssignments == nil {
		t.Error("StageAssignments should be initialized")
	}
	if task.ProjectIDs == nil {
		t.Error("ProjectIDs should be initialized")
	}
	if task.WorktreePaths == nil {
		t.Error("WorktreePaths should be initialized")
	}
	if task.PRURLs == nil {
		t.Error("PRURLs should be initialized")
	}
}

func TestGetRaw_EmptyStatus(t *testing.T) {
	store := newTestStore(t)
	_, err := store.DB().Exec(`INSERT INTO tasks
		(id, title, body, stage, stage_assignments,
		 branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
		 status, tags, priority, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW(),NOW())`,
		"raw-empty-status", "title", "", "backlog", "{}",
		"", "{}", "{}", "", "", "", "[]", "")
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	task, err := store.getRaw(t.Context(), "raw-empty-status")
	if err != nil {
		t.Fatalf("getRaw: %v", err)
	}
	if task.Status != StatusIdle {
		t.Errorf("expected StatusIdle for empty status, got %q", task.Status)
	}
}

func TestList_NullMaps(t *testing.T) {
	store := newTestStore(t)
	_, err := store.DB().Exec(`INSERT INTO tasks
		(id, title, body, stage, stage_assignments,
		 branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
		 status, tags, priority, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW(),NOW())`,
		"list-null-maps", "title", "", "backlog", "null",
		"", "null", "null", "", "", "", "null", "")
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	tasks, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, task := range tasks {
		if task.ID == "list-null-maps" {
			found = true
			if task.StageAssignments == nil {
				t.Error("List: StageAssignments should be initialized")
			}
			if task.PRURLs == nil {
				t.Error("List: PRURLs should be initialized")
			}
		}
	}
	if !found {
		t.Error("expected to find list-null-maps task in List results")
	}
}

func TestCreate_PersistsWorkspaceID(t *testing.T) {
	s := newTestStore(t)

	scoped := &Task{Title: "scoped", WorkspaceID: "ws-alpha"}
	if err := s.Create(t.Context(), scoped); err != nil {
		t.Fatalf("Create scoped: %v", err)
	}
	if err := s.Create(t.Context(), &Task{Title: "unowned"}); err == nil {
		t.Fatal("Create with no WorkspaceID: want error, got nil")
	}

	got, err := s.Get(t.Context(), scoped.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WorkspaceID != "ws-alpha" {
		t.Errorf("Get WorkspaceID = %q, want ws-alpha", got.WorkspaceID)
	}

	list, err := s.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := map[string]string{}
	for i := range list {
		byID[list[i].ID] = list[i].WorkspaceID
	}
	if byID[scoped.ID] != "ws-alpha" {
		t.Errorf("List WorkspaceID for scoped = %q, want ws-alpha", byID[scoped.ID])
	}

	title := "renamed"
	if _, err := s.Update(t.Context(), scoped.ID, Patch{Title: &title}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.Get(t.Context(), scoped.ID)
	if got.WorkspaceID != "ws-alpha" {
		t.Errorf("WorkspaceID after Update = %q, want ws-alpha", got.WorkspaceID)
	}
}
