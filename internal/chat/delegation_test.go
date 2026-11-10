package chat_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/chat"
)

func delegatedChat(t *testing.T, s *chat.Store) *chat.Chat {
	t.Helper()
	c := &chat.Chat{WorkspaceID: "ws-1", AgentType: "admin", Title: "Admin", CreatedBy: "ana@test.com"}
	if err := s.CreateChat(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDelegation(context.Background(), c.ID, "ana@test.com", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return c
}

func pendingAction(t *testing.T, s *chat.Store, chatID string) *chat.Action {
	t.Helper()
	a := &chat.Action{ChatID: chatID, WorkspaceID: "ws-1", Agent: "admin", Method: "DELETE", Path: "/tasks/t1", Summary: "Delete task «x»"}
	if err := s.CreateAction(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDelegationRoundTrip(t *testing.T) {
	s := newStore(t)
	c := delegatedChat(t, s)
	got, err := s.GetChat(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Delegated(time.Now()) || got.DelegatedBy != "ana@test.com" {
		t.Fatalf("delegation not stored: %+v", got)
	}
	if got.Delegated(time.Now().Add(2 * time.Hour)) {
		t.Error("delegation still in force after it lapsed")
	}
	if err := s.ClearDelegation(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetChat(context.Background(), c.ID)
	if got.Delegated(time.Now()) || got.DelegatedBy != "" {
		t.Errorf("delegation survived ClearDelegation: %+v", got)
	}
}

func TestDecideActionHasOneWinner(t *testing.T) {
	s := newStore(t)
	c := delegatedChat(t, s)
	a := pendingAction(t, s, c.ID)

	var wg sync.WaitGroup
	wins := make(chan struct{}, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.DecideAction(context.Background(), c.ID, a.ID, chat.ActionConfirmed, "ana@test.com"); err == nil {
				wins <- struct{}{}
			} else if !errors.Is(err, chat.ErrActionDecided) {
				t.Errorf("DecideAction: %v", err)
			}
		}()
	}
	wg.Wait()
	close(wins)
	if n := len(wins); n != 1 {
		t.Errorf("%d confirms won, want 1", n)
	}
}

func TestDelegationChangeCancelsPendingActions(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for name, change := range map[string]func(id string) error{
		"clear": func(id string) error { return s.ClearDelegation(ctx, id) },
		"renew": func(id string) error { return s.SetDelegation(ctx, id, "ana@test.com", time.Now().Add(time.Hour)) },
	} {
		c := delegatedChat(t, s)
		a := pendingAction(t, s, c.ID)
		if err := change(c.ID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := s.GetAction(ctx, c.ID, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != chat.ActionCanceled {
			t.Errorf("%s: pending action is %s, want canceled", name, got.Status)
		}
	}
}

func TestDelegationPolicyDefaultsOn(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if ok, err := s.DelegationAllowed(ctx, "ws-1"); err != nil || !ok {
		t.Fatalf("default = %v, %v; want allowed", ok, err)
	}
	if err := s.SetDelegationAllowed(ctx, "ws-1", false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.DelegationAllowed(ctx, "ws-1"); ok {
		t.Error("still allowed after turning it off")
	}
	if ok, _ := s.DelegationAllowed(ctx, "ws-2"); !ok {
		t.Error("another workspace's switch moved")
	}
}
