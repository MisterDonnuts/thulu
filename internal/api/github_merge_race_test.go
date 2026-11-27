package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Holds a launch inside LaunchForStage, where the container is starting and the task still reads
// idle.
type startingLauncher struct {
	agent   *types.AgentType
	entered chan struct{}
	release chan struct{}
}

func (l *startingLauncher) LaunchForStage(_ context.Context, t *task.Task, s task.Stage, _ *types.AgentType,
	env map[string]string, _, _ map[string]string, _ bool,
) (string, string, string, error) {
	l.entered <- struct{}{}
	<-l.release
	return "ctr-" + t.ID, env["INSTANCE_ID"], "", nil
}
func (l *startingLauncher) OnExit(context.Context, string, func(int, string, string)) {}
func (l *startingLauncher) FindAgentType(_, name string) *types.AgentType {
	if name == l.agent.Name {
		return l.agent
	}
	return nil
}
func (l *startingLauncher) ContainerAlive(string) bool { return true }
func (l *startingLauncher) ScanExitedTaskAgents(context.Context) ([]task.ExitedTaskAgent, error) {
	return nil, nil
}

// The review agent's container is starting when the PR merges: moving the task now would leave that
// agent working on a task already in Release, next to Release's own run. The merge waits for the
// run, as it does for one already running.
func TestPRMerge_WaitsForARunThatIsStarting(t *testing.T) {
	e := newMergeEnv(t)
	p := e.project(t, "app")
	const url = "https://github.com/acme/app/pull/9"
	tk := e.task(t, mtBoard, mtReview, map[string]string{p.ID: url})
	if _, err := e.srv.taskStore.Update(t.Context(), tk.ID, task.Patch{
		StageAssignments: map[task.Stage]string{mtReview: "reviewer"},
	}); err != nil {
		t.Fatal(err)
	}
	tk = e.get(t, tk.ID)
	l := &startingLauncher{agent: &types.AgentType{Name: "reviewer"}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	e.srv.taskDispatcher = task.NewDispatcher(e.srv.taskStore, l, e.srv.projectStore, nil, nil).WithBoards(e.srv.boards)

	e.srv.taskDispatcher.HandleStageEntered(context.WithoutCancel(t.Context()), tk, mtWork)
	<-l.entered
	e.deliver(t, mergedPayload(100, url, sha40('9'), p.DefaultBranch))
	close(l.release)
	deadline := time.Now().Add(5 * time.Second)
	for e.get(t, tk.ID).Status != task.StatusRunning {
		if time.Now().After(deadline) {
			t.Fatal("the run never read running")
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := e.get(t, tk.ID)
	if got.Stage != mtReview || got.MergeSHAs[p.ID] != sha40('9') {
		t.Fatalf("after a merge during the launch: stage %s shas %v, want %s with the sha saved", got.Stage, got.MergeSHAs, mtReview)
	}
	if n := e.notes(t, tk.ID, "api.merges.comments.deferred"); n != 1 {
		t.Errorf("deferred comments = %d, want 1", n)
	}
	if n := len(e.events(t, tk.ID, task.EventStageEntered)); n != 0 {
		t.Errorf("stage_entered = %d, want 0: %s", n, fmt.Sprint(e.events(t, tk.ID, task.EventStageEntered)))
	}
}
