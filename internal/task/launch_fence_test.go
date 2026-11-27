package task

import (
	"context"
	"errors"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

// Holds every launch inside LaunchForStage until the test lets it go.
type gatedLauncher struct {
	*knownAgentLauncher
	entered chan struct{}
	release chan struct{}
}

func newGatedLauncher(agent *types.AgentType) *gatedLauncher {
	return &gatedLauncher{
		knownAgentLauncher: &knownAgentLauncher{recordingLauncher: newRecordingLauncher(), agent: agent},
		entered:            make(chan struct{}, 4),
		release:            make(chan struct{}),
	}
}

func (l *gatedLauncher) LaunchForStage(ctx context.Context, t *Task, s Stage, at *types.AgentType,
	env map[string]string, worktrees, stagePaths map[string]string, ro bool,
) (string, string, string, error) {
	l.entered <- struct{}{}
	<-l.release
	return l.recordingLauncher.LaunchForStage(ctx, t, s, at, env, worktrees, stagePaths, ro)
}

func fenceTask(t *testing.T, store *Store) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "x", Stage: "code", StageAssignments: map[Stage]string{"code": "dev"}}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var fenceAgent = &types.AgentType{Name: "dev"}

// The launch was decided with the task in code, but something moved it before the container
// started: the run for code never starts on a task that is no longer there.
func TestLaunchSkipsATaskThatLeftTheStage(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	l := &knownAgentLauncher{recordingLauncher: newRecordingLauncher(), agent: fenceAgent}
	d.launcher = l
	stale := fenceTask(t, store)
	moveTo(t, store, stale.ID, "verify")

	d.launchStage(t.Context(), stale, "code", "dev", fenceAgent, launchOpts{})
	if n := len(l.launches()); n != 0 {
		t.Fatalf("launches = %d, want none for a task that left code", n)
	}
	after, _ := store.Get(t.Context(), stale.ID)
	if after.Stage != "verify" || after.Status != StatusIdle {
		t.Errorf("task = %q/%q, want verify/idle", after.Stage, after.Status)
	}
	if hasCommentKey(t, store, stale.ID, "task.comments.launch_failed") {
		t.Error("a skipped launch reads as a failure")
	}
}

// A mover outside the dispatcher (a merge) must not move a task whose run is starting: the
// container would work on a task that already sits in the next stage, next to that stage's run.
func TestFencedMoveRefusesAStartingOrRunningRun(t *testing.T) {
	d, store := newKickDispatcher(t, 0)
	l := newGatedLauncher(fenceAgent)
	d.launcher = l
	tk := fenceTask(t, store)
	verify := Stage("verify")

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.launchStage(context.WithoutCancel(t.Context()), tk, "code", "dev", fenceAgent, launchOpts{})
	}()
	<-l.entered
	if _, err := d.UpdateIfStageFenced(t.Context(), tk.ID, "code", Patch{Stage: &verify}); !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("fenced move while the container starts: %v, want ErrRunInFlight", err)
	}
	close(l.release)
	<-done
	if _, err := d.UpdateIfStageFenced(t.Context(), tk.ID, "code", Patch{Stage: &verify}); !errors.Is(err, ErrRunInFlight) {
		t.Fatalf("fenced move while the run works: %v, want ErrRunInFlight", err)
	}
	if got, _ := store.Get(t.Context(), tk.ID); got.Stage != "code" || got.Status != StatusRunning {
		t.Fatalf("task = %q/%q, want code/running", got.Stage, got.Status)
	}

	idle := StatusIdle
	if _, err := store.Update(t.Context(), tk.ID, Patch{Status: &idle}); err != nil {
		t.Fatal(err)
	}
	moved, err := d.UpdateIfStageFenced(t.Context(), tk.ID, "code", Patch{Stage: &verify})
	if err != nil || moved.Stage != verify {
		t.Fatalf("fenced move with no run: %v, %+v; want it moved", err, moved)
	}
	if _, err := d.UpdateIfStageFenced(t.Context(), tk.ID, "code", Patch{Stage: &verify}); !IsStageChanged(err) {
		t.Errorf("fenced move from a stage the task left: %v, want ErrStageChanged", err)
	}
}
