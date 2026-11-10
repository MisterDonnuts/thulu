package stage

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestApprovalSettingsRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	board := []types.StageInfo{
		{ID: "ap_open", Label: "Open", Initial: true},
		{ID: "ap_build", Label: "Build"},
		{ID: "ap_review", Label: "Review", AutoAdvance: true, BlockedByFlags: true,
			RequiredApprovals: []types.ApprovalRequirement{
				{Key: "qa", Label: "QA", ResetOnChange: true},
				{Key: "legal", Label: "Legal"},
			},
			OnChangesRequested: types.ChangesKickBack, ChangesKickBackTo: "ap_build"},
		{ID: "ap_done", Label: "Done", Terminal: true},
	}
	if err := store.CreateBoard(ctx, BoardInfo{ID: "approvals", Label: "Approvals", WorkspaceID: FallbackWorkspaceID}, board); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	reopened, err := NewStore(store.DB())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, _ := reopened.Board("approvals").Get("ap_review")
	if !reflect.DeepEqual(got.RequiredApprovals, board[2].RequiredApprovals) {
		t.Errorf("required_approvals = %+v, want %+v", got.RequiredApprovals, board[2].RequiredApprovals)
	}
	if got.OnChangesRequested != types.ChangesKickBack || got.ChangesKickBackTo != "ap_build" {
		t.Errorf("changes settings = %q/%q", got.OnChangesRequested, got.ChangesKickBackTo)
	}
	if plain, _ := reopened.Board("approvals").Get("ap_build"); plain.RequiredApprovals != nil {
		t.Errorf("a stage without approvals must read back nil, got %#v", plain.RequiredApprovals)
	}
}

func TestApprovalTypesCheckedOnSave(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	board := twoStageBoard("ty")
	board[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "design", Label: "Design", TaskTypes: []string{"feature"}}}
	err := store.CreateBoard(ctx, BoardInfo{ID: "typed", Label: "Typed", WorkspaceID: FallbackWorkspaceID}, board)
	if err == nil || !strings.Contains(err.Error(), "does not offer") {
		t.Fatalf("CreateBoard naming an unknown type: err = %v", err)
	}
	board[0].RequiredApprovals = nil
	if err := store.CreateBoard(ctx, BoardInfo{ID: "typed", Label: "Typed", WorkspaceID: FallbackWorkspaceID}, board); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := store.SaveBoardTypes(ctx, "typed", []TaskTypeInfo{{ID: "feature", Label: "Feature"}}); err != nil {
		t.Fatalf("SaveBoardTypes: %v", err)
	}
	board[0].RequiredApprovals = []types.ApprovalRequirement{{Key: "design", Label: "Design", TaskTypes: []string{"feature"}}}
	if err := store.SaveBoard(ctx, "typed", board); err != nil {
		t.Fatalf("SaveBoard with an offered type: %v", err)
	}
	if err := store.SaveBoardTypes(ctx, "typed", nil); err == nil {
		t.Fatal("dropping a type a requirement names must fail")
	}
}
