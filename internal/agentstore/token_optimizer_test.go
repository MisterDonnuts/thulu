package agentstore

import (
	"testing"
	"testing/fstest"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestTokenOptimizerRoundTrips(t *testing.T) {
	s, ctx := newStore(t)
	policy := types.TokenOptimizer{Level: "enforce", Engine: "claude-code", Model: "claude-haiku-4-5"}
	if _, err := s.PutAgent(ctx, StarterTenant, Agent{AgentType: types.AgentType{Name: "developer", TokenOptimizer: policy}}, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAgent(ctx, StarterTenant, Agent{AgentType: types.AgentType{Name: "plain"}}, "t"); err != nil {
		t.Fatal(err)
	}
	check := func(where string, got types.TokenOptimizer) {
		t.Helper()
		if got != policy {
			t.Fatalf("%s: policy = %+v, want %+v", where, got, policy)
		}
	}
	got, err := s.GetAgent(ctx, StarterTenant, "developer")
	if err != nil {
		t.Fatal(err)
	}
	check("row", got.TokenOptimizer)
	if plain, _ := s.GetAgent(ctx, StarterTenant, "plain"); !plain.TokenOptimizer.IsZero() {
		t.Fatalf("an agent without a policy reads one back: %+v", plain.TokenOptimizer)
	}

	if err := s.EnsureSeeded(ctx, "0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	copied, err := s.GetAgent(ctx, "0123456789abcdef", "developer")
	if err != nil {
		t.Fatal(err)
	}
	check("starter copy", copied.TokenOptimizer)

	pack, err := s.ExportPack(ctx, StarterTenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range pack.Agents {
		if a.Name == "developer" {
			check("pack", a.TokenOptimizer)
		}
	}
}

func TestTokenOptimizerOldRowLoadsAsOff(t *testing.T) {
	s, ctx := newStore(t)
	if _, err := s.PutAgent(ctx, StarterTenant, Agent{AgentType: types.AgentType{Name: "developer"}}, "t"); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{
		`{"level":"enforce","by_stage":{"review":"off"},"threshold_bytes":8192}`,
		`{"firm":true,"level":"suggest"}`,
	} {
		if _, err := s.db.ExecContext(ctx, s.rb(`UPDATE agents SET token_optimizer = ?::jsonb WHERE tenant_id = ? AND name = ?`),
			old, StarterTenant, "developer"); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAgent(ctx, StarterTenant, "developer")
		if err != nil {
			t.Fatalf("%s: %v", old, err)
		}
		if !got.TokenOptimizer.IsZero() {
			t.Fatalf("%s loads as %+v, want off", old, got.TokenOptimizer)
		}
		got.Description = "edited"
		if _, err := s.PutAgent(ctx, StarterTenant, got, "t"); err != nil {
			t.Fatalf("%s: saving another field: %v", old, err)
		}
	}
}

func TestSystemSkillWhen(t *testing.T) {
	s, ctx := newStore(t)
	fsys := fstest.MapFS{
		"octopus-token-optimizer/skill.yaml": {Data: []byte("name: octopus-token-optimizer\nversion: 1\nwhen: token_optimizer\n")},
		"octopus-token-optimizer/prompt.md":  {Data: []byte("Delegate large reads.")},
	}
	if _, err := s.SyncSystemSkills(ctx, fsys, "boot"); err != nil {
		t.Fatal(err)
	}
	sk, err := s.GetSystemSkill(ctx, "octopus-token-optimizer")
	if err != nil {
		t.Fatal(err)
	}
	if sk.When != types.SkillWhenTokenOptimizer || sk.MCP != "" {
		t.Fatalf("stored = %+v", sk)
	}
	list, err := s.ListSystemSkills(ctx)
	if err != nil || len(list) != 1 || list[0].When != types.SkillWhenTokenOptimizer {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := s.PutSystemSkillFile(ctx, "octopus-token-optimizer", SkillMetaFile,
		[]byte("name: octopus-token-optimizer\nversion: 1\n"), "op"); err != nil {
		t.Fatal(err)
	}
	if sk, _ = s.GetSystemSkill(ctx, "octopus-token-optimizer"); sk.When != "" {
		t.Fatalf("after hotfix When = %q", sk.When)
	}
}
