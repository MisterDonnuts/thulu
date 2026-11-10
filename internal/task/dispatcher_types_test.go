package task

import (
	"context"
	"path/filepath"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func typedBoards() *fakeBoards {
	return &fakeBoards{
		def: "slow",
		regs: map[string]stagepkg.Registry{
			"slow": stagepkg.NewSnapshot([]types.StageInfo{
				{ID: "intake", Label: "Intake", Position: 10, Initial: true},
				{ID: "work", Label: "Work", Position: 20, AutoAdvance: true},
				{ID: "review", Label: "Review", Position: 30},
				{ID: "out", Label: "Out", Position: 40, Terminal: true},
			}),
		},
		types: map[string][]stagepkg.TaskTypeInfo{
			"slow": {
				{ID: "quick", Label: "Quick", Icon: "bolt", Skips: []types.Stage{"review"}},
				{ID: "full", Label: "Full", Default: true},
			},
		},
	}
}

func newTypesTestDispatcher(t *testing.T) (*Dispatcher, *Store) {
	t.Helper()
	store, err := NewStore(newTaskDB(t), filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	boards := typedBoards()
	store.WithBoards(boards)
	return NewDispatcher(store, nil, nil, nil, nil).WithBoards(boards), store
}

func mustCreateTyped(t *testing.T, store *Store, board, typeID string, stage Stage) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "t", BoardID: board, TypeID: typeID, Stage: stage}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create %s/%s: %v", board, typeID, err)
	}
	return tk
}

func TestAutoAdvanceSkipsTheTypesSkippedStage(t *testing.T) {
	d, store := newTypesTestDispatcher(t)
	quick := mustCreateTyped(t, store, "slow", "quick", "work")
	full := mustCreateTyped(t, store, "slow", "full", "work")

	d.autoAdvanceStage(context.Background(), quick, quick.ID, "work", 0, "")
	d.autoAdvanceStage(context.Background(), full, full.ID, "work", 0, "")

	afterQuick, _ := store.Get(t.Context(), quick.ID)
	afterFull, _ := store.Get(t.Context(), full.ID)
	if afterQuick.Stage != "out" {
		t.Errorf("quick: stage = %q, want out — review is not part of its flow", afterQuick.Stage)
	}
	if afterFull.Stage != "review" {
		t.Errorf("full: stage = %q, want review — an untyped-equivalent task walks the whole board", afterFull.Stage)
	}
}

func TestTypeRoundTrips(t *testing.T) {
	_, store := newTypesTestDispatcher(t)
	tk := mustCreateTyped(t, store, "slow", "quick", "")
	if tk.Stage != "intake" {
		t.Errorf("stage = %q, want the board's initial — a type never moves the entry point", tk.Stage)
	}
	got, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TypeID != "quick" {
		t.Errorf("type_id = %q after round-trip, want quick", got.TypeID)
	}
}

func TestUnknownTypeFallsBackToTheWholeBoard(t *testing.T) {
	d, store := newTypesTestDispatcher(t)
	orphan := mustCreateTyped(t, store, "slow", "deleted_type", "work")

	d.autoAdvanceStage(context.Background(), orphan, orphan.ID, "work", 0, "")

	after, _ := store.Get(t.Context(), orphan.ID)
	if after.Stage != "review" {
		t.Errorf("stage = %q, want review: an unknown type means the unfiltered board", after.Stage)
	}
}

func TestTerminalityIgnoresTheType(t *testing.T) {
	_, store := newTypesTestDispatcher(t)
	blocker := mustCreateTyped(t, store, "slow", "quick", "out")
	dependent := mustCreateTyped(t, store, "slow", "full", "intake")
	if err := store.AddDependency(t.Context(), dependent.ID, blocker.ID, DepHard); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	blocked, blockers, err := store.IsBlocked(t.Context(), dependent.ID)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if blocked {
		t.Errorf("dependent is blocked by a task in a terminal stage: %+v", blockers)
	}
}

func TestListByBoardType(t *testing.T) {
	_, store := newTypesTestDispatcher(t)
	mustCreateTyped(t, store, "slow", "quick", "work")
	mustCreateTyped(t, store, "slow", "full", "work")
	mustCreateTyped(t, store, "slow", "", "work")

	for _, tc := range []struct{ typeID string }{{"quick"}, {"full"}, {""}} {
		got, err := store.ListByBoardType(t.Context(), "slow", tc.typeID)
		if err != nil {
			t.Fatalf("ListByBoardType(%q): %v", tc.typeID, err)
		}
		if len(got) != 1 {
			t.Errorf("ListByBoardType(%q) = %d tasks, want 1", tc.typeID, len(got))
		}
	}
	inStage, err := store.ListInBoardTypeStage(t.Context(), "slow", "quick", "work")
	if err != nil {
		t.Fatalf("ListInBoardTypeStage: %v", err)
	}
	if len(inStage) != 1 {
		t.Errorf("ListInBoardTypeStage = %d tasks, want 1", len(inStage))
	}
}
