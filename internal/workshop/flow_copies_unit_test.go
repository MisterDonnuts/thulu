package workshop

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type copiesState struct {
	State
	agents map[string]bool
}

func (s copiesState) Agent(_ context.Context, name string) (agentstore.Agent, bool, error) {
	return agentstore.Agent{}, s.agents[name], nil
}

func (s copiesState) Origin(context.Context, string, string) (agentstore.Origin, bool, error) {
	return agentstore.Origin{}, false, nil
}

type copiesBoards struct {
	BoardState
	byFlow map[string][]FlowBoard
}

func (b copiesBoards) FlowBoards(_ context.Context, flow string) ([]FlowBoard, error) {
	return b.byFlow[flow], nil
}

// A flow whose board is gone does not stop the plan from reading the copies of the next flow.
func TestKeptCopies_SkipsAFlowWhoseBoardIsGone(t *testing.T) {
	flow := func(name string) FlowItem {
		return FlowItem{Name: name, Stages: []FlowStage{{StageInfo: types.StageInfo{ID: "dev", DefaultAgent: "writer"}}}}
	}
	set := &Set{Flows: []FlowItem{flow("gone"), flow("mini")}}
	p := &Plan{Items: []Item{
		{Kind: KindFlow, Name: "gone", Resolution: ResolveUpdate, Target: "b-gone"},
		{Kind: KindFlow, Name: "mini", Resolution: ResolveUpdate, Target: "b-mini"},
	}}
	boards := copiesBoards{byFlow: map[string][]FlowBoard{
		"mini": {{ID: "b-mini", Columns: []types.StageInfo{{ID: "dev", DefaultAgent: "writer-2"}}}},
	}}
	st := copiesState{agents: map[string]bool{"writer-2": true}}
	if err := p.keptCopies(context.Background(), st, set, Env{Boards: boards}); err != nil {
		t.Fatal(err)
	}
	if got := p.kept["mini"]["writer"]; got != "writer-2" {
		t.Fatalf("kept = %+v, want mini's writer kept as writer-2", p.kept)
	}
}

// A board agent that stands for two flow agents, or two board agents for one, is ambiguous.
func TestBoardAgentPairs_DropsAmbiguousNames(t *testing.T) {
	f := &FlowItem{
		Stages: []FlowStage{
			{StageInfo: types.StageInfo{ID: "dev", DefaultAgent: "writer"}},
			{StageInfo: types.StageInfo{ID: "fix", DefaultAgent: "writer"}},
			{StageInfo: types.StageInfo{ID: "ship", DefaultAgent: "shipper"}},
		},
		Types: []FlowType{{TaskTypeInfo: stagepkg.TaskTypeInfo{ID: "release", Agents: map[types.Stage]string{"ship": "releaser"}}}},
	}
	b := &FlowBoard{
		Columns: []types.StageInfo{
			{ID: "dev", DefaultAgent: "writer-2"},
			{ID: "fix", DefaultAgent: "writer-3"},
			{ID: "ship", DefaultAgent: "shipper"},
		},
		Types: []stagepkg.TaskTypeInfo{{ID: "release", Agents: map[types.Stage]string{"ship": "releaser-2"}}},
	}
	got := boardAgentPairs(f, b)
	if len(got) != 1 || got["releaser"] != "releaser-2" {
		t.Fatalf("pairs = %+v, want only releaser → releaser-2", got)
	}
}
