package registry

import (
	"fmt"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agent"
)

func makeAgent(id, name string) *agent.Agent {
	return &agent.Agent{
		ID:     id,
		Name:   name,
		Type:   "test",
		Status: agent.StatusRunning,
	}
}

func TestNewRegistry(t *testing.T) {
	r := New()
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if got := r.List(); len(got) != 0 {
		t.Fatalf("new registry should be empty, got %d agents", len(got))
	}
}

func TestUpsertAndGetByID(t *testing.T) {
	r := New()
	a := makeAgent("abc123def456", "my-agent")
	r.Upsert(a)

	got, err := r.Get("abc123def456")
	if err != nil {
		t.Fatalf("Get by ID: %v", err)
	}
	if got.Name != "my-agent" {
		t.Fatalf("expected name 'my-agent', got %q", got.Name)
	}
}

func TestGetByName(t *testing.T) {
	r := New()
	r.Upsert(makeAgent("abc123def456", "my-agent"))

	got, err := r.Get("my-agent")
	if err != nil {
		t.Fatalf("Get by name: %v", err)
	}
	if got.ID != "abc123def456" {
		t.Fatalf("expected ID 'abc123def456', got %q", got.ID)
	}
}

func TestGetByIDPrefix(t *testing.T) {
	r := New()
	r.Upsert(makeAgent("abc123def456", "my-agent"))

	got, err := r.Get("abc1")
	if err != nil {
		t.Fatalf("Get by prefix: %v", err)
	}
	if got.ID != "abc123def456" {
		t.Fatalf("expected ID 'abc123def456', got %q", got.ID)
	}
}

func TestGetAmbiguous(t *testing.T) {
	r := New()
	r.Upsert(makeAgent("abc123000001", "agent-a"))
	r.Upsert(makeAgent("abc123000002", "agent-b"))

	_, err := r.Get("abc123")
	if err == nil {
		t.Fatal("expected ambiguous error, got nil")
	}
}

func TestGetNotFound(t *testing.T) {
	r := New()
	_, err := r.Get("nonexistent")
	if err == nil {
		t.Fatal("expected not found error, got nil")
	}
}

func TestRemove(t *testing.T) {
	r := New()
	r.Upsert(makeAgent("abc123def456", "my-agent"))
	r.Remove("abc123def456")

	_, err := r.Get("abc123def456")
	if err == nil {
		t.Fatal("expected not found after Remove, got nil")
	}
	if len(r.List()) != 0 {
		t.Fatal("List should be empty after Remove")
	}
}

func TestRemoveNonexistent(t *testing.T) {
	r := New()
	r.Remove("nonexistent")
}

func TestList(t *testing.T) {
	r := New()
	r.Upsert(makeAgent("id1", "agent-1"))
	r.Upsert(makeAgent("id2", "agent-2"))
	r.Upsert(makeAgent("id3", "agent-3"))

	got := r.List()
	if len(got) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(got))
	}

	names := map[string]bool{}
	for _, a := range got {
		names[a.Name] = true
	}
	for _, name := range []string{"agent-1", "agent-2", "agent-3"} {
		if !names[name] {
			t.Errorf("missing agent %q in List()", name)
		}
	}
}

func TestUpsertOverwrites(t *testing.T) {
	r := New()
	r.Upsert(makeAgent("id1", "agent-v1"))

	updated := makeAgent("id1", "agent-v2")
	updated.Status = agent.StatusExited
	r.Upsert(updated)

	got, _ := r.Get("id1")
	if got.Name != "agent-v2" {
		t.Fatalf("expected updated name 'agent-v2', got %q", got.Name)
	}
	if got.Status != agent.StatusExited {
		t.Fatalf("expected status exited, got %s", got.Status)
	}
	if len(r.List()) != 1 {
		t.Fatal("Upsert should not create duplicate")
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := New()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Upsert(makeAgent(fmt.Sprintf("id-%d", n), fmt.Sprintf("agent-%d", n)))
		}(i)
	}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.List()
		}()
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Remove(fmt.Sprintf("id-%d", n))
		}(i)
	}

	wg.Wait()

	got := r.List()
	if len(got) > 100 {
		t.Fatalf("unexpected agent count: %d", len(got))
	}
}
