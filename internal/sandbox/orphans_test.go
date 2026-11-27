package sandbox

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type listingRuntime struct {
	*fakeRuntime
	listed []ListedContainer
}

func (l *listingRuntime) ListSandboxes(context.Context) ([]ListedContainer, error) {
	return l.listed, nil
}

func TestReconcileOrphansRemovesOnlyOldContainersWithoutARow(t *testing.T) {
	m, st, sett, rt := newFakeManager(t)
	ctx := context.Background()
	if _, _, err := m.Exec(ctx, "task-owned", "in_progress", AcquireDockerOpts{}, ExecOpts{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	sb, err := st.Get(ctx, "task-owned")
	if err != nil || sb.Status != StatusReady {
		t.Fatalf("owned sandbox: %+v %v", sb, err)
	}
	old := time.Now().Add(-time.Hour)
	lr := &listingRuntime{fakeRuntime: rt, listed: []ListedContainer{
		{ID: shortID(sb.ContainerID), Name: "octopus-sb-task-owned", Owner: "task-owned", Created: old},
		{ID: "orphan-old", Name: "octopus-sb-gone", Owner: "gone", Created: old},
		{ID: "orphan-young", Name: "octopus-sb-new", Owner: "new", Created: time.Now()},
	}}
	m2 := NewManager(st, sett, lr, ManagerConfig{})

	if got := m2.ReconcileOrphans(ctx); got != 1 {
		t.Fatalf("removed %d, want 1", got)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !slices.Equal(rt.removed, []string{"orphan-old"}) {
		t.Fatalf("removed %v, want only the old orphan", rt.removed)
	}
}

func TestReleaseWithAFailedRemoveKeepsTheRowForTheGC(t *testing.T) {
	m, st, _, rt := newFakeManager(t)
	ctx := context.Background()
	if _, _, err := m.Exec(ctx, "task-stuck", "in_progress", AcquireDockerOpts{}, ExecOpts{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.removeErr = errors.New("docker said no")
	rt.mu.Unlock()
	if err := m.Release(ctx, "task-stuck"); err == nil {
		t.Fatal("a release whose remove failed reported success")
	}
	sb, err := st.Get(ctx, "task-stuck")
	if err != nil {
		t.Fatalf("the row is gone: %v", err)
	}
	if sb.Status != StatusEvicted || sb.ContainerID == "" {
		t.Fatalf("row %+v: want evicted with its container id, for ReapEvicted to retry", sb)
	}
}

func TestExecEnvStampsTheSessionsRun(t *testing.T) {
	run := &mcpSession{runInstance: "inst-a"}
	if got := run.execEnv(map[string]string{"A": "1", EnvRunInstance: "forged"}); got[EnvRunInstance] != "inst-a" || got["A"] != "1" {
		t.Fatalf("run session: %v", got)
	}
	person := &mcpSession{}
	if got := person.execEnv(map[string]string{EnvRunInstance: "forged"}); got[EnvRunInstance] != "" {
		t.Fatalf("a person's exec claimed a run: %v", got)
	}
	in := map[string]string{"A": "1"}
	if got := person.execEnv(in); len(got) != 1 {
		t.Fatalf("plain env changed: %v", got)
	}
	if got := person.execEnv(map[string]string{EnvRunTask: "t-other"}); got[EnvRunTask] != "" {
		t.Fatalf("a person's exec claimed a task's run: %v", got)
	}
}

// A subtask's runs work in its parent's sandbox, which outlives the subtask: each exec carries the
// run's task, so deleting the subtask finds what its runs left there.
func TestExecEnvStampsTheRunsTask(t *testing.T) {
	sub := &mcpSession{runInstance: "dev-task-sub1-dev-1", taskID: "sub1"}
	got := sub.execEnv(map[string]string{EnvRunTask: "parent"})
	if got[EnvRunTask] != "sub1" || got[EnvRunInstance] != "dev-task-sub1-dev-1" {
		t.Fatalf("subtask run: %v", got)
	}
}
