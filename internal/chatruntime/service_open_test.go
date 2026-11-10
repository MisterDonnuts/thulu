package chatruntime

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestOpenChat_ReusesOnlyYourOwnTaskChat(t *testing.T) {
	r := newRig(t, 5)
	svc := NewService(r.chats, r.mgr, chat.NewBus(), "", nil).
		WithAgentSource(stubSource{all: []types.AgentType{*testAgent()}})
	ana := chat.Creator{Name: "ana@x.com", Owner: "user:ana"}
	bob := chat.Creator{Name: "bob@x.com", Owner: "user:bob"}

	first, reused, err := svc.OpenChat(t.Context(), "ws-1", "manager", "t-1", "Fix the export", "", ana)
	if err != nil || reused {
		t.Fatalf("OpenChat = %v, reused %v; want a new chat", err, reused)
	}
	if first.Title != "Manager · Fix the export" {
		t.Errorf("title = %q, want it named after the task", first.Title)
	}

	again, reused, err := svc.OpenChat(t.Context(), "ws-1", "manager", "t-1", "Fix the export", "Otro", ana)
	if err != nil || !reused || again.ID != first.ID {
		t.Fatalf("ana reopening = %+v, reused %v, %v; want %s reused", again, reused, err, first.ID)
	}

	bobs, reused, err := svc.OpenChat(t.Context(), "ws-1", "manager", "t-1", "Fix the export", "", bob)
	if err != nil || reused || bobs.ID == first.ID {
		t.Fatalf("bob opening = %+v, reused %v, %v; want a chat of his own", bobs, reused, err)
	}

	viaMention, err := svc.FindOrCreateForTask(t.Context(), "ws-1", "manager", "t-1", "", "Fix the export")
	if err != nil || viaMention.ID == first.ID || viaMention.ID == bobs.ID || viaMention.Personal() {
		t.Fatalf("FindOrCreateForTask = %+v, %v; want the daemon's own, shared chat", viaMention, err)
	}
	if again, err := svc.FindOrCreateForTask(t.Context(), "ws-1", "manager", "t-1", "", ""); err != nil || again.ID != viaMention.ID {
		t.Errorf("second mention = %+v, %v; want %s reused", again, err, viaMention.ID)
	}

	loose, reused, err := svc.OpenChat(t.Context(), "ws-1", "manager", "", "", "Plan", ana)
	if err != nil || reused || loose.Title != "Plan" {
		t.Fatalf("loose OpenChat = %+v, reused %v, %v", loose, reused, err)
	}
}

func TestTaskChatTitle(t *testing.T) {
	cases := []struct {
		agent, task string
		thread      bool
		want        string
	}{
		{"reviewer", "Fix", false, "Reviewer · Fix"},
		{"reviewer", "Fix", true, "Reviewer · Fix · thread"},
		{"reviewer", "Fix rounding in the cart total", false, "Reviewer · Fix rounding in the cart total"},
		{"reviewer", "Fixing the café menu total on the checkout page across every locale", false, "Reviewer · Fixing the café menu total on the checko…"},
		{"reviewer", "  ", false, "Reviewer"},
		{"reviewer", "a very long task title that goes on past forty runes", false, "Reviewer · a very long task title that goes on past…"},
	}
	for _, c := range cases {
		if got := taskChatTitle("en", c.agent, c.task, c.thread); got != c.want {
			t.Errorf("taskChatTitle(%q, %q, %v) = %q, want %q", c.agent, c.task, c.thread, got, c.want)
		}
	}
	if got := taskChatTitle("es", "reviewer", "Fix", true); got != "Reviewer · Fix · hilo" {
		t.Errorf("Spanish thread chat title = %q", got)
	}
}
