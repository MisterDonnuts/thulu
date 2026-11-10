package task

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGet_MissingTaskIsNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get(t.Context(), "does-not-exist")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error %v does not match ErrNotFound", err)
	}
}

func TestGet_InfrastructureFailureIsNotAMissingTask(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.DB().Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err := s.GetRow(t.Context(), tk.ID)
	if err == nil {
		t.Fatal("expected an error from a closed pool")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("a closed database reported as a missing task: %v", err)
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("the real cause is not in the error: %v", err)
	}
}

func TestGetAttachment_MissingIsNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetAttachment(t.Context(), "nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error %v does not match ErrNotFound", err)
	}
}

func TestGetAttachment_InfrastructureFailureIsNotAMissingRow(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	att, err := s.AddAttachment(t.Context(), NewAttachment{TaskID: tk.ID, Filename: "f.txt", MimeType: "text/plain", Path: "/tmp/f.txt", Size: 3})
	if err != nil {
		t.Fatalf("AddAttachment: %v", err)
	}
	if err := s.DB().Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err = s.GetAttachment(t.Context(), att.ID); err == nil {
		t.Fatal("expected an error")
	} else if errors.Is(err, ErrNotFound) {
		t.Errorf("a closed database reported as a missing attachment: %v", err)
	}
}

func TestDelete_IsAtomic(t *testing.T) {
	s := newTestStore(t)
	tk := &Task{WorkspaceID: testWS, Title: "t", ProjectIDs: []string{"p"}}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.AddComment(t.Context(), tk.ID, SourceUser, "u", "hi"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if err := s.AddFlag(t.Context(), tk.ID, &Flag{Kind: FlagArchDecision, Label: "x"}); err != nil {
		t.Fatalf("AddFlag: %v", err)
	}
	if err := s.RecordEvent(t.Context(), tk.ID, EventTaskCreated, "", "u", "x", nil); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	if err := s.Delete(t.Context(), tk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	for name, count := range map[string]func() int{
		"comments": func() int { c, _ := s.ListComments(t.Context(), tk.ID); return len(c) },
		"flags":    func() int { f, _ := s.ListFlags(t.Context(), tk.ID); return len(f) },
		"events":   func() int { e, _ := s.ListEvents(t.Context(), tk.ID); return len(e) },
	} {
		if n := count(); n != 0 {
			t.Errorf("%s survived the delete: %d", name, n)
		}
	}
	var links int
	if err := s.DB().QueryRowContext(context.Background(),
		s.rb(`SELECT count(*) FROM task_projects WHERE task_id = ?`), tk.ID).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Errorf("%d project links survived", links)
	}
}

func TestDelete_UnknownIDLeavesOthersAlone(t *testing.T) {
	s := newTestStore(t)
	keep := &Task{WorkspaceID: testWS, Title: "keep", ProjectIDs: []string{"p"}}
	if err := s.Create(t.Context(), keep); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.AddComment(t.Context(), keep.ID, SourceUser, "u", "hi"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}

	if err := s.Delete(t.Context(), "0000000000000000"); err != nil {
		t.Fatalf("Delete of an unknown id: %v", err)
	}
	if got, err := s.Get(t.Context(), keep.ID); err != nil {
		t.Fatalf("the surviving task is gone: %v", err)
	} else if len(got.Comments) != 1 {
		t.Errorf("comments = %d, want 1", len(got.Comments))
	}
}

func TestGenerateID_IsUniqueAndFullLength(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		id, err := generateID()
		if err != nil {
			t.Fatalf("generateID: %v", err)
		}
		if len(id) != 16 {
			t.Fatalf("id %q is %d chars, want 16", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
