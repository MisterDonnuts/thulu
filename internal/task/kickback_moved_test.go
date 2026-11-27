package task

import (
	"testing"
)

// The auditor decided on the task in verify, but a person moved it to review before the send-back
// landed: the task stays where the person put it, with the status it had there.
func TestKickBackLeavesATaskAPersonMoved(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	stale := createIn(t, store, "verify", "")
	moveTo(t, store, stale.ID, "review")
	waiting := StatusAwaitingApproval
	if _, err := store.Update(t.Context(), stale.ID, Patch{Status: &waiting}); err != nil {
		t.Fatal(err)
	}

	after := auditorKickBack(t, d, store, stale, "code")
	if after.Stage != "review" || after.Status != StatusAwaitingApproval {
		t.Fatalf("after a stale send-back: %q/%q, want review/awaiting_approval", after.Stage, after.Status)
	}
	if moves := eventsOf(t, store, stale.ID, EventStageEntered); len(moves) != 0 {
		t.Errorf("stage_entered = %v, want none", moves)
	}
	if hasCommentKey(t, store, stale.ID, "task.comments.auditor_kick_back") {
		t.Error("the send-back notes were posted on a task that did not move")
	}
}

// Over the cap the send-back blocks the task where it is; a person who already moved it elsewhere
// must not find it blocked there by a decision about a stage it left.
func TestKickBackRefusalLeavesATaskAPersonMoved(t *testing.T) {
	d, store := newKickDispatcher(t, 1)
	tk := createIn(t, store, "verify", "")
	auditorKickBack(t, d, store, tk, "code")
	stale := moveTo(t, store, tk.ID, "verify")
	moveTo(t, store, tk.ID, "review")
	waiting := StatusAwaitingApproval
	if _, err := store.Update(t.Context(), tk.ID, Patch{Status: &waiting}); err != nil {
		t.Fatal(err)
	}

	after := auditorKickBack(t, d, store, stale, "code")
	if after.Stage != "review" || after.Status != StatusAwaitingApproval {
		t.Fatalf("after a stale send-back over the cap: %q/%q, want review/awaiting_approval", after.Stage, after.Status)
	}
	if limits := eventsOf(t, store, tk.ID, EventKickBackLimit); len(limits) != 0 {
		t.Errorf("kick_back_limit = %v, want none", limits)
	}
	if hasCommentKey(t, store, tk.ID, "task.comments.kick_back_limit") {
		t.Error("the limit comment was posted on a task that left the stage")
	}
}
