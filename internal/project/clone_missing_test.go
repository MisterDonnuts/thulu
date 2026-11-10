package project

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func TestProjectWithoutCloneSaysSo(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	seedInitProject(t, s, "gone", testWS)

	_, _, err := s.RootFiles(ctx, "gone")
	if apperr.Code(err) != "project.errors.clone_missing" {
		t.Fatalf("RootFiles err = %v, want clone_missing", err)
	}
	if got := apperr.KindOf(err); got != apperr.KindPrecondition {
		t.Errorf("kind = %v, want a precondition", got)
	}
	if _, err := s.CreateTaskWorktreeAuth(ctx, "gone", "abc123", ""); apperr.Code(err) != "project.errors.clone_missing" {
		t.Errorf("CreateTaskWorktreeAuth err = %v, want clone_missing", err)
	}

	if _, err := s.ListWorktrees(ctx, "gone"); apperr.Code(err) != "project.errors.clone_missing" {
		t.Errorf("ListWorktrees err = %v, want clone_missing", err)
	}
}
