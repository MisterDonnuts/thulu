package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestList_Empty(t *testing.T) {
	s := newTestStore(t)
	tasks, err := s.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("want 0 tasks, got %d", len(tasks))
	}
}

func TestList_MultipleOrderedByUpdatedAt(t *testing.T) {
	s := newTestStore(t)
	a := &Task{WorkspaceID: testWS, Title: "alpha"}
	b := &Task{WorkspaceID: testWS, Title: "beta"}
	if err := s.Create(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	st := types.StageRefinement
	if _, err := s.Update(t.Context(), b.ID, Patch{Stage: &st}); err != nil {
		t.Fatal(err)
	}

	tasks, err := s.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("want 2, got %d", len(tasks))
	}
	if tasks[0].Title != "beta" {
		t.Errorf("first = %q, want beta (more recently updated)", tasks[0].Title)
	}
}

func TestUpdate_AllPatchFields(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "orig", Body: "body", Priority: "low"}
	s.Create(t.Context(), tk)

	title := "updated"
	body := "new body"
	status := StatusRunning
	run := "run-1"
	inst := "inst-1"
	branch := "feature/x"
	tags := []string{"a", "b"}
	priority := "high"
	stage := types.StageRefinement

	got, err := s.Update(t.Context(), tk.ID, Patch{
		Title:           &title,
		Body:            &body,
		Stage:           &stage,
		Status:          &status,
		CurrentRunID:    &run,
		CurrentInstance: &inst,
		BranchName:      &branch,
		Tags:            &tags,
		Priority:        &priority,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Title != title {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Body != body {
		t.Errorf("Body = %q", got.Body)
	}
	if got.Stage != types.StageRefinement {
		t.Errorf("Stage = %q", got.Stage)
	}
	if got.Status != StatusRunning {
		t.Errorf("Status = %q", got.Status)
	}
	if got.CurrentRunID != run {
		t.Errorf("CurrentRunID = %q", got.CurrentRunID)
	}
	if got.CurrentInstance != inst {
		t.Errorf("CurrentInstance = %q", got.CurrentInstance)
	}
	if got.BranchName != branch {
		t.Errorf("BranchName = %q", got.BranchName)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "a" {
		t.Errorf("Tags = %v", got.Tags)
	}
	if got.Priority != priority {
		t.Errorf("Priority = %q", got.Priority)
	}
}

func TestUpdate_WorktreeAndPRPaths(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	wt := map[string]string{"proj-1": "/tmp/wt"}
	pr := map[string]string{"proj-1": "https://github.com/pr/1"}
	got, err := s.Update(t.Context(), tk.ID, Patch{WorktreePaths: wt, PRURLs: pr})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.WorktreePaths["proj-1"] != "/tmp/wt" {
		t.Errorf("WorktreePaths = %v", got.WorktreePaths)
	}
	if got.PRURLs["proj-1"] != "https://github.com/pr/1" {
		t.Errorf("PRURLs = %v", got.PRURLs)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Update(t.Context(), "nonexistent", Patch{})
	if err == nil {
		t.Error("expected error for missing task")
	}
}

func TestComments_CRUD(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	c, err := s.AddComment(t.Context(), tk.ID, SourceUser, "alice", "hello")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if c.ID == "" {
		t.Error("comment ID empty")
	}
	if c.Stage != types.StageBacklog {
		t.Errorf("stage snapshot = %q, want backlog", c.Stage)
	}

	cs, err := s.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(cs) != 1 {
		t.Fatalf("want 1 comment, got %d", len(cs))
	}
	if cs[0].Body != "hello" || cs[0].Author != "alice" {
		t.Errorf("comment mismatch: %+v", cs[0])
	}
}

func TestComments_Empty(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	cs, err := s.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(cs) != 0 {
		t.Errorf("want 0, got %d", len(cs))
	}
}

func TestAttachments_AddAndList(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	a, err := s.AddAttachment(t.Context(), NewAttachment{TaskID: tk.ID, Filename: "report.pdf", MimeType: "application/pdf", Path: "/some/path.pdf", Size: 12345})
	if err != nil {
		t.Fatalf("AddAttachment: %v", err)
	}
	if a.ID == "" {
		t.Error("attachment ID empty")
	}

	list, err := s.ListAttachments(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1, got %d", len(list))
	}
	if list[0].Filename != "report.pdf" || list[0].Size != 12345 {
		t.Errorf("attachment mismatch: %+v", list[0])
	}

	got, err := s.GetAttachment(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if got.MimeType != "application/pdf" {
		t.Errorf("MimeType = %q", got.MimeType)
	}
}

func TestAttachments_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetAttachment(t.Context(), "nope")
	if err == nil {
		t.Error("expected error for missing attachment")
	}
}

func TestReports_AddAndList(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	r, err := s.AddReport(t.Context(), tk.ID, SourceAuditor, "auditor", "verdict", "Pass", "All checks ok", "run-42")
	if err != nil {
		t.Fatalf("AddReport: %v", err)
	}
	if r.ID == "" {
		t.Error("report ID empty")
	}

	reports, err := s.ListReports(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("want 1, got %d", len(reports))
	}
	if reports[0].Title != "Pass" {
		t.Errorf("Title = %q", reports[0].Title)
	}
}

func TestEvents_RecordAndList(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	err := s.RecordEvent(t.Context(), tk.ID, EventTaskCreated, "", "system", "created", nil)
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	err = s.RecordEvent(t.Context(), tk.ID, EventAgentRunStarted, "inst-1", "octopus", "launched", map[string]string{"stage": "refinement"})
	if err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	evs, err := s.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
}

func TestHasMentionFinishEventForInstance(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	if s.HasMentionFinishEventForInstance(t.Context(), "inst-1") {
		t.Error("should be false when no events exist")
	}

	s.RecordEvent(t.Context(), tk.ID, EventMentionFinished, "inst-1", "octopus", "done", nil)

	if !s.HasMentionFinishEventForInstance(t.Context(), "inst-1") {
		t.Error("should be true after recording mention finish event")
	}
	if s.HasMentionFinishEventForInstance(t.Context(), "inst-2") {
		t.Error("should not match different instance")
	}
}

func TestForkData(t *testing.T) {
	s := newTestStore(t)
	src := &Task{WorkspaceID: testWS, Title: "src", Body: "body", Tags: []string{"x"}}
	s.Create(t.Context(), src)
	s.AddComment(t.Context(), src.ID, SourceUser, "alice", "comment 1")
	s.AddFlag(t.Context(), src.ID, &Flag{Kind: FlagInfraImpact, Label: "infra"})
	s.RecordEvent(t.Context(), src.ID, EventTaskCreated, "", "system", "created", nil)

	dst := &Task{WorkspaceID: testWS, Title: "fork"}
	s.Create(t.Context(), dst)

	if err := s.ForkData(t.Context(), src.ID, dst.ID); err != nil {
		t.Fatalf("ForkData: %v", err)
	}

	cs, _ := s.ListComments(t.Context(), dst.ID)
	if len(cs) != 1 {
		t.Errorf("fork: want 1 comment, got %d", len(cs))
	}
	fs, _ := s.ListFlags(t.Context(), dst.ID)
	if len(fs) != 1 {
		t.Errorf("fork: want 1 flag, got %d", len(fs))
	}
	evs, _ := s.ListEvents(t.Context(), dst.ID)
	if len(evs) != 1 {
		t.Errorf("fork: want 1 event, got %d", len(evs))
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"report.pdf", "report.pdf"},
		{"my file.txt", "my_file.txt"},
		{"../../../etc/passwd", ".._.._.._etc_passwd"},
		{"", "file"},
		{"   ", "file"},
		{"a-b_c.d", "a-b_c.d"},
		{strings.Repeat("x", 200), strings.Repeat("x", 120)},
	}
	for _, c := range cases {
		got := SanitizeFilename(c.in)
		if got != c.want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCountRunsForStage(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	if n := s.CountRunsForStage(t.Context(), tk.ID, types.StageRefinement); n != 0 {
		t.Errorf("want 0, got %d", n)
	}

	payload := map[string]string{"stage": string(types.StageRefinement), "agent": "probe"}
	s.RecordEvent(t.Context(), tk.ID, EventAgentRunStarted, "inst-1", "probe", "summary", payload)
	s.RecordEvent(t.Context(), tk.ID, EventAgentRunStarted, "inst-2", "probe", "summary", payload)

	if n := s.CountRunsForStage(t.Context(), tk.ID, types.StageRefinement); n != 2 {
		t.Errorf("want 2, got %d", n)
	}
	if n := s.CountRunsForStage(t.Context(), tk.ID, types.StageInProgress); n != 0 {
		t.Errorf("want 0 for in_progress, got %d", n)
	}
}

func TestDeleteFlag(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)
	f := &Flag{Kind: FlagInfraImpact, Label: "infra"}
	s.AddFlag(t.Context(), tk.ID, f)

	if err := s.DeleteFlag(t.Context(), f.ID); err != nil {
		t.Fatalf("DeleteFlag: %v", err)
	}
	flags, _ := s.ListFlags(t.Context(), tk.ID)
	if len(flags) != 0 {
		t.Errorf("after delete, flags should be empty, got %d", len(flags))
	}
}

func TestGet_IncludesSubCollections(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)
	s.AddComment(t.Context(), tk.ID, SourceAgent, "probe", "progress")
	s.AddFlag(t.Context(), tk.ID, &Flag{Kind: FlagUXDesign, Label: "review needed"})

	got, err := s.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Comments) != 1 {
		t.Errorf("Get: want 1 comment, got %d", len(got.Comments))
	}
	if len(got.Flags) != 1 {
		t.Errorf("Get: want 1 flag, got %d", len(got.Flags))
	}
}

func TestStore_DBAndAttachmentsDir(t *testing.T) {
	s := newTestStore(t)
	if s.DB() == nil {
		t.Error("DB() should not be nil")
	}
	if s.AttachmentsDir() == "" {
		t.Error("AttachmentsDir() should not be empty")
	}
}

func TestRecordEvent_PayloadVariants(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "payload test"}
	s.Create(t.Context(), tk)

	if err := s.RecordEvent(t.Context(), tk.ID, EventTaskCreated, "", "", "nil payload", nil); err != nil {
		t.Fatalf("nil payload: %v", err)
	}
	if err := s.RecordEvent(t.Context(), tk.ID, EventStageEntered, "", "", "str", `{"stage":"refinement"}`); err != nil {
		t.Fatalf("string payload: %v", err)
	}
	if err := s.RecordEvent(t.Context(), tk.ID, EventBranchCreated, "", "", "bytes", []byte(`{"branch":"main"}`)); err != nil {
		t.Fatalf("bytes payload: %v", err)
	}
	if err := s.RecordEvent(t.Context(), tk.ID, EventPROpened, "", "", "struct", map[string]string{"pr": "1"}); err != nil {
		t.Fatalf("struct payload: %v", err)
	}

	events, err := s.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 4 {
		t.Errorf("expected 4 events, got %d", len(events))
	}
}

func TestHasMentionFinishEventForInstance_Empty(t *testing.T) {
	s := newTestStore(t)
	if s.HasMentionFinishEventForInstance(t.Context(), "") {
		t.Error("empty instanceID should return false")
	}
	if s.HasMentionFinishEventForInstance(t.Context(), "nonexistent") {
		t.Error("nonexistent instance should return false")
	}
}

const testWS = "ws-test"

func TestBindCommentAttachments(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)
	other := &Task{WorkspaceID: testWS, Title: "other"}
	s.Create(t.Context(), other)

	c, err := s.AddComment(t.Context(), tk.ID, SourceUser, "user", "look at this")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	mine, _ := s.AddAttachment(t.Context(), NewAttachment{TaskID: tk.ID, Filename: "a.png", Path: "/tmp/a.png"})
	foreign, _ := s.AddAttachment(t.Context(), NewAttachment{TaskID: other.ID, Filename: "b.png", Path: "/tmp/b.png"})

	if err := s.BindCommentAttachments(t.Context(), tk.ID, c.ID, []string{mine.ID, foreign.ID}); err != nil {
		t.Fatalf("BindCommentAttachments: %v", err)
	}

	got, err := s.GetAttachment(t.Context(), mine.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if got.CommentID != c.ID {
		t.Errorf("comment_id = %q, want %q", got.CommentID, c.ID)
	}
	stillFree, err := s.GetAttachment(t.Context(), foreign.ID)
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	if stillFree.CommentID != "" {
		t.Errorf("another task's attachment was bound: %q", stillFree.CommentID)
	}

	c2, _ := s.AddComment(t.Context(), tk.ID, SourceUser, "user", "mine now")
	if err := s.BindCommentAttachments(t.Context(), tk.ID, c2.ID, []string{mine.ID}); err != nil {
		t.Fatalf("BindCommentAttachments (rebind): %v", err)
	}
	after, _ := s.GetAttachment(t.Context(), mine.ID)
	if after.CommentID != c.ID {
		t.Errorf("comment_id = %q, want it to stay %q", after.CommentID, c.ID)
	}
}

func TestDeleteAttachment_RemovesRowAndFile(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	s.Create(t.Context(), tk)

	path := filepath.Join(t.TempDir(), "doomed.txt")
	if err := os.WriteFile(path, []byte("bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := s.AddAttachment(t.Context(), NewAttachment{TaskID: tk.ID, Filename: "doomed.txt", Path: path, Size: 5})
	if err != nil {
		t.Fatalf("AddAttachment: %v", err)
	}
	if err := s.DeleteAttachment(t.Context(), a.ID); err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	if _, err := s.GetAttachment(t.Context(), a.ID); err == nil {
		t.Error("row survived the delete")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file survived the delete")
	}
	b, _ := s.AddAttachment(t.Context(), NewAttachment{TaskID: tk.ID, Filename: "ghost.txt", Path: filepath.Join(t.TempDir(), "nope.txt")})
	if err := s.DeleteAttachment(t.Context(), b.ID); err != nil {
		t.Errorf("DeleteAttachment with missing file: %v", err)
	}
}

func TestDelete_RemovesAttachmentDir(t *testing.T) {
	s := newTestStore(t)
	doomed := &Task{WorkspaceID: testWS, Title: "doomed"}
	kept := &Task{WorkspaceID: testWS, Title: "kept"}
	s.Create(t.Context(), doomed)
	s.Create(t.Context(), kept)

	for _, id := range []string{doomed.ID, kept.ID} {
		dir := filepath.Join(s.AttachmentsDir(), id)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "1-shot.png"), []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(t.Context(), doomed.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.AttachmentsDir(), doomed.ID)); !os.IsNotExist(err) {
		t.Errorf("deleted task's attachment dir survived (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(s.AttachmentsDir(), kept.ID, "1-shot.png")); err != nil {
		t.Errorf("sibling task's attachment must survive: %v", err)
	}
}
