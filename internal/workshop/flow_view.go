package workshop

import (
	"context"
	"reflect"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// FlowView is a flow's board as it installs, in one locale: what the Workshop's detail shows so
// nobody has to read flow.json to know the columns, their agents and the task types.
type FlowView struct {
	MaxKickBacks int                     `json:"max_kick_backs,omitempty"`
	Stages       []types.StageInfo       `json:"stages"`
	Types        []stagepkg.TaskTypeInfo `json:"types"`
}

func (f *FlowItem) View(locale string) *FlowView {
	spec := f.Spec(locale, nil, nil)
	return &FlowView{MaxKickBacks: spec.Info.MaxKickBacks, Stages: spec.Stages, Types: spec.Types}
}

// What a plan does to each column of a flow's board.
const (
	StageAdded   = "add"
	StageChanged = "change"
	StageSame    = "same"
	StageRemoved = "remove"
)

type FlowStageChange struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// The column's default agent after the apply, under an agent copy's new name.
	Agent  string `json:"agent,omitempty"`
	Change string `json:"change"`
}

// describeFlows lists, for each flow the plan writes, the columns it creates or changes. An install
// or a copy adds them all; an update or a replace compares against the board it rewrites.
func (p *Plan) describeFlows(ctx context.Context, set *Set, env Env) error {
	copies, missing := p.agentCopies(), p.missingAgents()
	for i := range p.Items {
		it := &p.Items[i]
		if it.Kind != KindFlow || !writes(it.Resolution) {
			continue
		}
		f, _ := set.Flow(it.Name)
		spec := f.Spec(p.locale, p.flowAgents(f.Name, copies), missing)
		var current []types.StageInfo
		if it.Resolution == ResolveUpdate || it.Resolution == ResolveReplace {
			cols, err := boardColumns(ctx, env.Boards, f.Name, it.Target)
			if err != nil {
				return err
			}
			current = cols
		}
		it.Stages = stageChanges(spec.Stages, current)
	}
	return nil
}

func boardColumns(ctx context.Context, boards BoardState, flow, boardID string) ([]types.StageInfo, error) {
	b, err := flowBoard(ctx, boards, flow, boardID)
	if err != nil || b == nil {
		return nil, err
	}
	return b.Columns, nil
}

// With no current board every column is new. A column both have keeps the board's label, as
// UpdateFlowBoard does.
func stageChanges(next, current []types.StageInfo) []FlowStageChange {
	had := make(map[types.Stage]types.StageInfo, len(current))
	for i := range current {
		had[current[i].ID] = current[i]
	}
	out := make([]FlowStageChange, 0, len(next)+len(current))
	kept := make(map[types.Stage]bool, len(next))
	for i := range next {
		st := next[i]
		c := FlowStageChange{ID: string(st.ID), Label: st.Label, Agent: st.DefaultAgent, Change: StageAdded}
		if cur, ok := had[st.ID]; ok {
			kept[st.ID] = true
			c.Label, c.Change = cur.Label, StageChanged
			if reflect.DeepEqual(stageShape(cur), stageShape(st)) {
				c.Change = StageSame
			}
		}
		out = append(out, c)
	}
	for i := range current {
		if !kept[current[i].ID] {
			out = append(out, FlowStageChange{ID: string(current[i].ID), Label: current[i].Label, Change: StageRemoved})
		}
	}
	return out
}
