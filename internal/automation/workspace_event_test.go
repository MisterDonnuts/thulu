package automation_test

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/automation"
)

func TestScheduler_EventsStayInTheirWorkspace(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(t, reviewSpec)
	legacy := h.create(t, reviewSpec)
	if _, err := h.store.DB().ExecContext(ctx,
		`UPDATE automations SET workspace_id = '' WHERE id = $1`, legacy.ID); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{"action": "created", "sender_is_bot": false, "task_id": "task-x", "comment": "c"}
	for _, wid := range []string{"", "ws-2"} {
		publish(t, h, automation.Event{WorkspaceID: wid, Kind: automation.EventPRReviewComment,
			DedupeKey: "pr-" + wid, Payload: payload})
		n, err := deliverEvents(ctx, t, h)
		if err != nil {
			t.Fatal(err)
		}
		h.sched.Wait()
		if n != 0 {
			t.Errorf("an event of workspace %q fired %d automation(s), want 0", wid, n)
		}
	}
	if got := h.tasks.mentions["task-x"]; len(got) != 0 {
		t.Fatalf("foreign events reached ws-1's recipe or the unstamped one: %v", got)
	}

	publish(t, h, automation.Event{WorkspaceID: "ws-1", Kind: automation.EventPRReviewComment,
		DedupeKey: "pr-ws-1", Payload: payload})
	n, err := deliverEvents(ctx, t, h)
	if err != nil {
		t.Fatal(err)
	}
	h.sched.Wait()
	if n != 1 {
		t.Errorf("ws-1's own event fired %d automation(s), want 1", n)
	}
}
