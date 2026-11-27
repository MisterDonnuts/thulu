package api

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func (l *runnerLauncher) CheckRunner(ctx context.Context, workspaceID, runnerID string) error {
	_, err := l.liveRunner(ctx, workspaceID, runnerID)
	return err
}

// RunTurn queues one chat turn on the chat's machine and waits for it. Each turn has its own job
// id, which is also its instance: the outputs land in a folder of their own, so a turn that fails
// before writing never hands back the previous answer.
func (l *runnerLauncher) RunTurn(ctx context.Context, turn chatruntime.MachineTurn) (chatruntime.ExecOutput, error) {
	if err := machineEngine(turn.Env); err != nil {
		return chatruntime.ExecOutput{}, err
	}
	r, err := l.liveRunner(ctx, turn.WorkspaceID, turn.RunnerID)
	if err != nil {
		return chatruntime.ExecOutput{}, err
	}
	projects, err := l.chatProjects(ctx, turn)
	if err != nil {
		return chatruntime.ExecOutput{}, err
	}
	id := runner.NewJobID()
	stage := string(types.StageChat)
	job := &runner.Job{
		ID: id, RunnerID: turn.RunnerID, WorkspaceID: turn.WorkspaceID, TaskID: turn.TaskID, InstanceID: id,
		AgentName: turn.AgentName, AgentType: turn.AgentType, Engine: turn.Env["ENGINE"], Stage: stage,
		LaunchKind: runner.LaunchKindChatTurn,
		Spec: runner.JobSpec{
			InstanceID: id, TaskID: turn.TaskID, Stage: stage, AgentName: turn.AgentName,
			LaunchKind: runner.LaunchKindChatTurn, ReadOnly: true, Env: copyEnv(turn.Env), Projects: projects,
			ChatID: turn.ChatID, Stdin: turn.Stdin, DeadlineSeconds: turnDeadlineSeconds(ctx),
		},
	}
	// Registered before the job exists, so settle can never reap it unseen. Buffered: a turn whose
	// waiter gave up still ends here, and its files go with it.
	ended := make(chan chatruntime.ExecOutput, 1)
	l.mu.Lock()
	l.callbacks[id] = func(code int, _, _ string) { ended <- l.collectTurn(job, code) }
	l.mu.Unlock()
	if err := l.enqueue(ctx, r, job); err != nil {
		l.mu.Lock()
		delete(l.callbacks, id)
		l.mu.Unlock()
		return chatruntime.ExecOutput{}, err
	}
	select {
	case out := <-ended:
		return out, nil
	case <-ctx.Done():
		logx.Warn(l.store.RequestCancel(context.WithoutCancel(ctx), id), "api.runners.log.turn_cancel_failed", "job", id)
		return chatruntime.ExecOutput{}, apperr.Unavailable("api.runners.errors.turn_unfinished", "runner", r.Name)
	}
}

// A task chat reads its task's code, read-only: the machine's folders from the machine, Thulu's
// projects through /git/ with the chat's key, which carries the task.
func (l *runnerLauncher) chatProjects(ctx context.Context, turn chatruntime.MachineTurn) ([]runner.JobProject, error) {
	if turn.TaskID == "" || l.task == nil {
		return nil, nil
	}
	t, err := l.task(ctx, turn.TaskID)
	if errors.Is(err, task.ErrNotFound) {
		// A chat outlives its deleted task; without the task it reads no code.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l.runnerFor(ctx, t, turn.RunnerID, true)
}

// collectTurn reads what the machine's engine printed (the turn's outputs) and what its scripts
// logged, the stderr of a container exec, then drops both: the chat keeps the answer, not the files.
func (l *runnerLauncher) collectTurn(j *runner.Job, code int) chatruntime.ExecOutput {
	out := chatruntime.ExecOutput{ExitCode: code}
	out.Stderr, _ = readTurnFile(l.logPath(j))
	if dir := runOutputDir(l.runsDir, j.InstanceID); dir != "" {
		var arrived bool
		out.Stdout, arrived = readTurnFile(filepath.Join(dir, runner.ChatTurnOutput))
		// The machine reports its engine's exit even when the outputs never got here. Taken as a
		// success, the turn would answer nothing and lose the engine's session.
		if code == 0 && !arrived {
			out.ExitCode = runner.ExitLost
			out.Stderr = append(out.Stderr, i18n.Log("api.runners.log.turn_output_missing")+"\n"...)
		}
	}
	l.dropTurn(j)
	return out
}

// readTurnFile also says whether the file was there.
func readTurnFile(path string) ([]byte, bool) {
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logx.Warn(err, "api.runners.log.turn_read_failed", "path", path)
	}
	return b, err == nil
}

func (l *runnerLauncher) dropTurn(j *runner.Job) {
	if dir := runOutputDir(l.runsDir, j.InstanceID); dir != "" {
		logx.Warn(os.RemoveAll(dir), "api.runners.log.turn_cleanup_failed", "path", dir)
	}
	if p := l.logPath(j); p != "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logx.Warn(err, "api.runners.log.turn_cleanup_failed", "path", p)
		}
	}
}

// turnDeadlineSeconds is what is left of the turn's timeout, rounded up, for the machine to stop the
// turn on its own when this daemon's cancel never reaches it; 0 when ctx has no deadline.
func turnDeadlineSeconds(ctx context.Context) int {
	dl, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	return max(int((time.Until(dl)+time.Second-1)/time.Second), 1)
}
