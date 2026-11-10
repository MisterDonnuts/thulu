package stage

import (
	"context"
	"reflect"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/stage/stagetest"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func newFlowStore(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	if err := store.Save(context.Background(), stagetest.Flow()); err != nil {
		t.Fatalf("Save(stagetest.Flow): %v", err)
	}
	return store
}

func TestSeedMatchesStatic(t *testing.T) {
	store := newTestStore(t)
	if got, want := store.DefaultBoard().List(), Static().List(); !reflect.DeepEqual(got, want) {
		t.Errorf("seeded board != Static():\n got: %+v\nwant: %+v", got, want)
	}
	if !reflect.DeepEqual(store.DefaultBoard().ListWithVirtual(), Static().ListWithVirtual()) {
		t.Error("seeded ListWithVirtual != Static()")
	}
}

func TestSeedDoesNotOverwrite(t *testing.T) {
	db := testpg.New(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	board := store.DefaultBoard().List()
	board[1].Label = "Analysis"
	if err := store.Save(context.Background(), board); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := NewStore(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, _ := reopened.DefaultBoard().Get(types.StageInProgress); got.Label != "Analysis" {
		t.Errorf("reopened label = %q, want user edit to survive", got.Label)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	db := testpg.New(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	board := store.DefaultBoard().List()
	custom := types.StageInfo{
		ID: "security_review", Label: "Security Review", Color: "#ff0044",
		AutoAdvance: true, PushBranch: true, WipGroup: types.WipGroupReview,
	}
	board = append(board[:2], append([]types.StageInfo{custom}, board[2:]...)...)
	if err := store.Save(context.Background(), board); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reopened, err := NewStore(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.DefaultBoard().Get("security_review")
	if !ok {
		t.Fatal("custom stage missing after reload")
	}
	if !got.AutoAdvance || !got.PushBranch || got.WipGroup != types.WipGroupReview ||
		got.Color != "#ff0044" || got.Label != "Security Review" {
		t.Errorf("custom stage lost fields on round-trip: %+v", got)
	}
	if next := reopened.DefaultBoard().Next(types.StageInProgress); next != "security_review" {
		t.Errorf("Next(in_progress) = %s, want security_review", next)
	}
	if next := reopened.DefaultBoard().Next("security_review"); next != types.StageDone {
		t.Errorf("Next(security_review) = %s, want done", next)
	}
}

func TestSaveRejectsInvalidBoards(t *testing.T) {
	store := newTestStore(t)
	before := store.DefaultBoard().List()
	bad := [][]types.StageInfo{
		nil,
		{{ID: "only", Label: "Only", Initial: true}},
		{{ID: "with-hyphen", Label: "X", Initial: true, Terminal: false}},
		{{ID: "mention", Label: "M", Initial: true}},
		{{ID: "a", Label: "A", Initial: true, Terminal: true}},
		{{ID: "a", Label: "A", Initial: true}, {ID: "a", Label: "A2", Terminal: true}},
	}
	for i, b := range bad {
		if err := store.Save(context.Background(), b); err == nil {
			t.Errorf("case %d: Save accepted an invalid board: %v", i, b)
		}
	}
	if !reflect.DeepEqual(store.DefaultBoard().List(), before) {
		t.Error("rejected Save mutated the live board")
	}
}

func TestSaveRenumbersPositions(t *testing.T) {
	store := newTestStore(t)
	board := store.DefaultBoard().List()
	for i := range board {
		board[i].Position = 999
	}
	if err := store.Save(context.Background(), board); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for i, st := range store.DefaultBoard().List() {
		if st.Position != (i+1)*10 {
			t.Errorf("position[%d] = %d, want %d", i, st.Position, (i+1)*10)
		}
	}
}
