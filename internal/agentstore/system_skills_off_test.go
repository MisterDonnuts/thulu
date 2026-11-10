package agentstore

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestSystemSkillsOffRoundTripsBesideTheGrants(t *testing.T) {
	s, ctx := newStore(t)
	in := Agent{AgentType: types.AgentType{
		Name:            "developer",
		ValidStages:     []types.Stage{"in_progress", "review"},
		SkillsByStage:   map[string][]string{"in_progress": {"notes"}},
		SystemSkillsOff: map[string][]string{"in_progress": {"octopus-run"}, "review": {}},
	}}
	if _, err := s.PutAgent(ctx, GlobalTenant, in, "tester"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.GetAgent(ctx, GlobalTenant, "developer")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.SkillsByStage) != 1 || !slices.Equal(got.SkillsByStage["in_progress"], []string{"notes"}) {
		t.Fatalf("grants = %v, want only in_progress: [notes]", got.SkillsByStage)
	}
	if len(got.SystemSkillsOff) != 1 || !slices.Equal(got.SystemSkillsOff["in_progress"], []string{"octopus-run"}) {
		t.Fatalf("opt-outs = %v, want in_progress: [octopus-run] (an empty list is not stored)", got.SystemSkillsOff)
	}

	list, err := s.ListAgents(ctx, GlobalTenant)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d agents, %v", len(list), err)
	}
	if !list[0].SystemSkillOff("in_progress", "octopus-run") {
		t.Fatalf("the catalog read lost the opt-out: %+v", list[0].SystemSkillsOff)
	}

	got.SystemSkillsOff = nil
	if _, err := s.PutAgent(ctx, GlobalTenant, got, "tester"); err != nil {
		t.Fatalf("put cleared: %v", err)
	}
	cleared, err := s.GetAgent(ctx, GlobalTenant, "developer")
	if err != nil {
		t.Fatalf("get cleared: %v", err)
	}
	if len(cleared.SystemSkillsOff) != 0 {
		t.Fatalf("opt-outs after clearing = %v", cleared.SystemSkillsOff)
	}
	if !slices.Equal(cleared.SkillsByStage["in_progress"], []string{"notes"}) {
		t.Fatalf("clearing the opt-outs touched the grants: %v", cleared.SkillsByStage)
	}
}

func TestSkillsColumnKeepsStagesAndOptOutsApart(t *testing.T) {
	col := skillsColumn(
		map[string][]string{"in_progress": {"notes"}},
		map[string][]string{"in_progress": {"octopus-run"}, "review": nil},
	)
	if len(col) != 2 || !slices.Equal(col[systemOffKeyPrefix+"in_progress"], []string{"octopus-run"}) {
		t.Fatalf("column = %v", col)
	}
	granted, off := splitSkillsColumn(col)
	if len(granted) != 1 || !slices.Equal(granted["in_progress"], []string{"notes"}) {
		t.Fatalf("granted = %v", granted)
	}
	if len(off) != 1 || !slices.Equal(off["in_progress"], []string{"octopus-run"}) {
		t.Fatalf("off = %v", off)
	}
	if _, off := splitSkillsColumn(map[string][]string{"in_progress": {"notes"}}); off != nil {
		t.Fatalf("a column with no opt-outs gave %v, want nil", off)
	}
}
