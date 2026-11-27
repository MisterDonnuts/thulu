package task

import (
	"slices"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A real task in review: the refiner's flags and its question stayed open while the task went through
// four columns. Only the review sign-off and the question review asked wait for a person.
func TestWaitingKeepsOnlyWhatHoldsTheStageTheTaskIsIn(t *testing.T) {
	d, store := newStageTestDispatcher(t, attentionBoard())
	ctx := t.Context()
	tk := createIn(t, store, "review", "")
	info, _ := attentionBoard().Get("review")
	if _, err := store.EnsureStageApprovals(ctx, tk, info); err != nil {
		t.Fatal(err)
	}
	flag := &Flag{Kind: FlagCustom, Label: "Specs that pin the old route must be rewritten", CreatedBy: "agent:refiner"}
	if err := store.AddFlag(ctx, tk.ID, flag); err != nil {
		t.Fatal(err)
	}
	old := askIn(t, store, tk, "intake", "refiner-run", false)
	now := askIn(t, store, tk, "review", "delivery-run", true)

	got := mustGet(t, store, tk.ID)
	w := d.WaitingFor(got)
	signOff := ""
	for _, a := range got.Approvals {
		if a.Key == "qa" {
			signOff = a.ID
		}
	}
	if signOff == "" || !slices.Equal(w.Approvals, []string{signOff}) {
		t.Errorf("waiting approvals = %v, want only the review sign-off %q (not the flag %q)", w.Approvals, signOff, flag.ID)
	}
	if !slices.Equal(w.Questions, []string{now.ID}) {
		t.Errorf("waiting questions = %v, want only review's %q (not intake's %q)", w.Questions, now.ID, old.ID)
	}
}

// A stage gated by flags waits on the agent's flags: they hold the move out of it.
func TestWaitingCountsTheFlagsWhereTheStageIsGatedByThem(t *testing.T) {
	board := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "intake", Label: "Intake", Position: 10, Initial: true},
		{ID: "review", Label: "Review", Position: 20, BlockedByFlags: true},
		{ID: "shipped", Label: "Shipped", Position: 30, Terminal: true},
	})
	d, store := newStageTestDispatcher(t, board)
	ctx := t.Context()
	tk := createIn(t, store, "review", "")
	flag := &Flag{Kind: FlagCustom, Label: "Auth flow changes", CreatedBy: "agent:refiner"}
	if err := store.AddFlag(ctx, tk.ID, flag); err != nil {
		t.Fatal(err)
	}
	if w := d.WaitingFor(mustGet(t, store, tk.ID)); !slices.Equal(w.Approvals, []string{flag.ID}) {
		t.Errorf("waiting approvals = %v, want the flag %q", w.Approvals, flag.ID)
	}

	done := createIn(t, store, "shipped", "")
	if err := store.AddFlag(ctx, done.ID, &Flag{Kind: FlagCustom, Label: "Late note", CreatedBy: "agent:refiner"}); err != nil {
		t.Fatal(err)
	}
	if w := d.WaitingFor(mustGet(t, store, done.ID)); len(w.Approvals) != 0 || len(w.Questions) != 0 {
		t.Errorf("a shipped task waits on %+v, want nobody", w)
	}
}
