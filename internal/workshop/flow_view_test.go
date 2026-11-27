package workshop

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func changesOf(it Item) map[string]string {
	out := map[string]string{}
	for _, c := range it.Stages {
		out[c.ID] = c.Change
	}
	return out
}

func TestFlowPlanListsTheColumnsAnInstallCreates(t *testing.T) {
	r, ctx := newFlowRig(t)
	_, p := r.plan(t, ctx, mustCatalog(t, flowFS(t, 1, nil)), Choices{})
	it := itemOf(t, p, KindFlow, "mini")
	if len(it.Stages) != 4 {
		t.Fatalf("stages = %+v, want the 4 columns", it.Stages)
	}
	for _, c := range it.Stages {
		if c.Change != StageAdded {
			t.Errorf("%s: change = %s, want add", c.ID, c.Change)
		}
	}
	build := it.Stages[1]
	if build.ID != "in_progress" || build.Label != "Build" || build.Agent != "writer" {
		t.Errorf("in_progress = %+v, want label Build and agent writer", build)
	}
}

func TestFlowPlanSaysWhatAnUpdateChanges(t *testing.T) {
	r, ctx := newFlowRig(t)
	set, p := r.plan(t, ctx, mustCatalog(t, flowFS(t, 1, nil)), Choices{})
	r.apply(t, ctx, set, p)
	board := r.boards.Board("mini").List()
	board[1].Label = "Coding"
	if err := r.boards.SaveBoard(ctx, "mini", board); err != nil {
		t.Fatal(err)
	}

	v2 := []map[string]any{
		flowStage("backlog", map[string]any{"initial": true}),
		flowStage("in_progress", map[string]any{"default_agent": "writer", "auto_advance": true, "push_branch": true}),
		flowStage("ship", nil),
		flowStage("qa", nil),
		flowStage("done", map[string]any{"terminal": true}),
	}
	_, p = r.plan(t, ctx, mustCatalog(t, flowFS(t, 2, v2)), Choices{})
	it := itemOf(t, p, KindFlow, "mini")
	want := map[string]string{
		"backlog": StageSame, "in_progress": StageChanged, "ship": StageSame, "qa": StageAdded, "done": StageSame,
	}
	got := changesOf(it)
	for id, change := range want {
		if got[id] != change {
			t.Errorf("%s: change = %q, want %q (all: %+v)", id, got[id], change, it.Stages)
		}
	}
	if it.Stages[1].Label != "Coding" {
		t.Errorf("an updated column shows %q, not the board's own label", it.Stages[1].Label)
	}
}

func TestFlowPlanListsAColumnAnUpdateDrops(t *testing.T) {
	next := []types.StageInfo{{ID: "backlog", Label: "Backlog", Initial: true}, {ID: "done", Label: "Done", Terminal: true}}
	current := append([]types.StageInfo{{ID: "ship", Label: "Ship"}}, next...)
	got := stageChanges(next, current)
	if len(got) != 3 || got[2].ID != "ship" || got[2].Change != StageRemoved || got[2].Label != "Ship" {
		t.Errorf("changes = %+v, want ship listed last as removed", got)
	}
}

func TestFlowPlanLeavesKeptFlowsUndescribed(t *testing.T) {
	r, ctx := newFlowRig(t)
	_, p := r.plan(t, ctx, mustCatalog(t, flowFS(t, 1, nil)), Choices{Resolutions: map[string]Resolution{"flow/mini": ResolveSkip}})
	if it := itemOf(t, p, KindFlow, "mini"); len(it.Stages) != 0 {
		t.Errorf("a skipped flow lists stages: %+v", it.Stages)
	}
}

func TestFlowDetailCarriesTheBoardInTheReadersLocale(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	d, found, err := Describe(ctx, r.st, r.env().Boards, c, KindFlow, "mini", "es")
	if err != nil || !found {
		t.Fatalf("describe: found=%v err=%v", found, err)
	}
	if d.Flow == nil || len(d.Flow.Stages) != 4 || len(d.Flow.Types) != 2 || d.Flow.MaxKickBacks != 2 {
		t.Fatalf("flow view = %+v", d.Flow)
	}
	if st := d.Flow.Stages[1]; st.Label != "Construir" || st.DefaultAgent != "writer" {
		t.Errorf("in_progress = %+v, want the Spanish label and its agent", st)
	}
	if agents := d.Flow.Types[1].Agents; agents["ship"] != "shipper" {
		t.Errorf("release type agents = %v", agents)
	}
}
