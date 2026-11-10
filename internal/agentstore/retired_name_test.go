package agentstore

import (
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestCreateReusesARetiredName(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.CreateAgent(ctx, GlobalTenant, Agent{AgentType: types.AgentType{
		Name: "backend", Description: "old", ValidStages: []types.Stage{"in_progress"},
	}}, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "backend", "prompts/main.md", []byte("old rules"), "tester"); err != nil {
		t.Fatalf("put prompt: %v", err)
	}
	if _, err := s.CreateAgent(ctx, GlobalTenant, Agent{AgentType: types.AgentType{Name: "backend"}}, "tester"); !errors.Is(err, ErrExists) {
		t.Fatalf("create over an active agent = %v, want ErrExists", err)
	}
	setOrigin(t, s, "backend")
	if err := s.SetDisabled(ctx, GlobalTenant, "backend", true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	got, err := s.CreateAgent(ctx, GlobalTenant, Agent{AgentType: types.AgentType{Name: "backend", Description: "new"}}, "tester")
	if err != nil {
		t.Fatalf("create over a retired name: %v", err)
	}
	if got.Disabled() || got.Description != "new" || len(got.ValidStages) != 0 {
		t.Fatalf("the new agent kept the old one's state: %+v", got)
	}
	files, err := s.ListOwner(ctx, GlobalTenant, KindAgent, "backend")
	if err != nil || len(files) != 0 {
		t.Fatalf("the old prompts reached the new agent: %+v (%v)", files, err)
	}
	revs, err := s.ListRevisions(ctx, GlobalTenant, KindAgent, "backend", "prompts/main.md", 0)
	if err != nil || len(revs) != 2 || revs[0].Action != ActionDeleted {
		t.Fatalf("history = %+v (%v), want the deletion on top of the creation", revs, err)
	}
	if o, err := s.GetOrigin(ctx, GlobalTenant, KindAgent, "backend"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("origin after reuse = %+v (%v), want ErrNotFound", o, err)
	}
}

func setOrigin(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.InTx(t.Context(), func(tx *Tx) error {
		return tx.SetOrigin(GlobalTenant, KindAgent, name, Origin{Origin: "catalog/" + name, Revision: 3, CopiedAt: time.Now().UTC()})
	}); err != nil {
		t.Fatalf("set origin: %v", err)
	}
	if _, err := s.GetOrigin(t.Context(), GlobalTenant, KindAgent, name); err != nil {
		t.Fatalf("origin not recorded: %v", err)
	}
}

func TestCloneOntoARetiredNameCarriesOnlyTheSource(t *testing.T) {
	s, ctx := newStore(t)

	for _, name := range []string{"backend", "reviewer"} {
		if _, err := s.CreateAgent(ctx, GlobalTenant, Agent{AgentType: types.AgentType{Name: name}}, "tester"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "reviewer", "prompts/review.md", []byte("old reviewer"), "tester"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "backend", "prompts/main.md", []byte("backend rules"), "tester"); err != nil {
		t.Fatalf("put: %v", err)
	}
	setOrigin(t, s, "reviewer")
	if err := s.SetDisabled(ctx, GlobalTenant, "reviewer", true); err != nil {
		t.Fatalf("disable: %v", err)
	}

	got, err := s.CloneAgent(ctx, GlobalTenant, "backend", "reviewer", "tester")
	if err != nil {
		t.Fatalf("clone onto a retired name: %v", err)
	}
	if got.Disabled() {
		t.Fatal("the clone came back disabled")
	}
	files, err := s.ListOwner(ctx, GlobalTenant, KindAgent, "reviewer")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 || files[0].Path != "prompts/main.md" {
		t.Fatalf("files = %+v, want only the source's prompt", files)
	}
	if o, err := s.GetOrigin(ctx, GlobalTenant, KindAgent, "reviewer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("origin after clone onto a retired name = %+v (%v), want ErrNotFound", o, err)
	}
}
