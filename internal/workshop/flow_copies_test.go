package workshop

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
)

// An install that copied the flow's agents (the workspace had edited ones under those names) keeps
// the copies on a later update: the column and the type override stay on writer-2 and shipper-2,
// and the plan says so before it writes.
func TestFlowUpdateKeepsTheAgentCopiesTheInstallNamed(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, p := r.plan(t, ctx, c, Choices{}, Ref{Kind: KindAgent, Name: "writer"}, Ref{Kind: KindAgent, Name: "shipper"})
	r.apply(t, ctx, set, p)
	time.Sleep(5 * time.Millisecond)
	for _, name := range []string{"writer", "shipper"} {
		if _, err := r.st.Store.PutFile(ctx, tenant, agentstore.KindAgent, name, "prompts/main.md", []byte("mine"), "person"); err != nil {
			t.Fatal(err)
		}
	}
	set, p = r.plan(t, ctx, c, Choices{Resolutions: map[string]Resolution{
		"agent/writer": ResolveCopy, "agent/shipper": ResolveCopy,
	}})
	r.apply(t, ctx, set, p)
	if got := r.boards.BoardTypes("mini")[1].Agents["ship"]; got != "shipper-2" {
		t.Fatalf("release type's ship agent after install = %q, want shipper-2", got)
	}

	v2 := []map[string]any{
		flowStage("backlog", map[string]any{"initial": true}),
		flowStage("in_progress", map[string]any{"default_agent": "writer", "auto_advance": true, "push_branch": true}),
		flowStage("ship", nil),
		flowStage("done", map[string]any{"terminal": true}),
	}
	set, p = r.plan(t, ctx, mustCatalog(t, flowFS(t, 2, v2)), Choices{})
	it := itemOf(t, p, KindFlow, "mini")
	if it.Resolution != ResolveUpdate {
		t.Fatalf("flow item = %+v", it)
	}
	shown := ""
	for _, ch := range it.Stages {
		if ch.ID == "in_progress" {
			shown = ch.Agent
		}
	}
	if shown != "writer-2" {
		t.Errorf("plan shows in_progress on %q, want writer-2 (stages %+v)", shown, it.Stages)
	}
	r.apply(t, ctx, set, p)
	got := stageOf(t, r.boards, "mini", "in_progress")
	if got.DefaultAgent != "writer-2" || !got.PushBranch {
		t.Errorf("in_progress after update = agent %q push %v, want writer-2 and the new flag", got.DefaultAgent, got.PushBranch)
	}
	if got := r.boards.BoardTypes("mini")[1].Agents["ship"]; got != "shipper-2" {
		t.Errorf("release type's ship agent after update = %q, want shipper-2", got)
	}
}
