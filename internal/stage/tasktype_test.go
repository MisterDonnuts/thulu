package stage

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func bugfixType(skips ...types.Stage) TaskTypeInfo {
	return TaskTypeInfo{ID: "bugfix", Label: "Bugfix", Icon: "bug", Color: "#e5484d", Skips: skips}
}

func saveTypes(t *testing.T, store *Store, boardID string, tt ...TaskTypeInfo) {
	t.Helper()
	if err := store.SaveBoardTypes(context.Background(), boardID, tt); err != nil {
		t.Fatalf("SaveBoardTypes(%s): %v", boardID, err)
	}
}

func TestBoardForSkipsStage(t *testing.T) {
	store := newFlowStore(t)
	saveTypes(t, store, DefaultBoardID, bugfixType(types.StageRefinement, types.StageReview))

	reg := store.BoardFor(DefaultBoardID, "bugfix")
	if got := reg.Next(types.StageBacklog); got != types.StageInProgress {
		t.Errorf("Next(backlog) = %q, want %q — the type must skip refinement and review", got, types.StageInProgress)
	}
	if reg.Valid(types.StageRefinement) {
		t.Error("Valid(refinement) = true, want false: a skipped stage is not a stage of this flow")
	}
	if got := reg.Initial(); got != types.StageBacklog {
		t.Errorf("Initial() = %q, want %q — skipping never moves the entry point", got, types.StageBacklog)
	}
	if got := reg.KickbackDefault(); got != types.StageInProgress {
		t.Errorf("KickbackDefault() = %q, want %q — kickback falls to the first agent stage the type has",
			got, types.StageInProgress)
	}
	if got, want := reg.Terminals(), store.Board(DefaultBoardID).Terminals(); len(got) != len(want) {
		t.Errorf("Terminals() = %v, want %v (unchanged by type)", got, want)
	}
	if n, want := len(reg.List()), len(store.Board(DefaultBoardID).List())-2; n != want {
		t.Errorf("filtered board has %d stages, want %d", n, want)
	}
}

func TestBoardForUntypedAndUnknownFallBackToBoard(t *testing.T) {
	store := newFlowStore(t)
	saveTypes(t, store, DefaultBoardID, bugfixType(types.StageRefinement))

	full := len(store.Board(DefaultBoardID).List())
	for _, typeID := range []string{"", "nope"} {
		if got := len(store.BoardFor(DefaultBoardID, typeID).List()); got != full {
			t.Errorf("BoardFor(default, %q) has %d stages, want the full board (%d)", typeID, got, full)
		}
	}
}

func TestTaskTypeIdentityIsPerBoard(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	saveTypes(t, store, DefaultBoardID, bugfixType())
	if err := store.CreateBoard(ctx, BoardInfo{ID: "other", Label: "Other", WorkspaceID: "ws-b"},
		twoStageBoard("o")); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	saveTypes(t, store, "other", TaskTypeInfo{ID: "bugfix", Label: "Hijacked", Icon: "star", Color: "#000000"})

	for _, s := range []*Store{store, mustReopen(t, store)} {
		mine := s.BoardTypes(DefaultBoardID)
		if len(mine) != 1 || mine[0].Label != "Bugfix" || mine[0].Icon != "bug" || mine[0].Color != "#e5484d" {
			t.Fatalf("default board types = %+v, want its own Bugfix untouched", mine)
		}
		if mine[0].WorkspaceID != FallbackWorkspaceID {
			t.Errorf("default board's type owner = %q, want the board's workspace %q", mine[0].WorkspaceID, FallbackWorkspaceID)
		}
		theirs := s.BoardTypes("other")
		if len(theirs) != 1 || theirs[0].Label != "Hijacked" || theirs[0].WorkspaceID != "ws-b" {
			t.Errorf("other board types = %+v, want its own label under ws-b", theirs)
		}
	}
}

func mustReopen(t *testing.T, store *Store) *Store {
	t.Helper()
	s, err := NewStore(store.DB())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return s
}

func TestNoTypesLeavesBoardUntouched(t *testing.T) {
	store := newTestStore(t)
	if tt := store.BoardTypes(DefaultBoardID); len(tt) != 0 {
		t.Errorf("fresh board offers %d types, want 0: %+v", len(tt), tt)
	}
	if got, want := len(store.BoardFor(DefaultBoardID, "").List()), len(Defaults()); got != want {
		t.Errorf("untyped flow has %d stages, want %d", got, want)
	}
}

func TestSaveBoardTypesRoundTrip(t *testing.T) {
	store := newFlowStore(t)
	saveTypes(t, store, DefaultBoardID,
		bugfixType(types.StageRefinement),
		TaskTypeInfo{ID: "feature", Label: "Feature", Icon: "feature", Default: true},
	)

	tt := store.BoardTypes(DefaultBoardID)
	if len(tt) != 2 {
		t.Fatalf("BoardTypes = %d, want 2: %+v", len(tt), tt)
	}
	if tt[0].ID != "bugfix" || tt[1].ID != "feature" {
		t.Errorf("order = %q,%q, want bugfix,feature (the order they were written)", tt[0].ID, tt[1].ID)
	}
	if tt[0].Icon != "bug" || tt[0].Color != "#e5484d" {
		t.Errorf("identity lost: %+v", tt[0])
	}
	if len(tt[0].Skips) != 1 || tt[0].Skips[0] != types.StageRefinement {
		t.Errorf("skips = %v, want [refinement]", tt[0].Skips)
	}
	if DefaultTaskType(tt) != "feature" {
		t.Errorf("DefaultTaskType = %q, want feature", DefaultTaskType(tt))
	}
	if !store.HasBoardType(DefaultBoardID, "bugfix") || store.HasBoardType(DefaultBoardID, "ghost") {
		t.Error("HasBoardType disagrees with the saved membership")
	}
	if !store.HasBoardType(DefaultBoardID, "") {
		t.Error(`HasBoardType(board, "") = false: untyped is offered by every board`)
	}
	if n := len(store.TypeCatalog()); n != 2 {
		t.Errorf("TypeCatalog = %d types, want 2", n)
	}
}

func TestTypeLeavingItsLastBoardLeavesTheCatalog(t *testing.T) {
	store := newFlowStore(t)
	saveTypes(t, store, DefaultBoardID, bugfixType(), TaskTypeInfo{ID: "chore", Label: "Chore"})
	saveTypes(t, store, DefaultBoardID, bugfixType())

	if n := len(store.TypeCatalog()); n != 1 {
		t.Errorf("TypeCatalog = %d, want 1 after dropping chore: %+v", n, store.TypeCatalog())
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM task_types WHERE id = 'chore'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("orphaned catalog row survived: %d", count)
	}
}

func TestRemovingStageFromBoardDropsItsSkips(t *testing.T) {
	store := newFlowStore(t)
	saveTypes(t, store, DefaultBoardID, bugfixType(types.StageRefinement))

	board := store.Board(DefaultBoardID).List()
	shorter := make([]types.StageInfo, 0, len(board))
	for _, st := range board {
		if st.ID != types.StageRefinement {
			shorter = append(shorter, st)
		}
	}
	if err := store.SaveBoard(context.Background(), DefaultBoardID, shorter); err != nil {
		t.Fatalf("SaveBoard: %v", err)
	}
	if skips := store.BoardTypes(DefaultBoardID)[0].Skips; len(skips) != 0 {
		t.Errorf("skips = %v, want none after the stage left the board", skips)
	}
	saveTypes(t, store, DefaultBoardID, bugfixType())
}

func TestDeleteBoardDropsItsTypes(t *testing.T) {
	store := newFlowStore(t)
	ctx := context.Background()
	if err := store.CreateBoard(ctx, BoardInfo{ID: "hotfix", Label: "Hotfix", WorkspaceID: FallbackWorkspaceID}, twoStageBoard("h")); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	saveTypes(t, store, "hotfix", bugfixType())
	if err := store.DeleteBoard(ctx, "hotfix"); err != nil {
		t.Fatalf("DeleteBoard: %v", err)
	}
	if tt := store.BoardTypes("hotfix"); len(tt) != 0 {
		t.Errorf("deleted board still offers types: %+v", tt)
	}
	if n := len(store.TypeCatalog()); n != 0 {
		t.Errorf("TypeCatalog = %d, want 0 — the type's only board is gone", n)
	}
}

func TestTypesSurviveReopen(t *testing.T) {
	store := newFlowStore(t)
	saveTypes(t, store, DefaultBoardID, bugfixType(types.StageRefinement, types.StageReview))

	reopened, err := NewStore(store.DB())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.BoardFor(DefaultBoardID, "bugfix").Next(types.StageBacklog); got != types.StageInProgress {
		t.Errorf("after reopen Next(backlog) = %q, want %q", got, types.StageInProgress)
	}
}

func TestValidateBoardTypes(t *testing.T) {
	board := stagetest.Flow()
	tests := []struct {
		name    string
		types   []TaskTypeInfo
		wantErr bool
	}{
		{"empty is fine", nil, false},
		{"skip a middle stage", []TaskTypeInfo{bugfixType(types.StageRefinement)}, false},
		{"skip several", []TaskTypeInfo{bugfixType(types.StageRefinement, types.StageReview)}, false},
		{"skip the initial stage", []TaskTypeInfo{bugfixType(types.StageBacklog)}, true},
		{"skip a terminal stage", []TaskTypeInfo{bugfixType(types.StageDone)}, true},
		{"skip a stage of another board", []TaskTypeInfo{bugfixType("nowhere")}, true},
		{"skip twice", []TaskTypeInfo{bugfixType(types.StageReview, types.StageReview)}, true},
		{"duplicate id", []TaskTypeInfo{bugfixType(), bugfixType()}, true},
		{"two defaults", []TaskTypeInfo{
			{ID: "a", Label: "A", Default: true}, {ID: "b", Label: "B", Default: true},
		}, true},
		{"bad id", []TaskTypeInfo{{ID: "Bug-Fix", Label: "x"}}, true},
		{"no label", []TaskTypeInfo{{ID: "bugfix"}}, true},
		{"bad colour", []TaskTypeInfo{{ID: "bugfix", Label: "B", Color: "red"}}, true},
		{"icon off the allowlist", []TaskTypeInfo{{ID: "bugfix", Label: "B", Icon: "skull"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBoardTypes(board, tc.types)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateBoardTypes = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateBoardTypesRejectsTooMany(t *testing.T) {
	board := Static().List()
	tt := make([]TaskTypeInfo, MaxBoardTypes+1)
	for i := range tt {
		tt[i] = TaskTypeInfo{ID: "t" + string(rune('a'+i)), Label: "T"}
	}
	if err := ValidateBoardTypes(board, tt); err == nil {
		t.Errorf("ValidateBoardTypes accepted %d types, want a cap at %d", len(tt), MaxBoardTypes)
	}
}

func TestSaveBoardTypesRejectsInvalid(t *testing.T) {
	store := newFlowStore(t)
	err := store.SaveBoardTypes(context.Background(), DefaultBoardID,
		[]TaskTypeInfo{bugfixType(types.StageDone)})
	if err == nil {
		t.Fatal("SaveBoardTypes accepted a type skipping a terminal stage")
	}
	if tt := store.BoardTypes(DefaultBoardID); len(tt) != 0 {
		t.Errorf("rejected write left state behind: %+v", tt)
	}
}

func TestSaveBoardTypesUnknownBoard(t *testing.T) {
	store := newFlowStore(t)
	if err := store.SaveBoardTypes(context.Background(), "ghost", []TaskTypeInfo{bugfixType()}); err == nil {
		t.Error("SaveBoardTypes accepted an unknown board")
	}
}

func TestBoardTypesReturnsCopies(t *testing.T) {
	store := newFlowStore(t)
	saveTypes(t, store, DefaultBoardID, bugfixType(types.StageRefinement))
	got := store.BoardTypes(DefaultBoardID)
	got[0].Skips[0] = "hacked"
	if s := store.BoardTypes(DefaultBoardID)[0].Skips[0]; s != types.StageRefinement {
		t.Errorf("live state mutated through a returned slice: %q", s)
	}
}
