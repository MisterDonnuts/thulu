package automation_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/automation"
)

// fakeWatches answers Observe from a queue of observations, one per call.
type fakeWatches struct {
	mu    sync.Mutex
	queue []map[string]any
	calls []string
}

func (f *fakeWatches) Observe(_ context.Context, workspaceID, watchID string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, workspaceID+"/"+watchID)
	if len(f.queue) == 0 {
		return map[string]any{"changed": false}, nil
	}
	out := f.queue[0]
	f.queue = f.queue[1:]
	return out, nil
}

const deployWatchSpec = `
name: deploy watch
trigger:
  schedule: "@every 5m"
policy:
  timeout: 2m
steps:
  - id: observe
    deploy_watch:
      watch: dw1
  - id: release
    when: steps.observe.changed
    task:
      title: "Verify the deploy at {{ steps.observe.version }}"
      board: thulu
      type: release
      stage: release
      fingerprint: "deploy-watch-dw1-{{ steps.observe.fingerprint }}"
      on_duplicate: skip
`

func TestDeployWatchStepParses(t *testing.T) {
	spec, err := automation.ParseSpec(deployWatchSpec)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Steps[0].Kind() != automation.StepDeployWatch || spec.Steps[0].DeployWatch.Watch != "dw1" {
		t.Fatalf("step 0 = %+v", spec.Steps[0])
	}
	if !automation.HasDeployWatchStep(spec.Steps) {
		t.Fatal("HasDeployWatchStep = false")
	}
	plain, err := automation.ParseSpec(strings.Replace(deployWatchSpec, "    deploy_watch:\n      watch: dw1\n",
		"    condition: \"true\"\n", 1))
	if err != nil {
		t.Fatal(err)
	}
	if automation.HasDeployWatchStep(plain.Steps) {
		t.Fatal("a recipe without the step reports one")
	}
	if _, err := automation.ParseSpec(strings.Replace(deployWatchSpec, "watch: dw1", `watch: ""`, 1)); err == nil {
		t.Fatal("an empty watch id parsed")
	}
}

// The task step runs only when the observation changed, and the fingerprint keeps one task per
// observed version.
func TestDeployWatchStepOpensTaskOnlyOnChange(t *testing.T) {
	h := newHarness(t)
	watches := &fakeWatches{queue: []map[string]any{
		{"changed": false, "state": "success", "version": "v1", "fingerprint": "success-v1"},
		{"changed": true, "state": "success", "version": "v2", "fingerprint": "success-v2"},
		{"changed": true, "state": "success", "version": "v2", "fingerprint": "success-v2"},
	}}
	h.engine = automation.NewEngine(h.store, automation.Deps{Tasks: h.tasks, Watches: watches})
	a := h.create(t, deployWatchSpec)

	for i := 0; i < 3; i++ {
		if run := h.run(t, a); run.Status != automation.RunSuccess {
			t.Fatalf("run %d status = %s (%s)", i, run.Status, run.Error)
		}
	}
	if got := h.tasks.createdCount(); got != 1 {
		t.Fatalf("created %d tasks, want 1", got)
	}
	if got := h.tasks.created[0].Title; got != "Verify the deploy at v2" {
		t.Errorf("title = %q", got)
	}
	if len(watches.calls) != 3 || watches.calls[0] != "ws-1/dw1" {
		t.Errorf("observe calls = %v", watches.calls)
	}
}

func TestDeployWatchStepWithoutObserverFails(t *testing.T) {
	h := newHarness(t)
	a := h.create(t, deployWatchSpec)
	run := h.run(t, a)
	if run.Status != automation.RunFailed {
		t.Fatalf("status = %s, want failed", run.Status)
	}
}

func TestCreateManagedKeepsOwner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, err := h.store.CreateManaged(ctx, "ws-1", deployWatchSpec, "deploy_watch:dw1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.store.Get(ctx, a.ID, automation.InWorkspace("ws-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ManagedBy != "deploy_watch:dw1" || !got.Enabled {
		t.Fatalf("managed_by = %q enabled = %v", got.ManagedBy, got.Enabled)
	}
	if _, err := h.store.Update(ctx, a.ID, automation.InWorkspace("ws-1"), deployWatchSpec, true); err != nil {
		t.Fatal(err)
	}
	if got, _ = h.store.Get(ctx, a.ID, automation.InWorkspace("ws-1")); got.ManagedBy != "deploy_watch:dw1" {
		t.Fatalf("Update dropped managed_by: %q", got.ManagedBy)
	}
	if _, err := h.store.CreateManaged(ctx, "ws-1", deployWatchSpec, ""); err == nil {
		t.Fatal("CreateManaged without an owner succeeded")
	}
	plain := h.create(t, deployWatchSpec)
	if plain.ManagedBy != "" {
		t.Fatalf("Create set managed_by %q", plain.ManagedBy)
	}
}
