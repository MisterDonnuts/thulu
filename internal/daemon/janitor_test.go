package daemon

import (
	"context"
	"path/filepath"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestIsJanitorReleasableStatus(t *testing.T) {
	releasable := []task.RunStatus{
		task.StatusFailed,
		task.StatusOutOfTurns,
		task.StatusBlocked,
		task.StatusNeedsMoreTurns,
	}
	for _, s := range releasable {
		if !isJanitorReleasableStatus(s) {
			t.Errorf("expected status %q to be releasable", s)
		}
	}

	for _, s := range []task.RunStatus{task.StatusRunning, task.StatusIdle, task.RunStatus("")} {
		if isJanitorReleasableStatus(s) {
			t.Errorf("status %q must NOT be releasable by janitor", s)
		}
	}
}

func TestJanitorDefaults(t *testing.T) {
	j := newJanitor(nil, nil, nil, nil, "")
	if j.interval.Hours() < 1 {
		t.Errorf("janitor interval too aggressive: %v", j.interval)
	}
	if j.agentExitedTTL.Hours() < 1 {
		t.Errorf("agentExitedTTL too aggressive: %v", j.agentExitedTTL)
	}
	if j.sandboxExitedTTL.Hours() < 4 {
		t.Errorf("sandboxExitedTTL too aggressive: %v", j.sandboxExitedTTL)
	}
	if j.failedTaskSandboxTTL.Hours() < 4 {
		t.Errorf("failedTaskSandboxTTL too aggressive: %v", j.failedTaskSandboxTTL)
	}
	if j.buildCacheTTL.Hours() < 24*3 {
		t.Errorf("buildCacheTTL too aggressive: %v", j.buildCacheTTL)
	}
}

// A root whose group all reached a release_resources stage gave its sandbox up; one standing again
// is the janitor's, unless the task is running.
func TestReleasedGroupRoot(t *testing.T) {
	ctx := context.Background()
	tasks, err := task.NewStore(testpg.New(t), filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("task.NewStore: %v", err)
	}
	tasks.WithStages(stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "backlog", Label: "Backlog", Position: 10, Initial: true},
		{ID: "in_progress", Label: "In progress", Position: 20},
		{ID: "release", Label: "Release", Position: 30, ReleaseResources: true},
		{ID: "done", Label: "Done", Position: 40, Terminal: true},
	}))
	create := func(tk *task.Task) *task.Task {
		t.Helper()
		tk.WorkspaceID = "ws1"
		if err := tasks.Create(ctx, tk); err != nil {
			t.Fatalf("create task: %v", err)
		}
		return tk
	}
	alone := create(&task.Task{Title: "alone", Stage: "release", Status: task.StatusIdle})
	running := create(&task.Task{Title: "running", Stage: "release", Status: task.StatusRunning})
	parent := create(&task.Task{Title: "parent", Stage: "release", Status: task.StatusIdle})
	child := create(&task.Task{Title: "child", Stage: "in_progress", ParentID: parent.ID})
	coding := create(&task.Task{Title: "coding", Stage: "in_progress", Status: task.StatusIdle})

	for _, c := range []struct {
		tk   *task.Task
		want bool
	}{
		{alone, true},
		{running, false},
		{parent, false},
		{child, false},
		{coding, false},
	} {
		if got := releasedGroupRoot(ctx, tasks, c.tk); got != c.want {
			t.Errorf("%s: releasedGroupRoot = %v, want %v", c.tk.Title, got, c.want)
		}
	}
}
