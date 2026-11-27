package chatruntime

import (
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func queueRig(t *testing.T) (*rig, *Service, *chat.Chat) {
	t.Helper()
	r := newRig(t, 5)
	r.docker.gate = make(chan struct{})
	svc := NewService(r.chats, r.mgr, chat.NewBus(), "", nil).
		WithAgentSource(stubSource{all: []types.AgentType{*testAgent()}})
	c, err := svc.CreateChat(t.Context(), "ws-1", "manager", "", "", chat.Creator{Name: "ana@x.com", Owner: "user:ana"})
	if err != nil {
		t.Fatal(err)
	}
	return r, svc, c
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func messages(t *testing.T, svc *Service, chatID string) []chat.Message {
	t.Helper()
	msgs, err := svc.Store().ListMessages(t.Context(), chatID, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

func TestPostQueuesBehindARunningTurnAndRunsItAfter(t *testing.T) {
	r, svc, c := queueRig(t)
	ana := "ana@x.com"

	if _, q, err := svc.Post(t.Context(), c.ID, Post{Author: ana, Text: "¿Qué falta en ABC-123?"}); err != nil || q != nil {
		t.Fatalf("first post = %v, queued %v", err, q)
	}
	waitFor(t, "the turn to run", func() bool { return svc.Runtime().TurnInFlight(c.ID) })

	if _, _, err := svc.Post(t.Context(), c.ID, Post{Author: ana, Text: "sin modo"}); !errors.Is(err, ErrTurnInFlight) {
		t.Fatalf("post without mode while busy = %v, want ErrTurnInFlight", err)
	}
	_, q, err := svc.Post(t.Context(), c.ID, Post{Author: ana, Text: "y después limpiá", Mode: PostQueue})
	if err != nil || q == nil {
		t.Fatalf("queued post = %v, %v", q, err)
	}
	_, gone, _ := svc.Post(t.Context(), c.ID, Post{Author: ana, Text: "este no", Mode: PostQueue})
	if got := svc.Queue(c.ID); len(got) != 2 {
		t.Fatalf("queue = %+v, want 2", got)
	}
	if !svc.Unqueue(t.Context(), c.ID, gone.ID) || svc.Unqueue(t.Context(), c.ID, "q-missing") {
		t.Fatal("Unqueue did not take back exactly the queued message")
	}

	close(r.docker.gate)
	waitFor(t, "both turns", func() bool { return !svc.Runtime().TurnInFlight(c.ID) && len(messages(t, svc, c.ID)) == 4 })

	msgs := messages(t, svc, c.ID)
	if msgs[2].Text != "y después limpiá" || msgs[2].Role != chat.RoleUser {
		t.Errorf("third message = %+v, want the queued one", msgs[2])
	}
	if msgs[1].Metadata[chat.MetaTurnID] != msgs[0].ID {
		t.Errorf("answer's turn id = %q, want %q", msgs[1].Metadata[chat.MetaTurnID], msgs[0].ID)
	}
	if lines, err := svc.Runtime().Transcript(c.ID, msgs[0].ID); err != nil || len(lines) == 0 {
		t.Errorf("transcript = %v, %v; want the turn's lines", lines, err)
	}
	if got, _ := svc.Store().GetChat(t.Context(), c.ID); got.Title != "¿Qué falta en ABC-123?" {
		t.Errorf("title = %q, want it taken from the first message", got.Title)
	}
	if len(svc.Queue(c.ID)) != 0 {
		t.Error("queue not empty after the turns")
	}
}

func TestCancelTurnStopsItAndSaysSo(t *testing.T) {
	r, svc, c := queueRig(t)
	if _, _, err := svc.Post(t.Context(), c.ID, Post{Author: "ana@x.com", Text: "hacé algo largo"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to run", func() bool { return svc.Runtime().TurnInFlight(c.ID) })
	waitFor(t, "the exec", func() bool {
		r.docker.mu.Lock()
		defer r.docker.mu.Unlock()
		return len(r.docker.execs) > 0
	})
	if !svc.CancelTurn(c.ID) {
		t.Fatal("CancelTurn = false with a turn running")
	}
	close(r.docker.gate)
	waitFor(t, "the turn to end", func() bool { return !svc.Runtime().TurnInFlight(c.ID) })

	msgs := messages(t, svc, c.ID)
	last := msgs[len(msgs)-1]
	if last.Metadata[chat.MetaCanceled] != "true" || last.Metadata[chat.MetaError] != "true" {
		t.Errorf("last message = %+v, want a canceled turn", last)
	}
	if svc.CancelTurn(c.ID) {
		t.Error("CancelTurn = true with no turn running")
	}
}

func TestSteerStopsTheTurnAndGoesFirst(t *testing.T) {
	r, svc, c := queueRig(t)
	ana := "ana@x.com"
	if _, _, err := svc.Post(t.Context(), c.ID, Post{Author: ana, Text: "uno"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn to run", func() bool { return svc.Runtime().TurnInFlight(c.ID) })
	waitFor(t, "the exec", func() bool {
		r.docker.mu.Lock()
		defer r.docker.mu.Unlock()
		return len(r.docker.execs) > 0
	})
	if _, _, err := svc.Post(t.Context(), c.ID, Post{Author: ana, Text: "después", Mode: PostQueue}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Post(t.Context(), c.ID, Post{Author: ana, Text: "ya", Mode: PostSteer}); err != nil {
		t.Fatal(err)
	}
	close(r.docker.gate)
	waitFor(t, "every turn", func() bool { return !svc.Runtime().TurnInFlight(c.ID) && len(messages(t, svc, c.ID)) == 6 })
	msgs := messages(t, svc, c.ID)
	if msgs[1].Metadata[chat.MetaCanceled] != "true" || msgs[2].Text != "ya" || msgs[4].Text != "después" {
		t.Errorf("order = %q / %q / %q", msgs[1].Text, msgs[2].Text, msgs[4].Text)
	}
}
