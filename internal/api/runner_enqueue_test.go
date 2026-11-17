package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// liveFixture is a runner fixture whose machine speaks the current protocol.
func liveFixture(t *testing.T) *runnerFixture {
	t.Helper()
	f := newRunnerFixture(t, "ws")
	if _, err := f.store.Authenticate(t.Context(), f.reg.Credential, "test", runner.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *runnerFixture) launch(t *testing.T, tk *task.Task, env map[string]string) *runner.Job {
	t.Helper()
	env[runner.RunnerIDEnv] = f.reg.RunnerID
	cid, _, _, err := f.launcher.LaunchForStage(t.Context(), tk, types.StageInProgress,
		&types.AgentType{Name: "dev", Type: "dev"}, env, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	j, err := f.store.GetJob(t.Context(), runner.JobIDFromRun(cid))
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// Case 17 with the machine project second and not yet in the runner's inventory: the runner that
// maps it takes the job, which a Thulu project listed first must not hide.
func TestJobWithAThuluProjectFirstGoesToTheRunnerThatMapsTheMachineOne(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	f := liveFixture(t)
	web, err := f.projects.Create(t.Context(), "web", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	tk := &task.Task{ID: "t1", WorkspaceID: "ws", ProjectIDs: []string{web.ID, f.project.ID}}
	j := f.launch(t, tk, map[string]string{"INSTANCE_ID": "dev-task-t1-1"})
	if j.Spec.SelfContained() {
		t.Fatal("the fixture's machine project has a folder; the job must need the runner's mapping")
	}
	spec, err := f.store.Claim(t.Context(), f.reg.RunnerID, runner.ProtocolVersion, []string{f.project.ID}, time.Minute)
	if err != nil || spec == nil {
		t.Fatalf("claim = %v, %v; want the runner that maps %s to take the job (project_id %q)",
			spec, err, f.project.ID, j.ProjectID)
	}
}

// No key travels to a machine, whatever left it in the env: every run and chat turn is queued
// without the engine's credential, endpoint and profile vars.
func TestMachineJobsNeverCarryAKey(t *testing.T) {
	leaked := func() map[string]string {
		return map[string]string{"ENGINE": engine.DefaultName, "ANTHROPIC_API_KEY": "sk-workspace",
			"ANTHROPIC_BASE_URL": "https://gw.example", engine.LLMProfileEnv: "k1", "STAGE": "chat"}
	}
	assertClean := func(t *testing.T, env map[string]string) {
		t.Helper()
		for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", engine.LLMProfileEnv} {
			if v, ok := env[k]; ok {
				t.Errorf("job env %s = %q, want it absent", k, v)
			}
		}
		if env["ENGINE"] != engine.DefaultName {
			t.Errorf("job env lost ENGINE: %v", env)
		}
	}

	t.Run("stage run", func(t *testing.T) {
		f := liveFixture(t)
		env := leaked()
		env["INSTANCE_ID"] = "dev-task-t1-1"
		j := f.launch(t, &task.Task{ID: "t1", WorkspaceID: "ws", ProjectIDs: []string{f.project.ID}}, env)
		assertClean(t, j.Spec.Env)
	})

	t.Run("chat turn", func(t *testing.T) {
		f := liveFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			_, _ = f.launcher.RunTurn(ctx, chatruntime.MachineTurn{WorkspaceID: "ws", RunnerID: f.reg.RunnerID,
				ChatID: "chat1", AgentName: "manager", AgentType: "manager", Env: leaked(), Stdin: "hola"})
		}()
		spec := waitClaim(t, f)
		assertClean(t, spec.Env)
	})
}

func waitClaim(t *testing.T, f *runnerFixture) *runner.JobSpec {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		spec, err := f.store.Claim(t.Context(), f.reg.RunnerID, runner.ProtocolVersion, nil, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if spec != nil {
			return spec
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no job was queued")
	return nil
}

// A machine that ended its turn with exit 0 but whose outputs never arrived fails the turn: a
// success with no answer would store an empty reply and lose the engine's session.
func TestChatTurnWhoseOutputsNeverArrivedFails(t *testing.T) {
	f := liveFixture(t)
	type result struct {
		out chatruntime.ExecOutput
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := f.launcher.RunTurn(t.Context(), chatruntime.MachineTurn{WorkspaceID: "ws", RunnerID: f.reg.RunnerID,
			ChatID: "chat1", AgentName: "manager", AgentType: "manager",
			Env: map[string]string{"ENGINE": engine.DefaultName}, Stdin: "hola"})
		done <- result{out, err}
	}()
	spec := waitClaim(t, f)
	if err := f.store.Finish(t.Context(), f.reg.RunnerID, spec.ID, runner.FinishRequest{ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	f.launcher.settle(t.Context())
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		want := i18n.Log("api.runners.log.turn_output_missing")
		if r.out.ExitCode != runner.ExitLost || !strings.Contains(string(r.out.Stderr), want) {
			t.Errorf("exit %d, stderr %q; want the turn lost and said why", r.out.ExitCode, r.out.Stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never ended")
	}
}

// After a restart nobody waits for the chat turns the previous daemon queued: they are canceled, so
// no machine runs one and bills its login for an answer nobody reads.
func TestOrphanChatTurnsAreCanceledAtStart(t *testing.T) {
	f := liveFixture(t)
	ctx := t.Context()
	turn := func(chatID string) *runner.Job {
		j := &runner.Job{RunnerID: f.reg.RunnerID, WorkspaceID: "ws", AgentName: "manager",
			Stage: string(types.StageChat), LaunchKind: runner.LaunchKindChatTurn,
			Spec: runner.JobSpec{Stage: string(types.StageChat), LaunchKind: runner.LaunchKindChatTurn,
				ChatID: chatID, Stdin: "hola"}}
		if err := f.store.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	running := turn("chat-running")
	if spec := waitClaim(t, f); spec.ID != running.ID {
		t.Fatalf("claimed %s, want %s", spec.ID, running.ID)
	}
	queued := turn("chat-queued")
	f.launcher.started = time.Now().Add(time.Hour)

	f.launcher.cancelOrphanTurns(ctx)

	if spec, err := f.store.Claim(ctx, f.reg.RunnerID, runner.ProtocolVersion, nil, time.Minute); err != nil || spec != nil {
		t.Errorf("claim = %+v, %v; want the orphan turn gone from the queue", spec, err)
	}
	if j, err := f.store.GetJob(ctx, queued.ID); err != nil || j.State != runner.StateFinished || *j.ExitCode != runner.ExitCanceled {
		t.Errorf("queued turn = %+v, %v; want it canceled", j, err)
	}
	if stop, err := f.store.Heartbeat(ctx, f.reg.RunnerID, running.ID, time.Minute); err != nil || !stop {
		t.Errorf("heartbeat cancel = %v, %v; want the running turn told to stop", stop, err)
	}
}

// A chat outlives its deleted task: its turn still runs on the machine, without the task's code.
func TestChatOfADeletedTaskReadsNoCode(t *testing.T) {
	l := &runnerLauncher{task: func(_ context.Context, id string) (*task.Task, error) {
		return nil, apperr.Wrap(task.ErrNotFound, "task.errors.task_ref", "id", id)
	}}
	got, err := l.chatProjects(t.Context(), chatruntime.MachineTurn{TaskID: "gone", RunnerID: "r1"})
	if err != nil || got != nil {
		t.Errorf("chatProjects = %+v, %v; want no code and no error", got, err)
	}
}
