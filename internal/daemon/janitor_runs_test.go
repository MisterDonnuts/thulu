package daemon

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agent"
)

type settledRuns struct{ asked []string }

func (r *settledRuns) Reconcile(context.Context) {}
func (r *settledRuns) ExitSettled(_ context.Context, inst string) bool {
	r.asked = append(r.asked, inst)
	return inst == "settled"
}

func TestAgentExitSettledKeepsContainersWhoseExitNobodyFinished(t *testing.T) {
	runs := &settledRuns{}
	j := &Janitor{runs: runs}
	ctx := context.Background()
	if j.agentExitSettled(ctx, map[string]string{agent.LabelInstance: "pending"}, nil) {
		t.Fatal("an exit nobody finished was released to the janitor")
	}
	if !j.agentExitSettled(ctx, nil, []string{"/settled"}) {
		t.Fatal("a finished exit, named only by its container name, was kept")
	}
	if len(runs.asked) != 2 || runs.asked[1] != "settled" {
		t.Fatalf("asked %v", runs.asked)
	}
	if !(&Janitor{}).agentExitSettled(ctx, nil, nil) {
		t.Fatal("without a reconciler (Kubernetes) every exit counts as settled")
	}
}
