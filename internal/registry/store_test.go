package registry

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newMirrored(t *testing.T) (*Registry, *Store) {
	t.Helper()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	r := New()
	r.AttachStore(st)
	return r, st
}

func TestMirror_UpsertRemoveRoundTrip(t *testing.T) {
	r, st := newMirrored(t)
	a := &agent.Agent{ID: "abc123", Name: "hellraiser-1", TaskID: "t1", Status: agent.Status("running"), StartedAt: time.Now()}
	r.Upsert(a)

	var taskID string
	if err := st.db.QueryRow(`SELECT task_id FROM agent_registry WHERE id = 'abc123'`).Scan(&taskID); err != nil {
		t.Fatalf("mirror row after Upsert: %v", err)
	}
	if taskID != "t1" {
		t.Fatalf("mirror task_id = %q, want %q", taskID, "t1")
	}

	r.Remove("abc123")
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM agent_registry`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("mirror should be empty after Remove, got %d rows", n)
	}
}

func TestMemoryOnly_NoStoreAttached(t *testing.T) {
	r := New()
	r.Upsert(&agent.Agent{ID: "x"})
	r.Remove("x")
}
