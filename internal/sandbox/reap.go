package sandbox

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/procreap"
)

// EnvRunInstance names the run an exec belongs to. The daemon sets it on every exec a run's session
// sends, so whatever the command leaves behind, detached or not, still carries it in its
// environment.
const EnvRunInstance = "OCTOPUS_RUN_INSTANCE"

// EnvRunTask names the task of the run an exec belongs to. A subtask's runs share its parent's
// sandbox, which outlives the subtask: deleting the subtask kills what its runs left there by it.
const EnvRunTask = "OCTOPUS_RUN_TASK"

// init_run groups outlive the run that started them on purpose: their pid files spare them.
func initPidGlob() string { return initRoot + "/*/pid" }

// ReapRun kills what the run left in the owner's sandbox. A sandbox that is not running has nothing
// left to kill, and a run without an instance has nothing to match.
func (m *Manager) ReapRun(ctx context.Context, ownerID, instanceID string) error {
	if instanceID == "" {
		return nil
	}
	return m.reap(ctx, ownerID, instanceID, procreap.WithKeep(procreap.Matching(EnvRunInstance, instanceID), initPidGlob()))
}

// ReapTask kills what any run of taskID left in the owner's sandbox. Deleting a subtask calls it on
// its parent's sandbox: the exit of a run whose task is gone finds no row to reach the sandbox by.
func (m *Manager) ReapTask(ctx context.Context, ownerID, taskID string) error {
	if taskID == "" {
		return nil
	}
	return m.reap(ctx, ownerID, "task:"+taskID, procreap.WithKeep(procreap.Matching(EnvRunTask, taskID), initPidGlob()))
}

// ReapAll kills every process in the owner's sandbox outside init_run. Call it only when no run of
// the sandbox's group is alive.
func (m *Manager) ReapAll(ctx context.Context, ownerID string) error {
	return m.reap(ctx, ownerID, "", procreap.All(initPidGlob()))
}

func (m *Manager) reap(ctx context.Context, ownerID, instanceID string, env map[string]string) error {
	sb, err := m.store.Get(ctx, ownerID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sb.ContainerID == "" || sb.Status != StatusReady || !m.docker.Alive(ctx, sb.ContainerID) {
		return nil
	}
	res, err := m.docker.Exec(ctx, sb.ContainerID, ExecOpts{
		Command: procreap.Script(), Env: env, TimeoutMs: procreap.TimeoutMs, Cwd: "/",
	})
	if err != nil {
		return apperr.Wrap(err, "sandbox.errors.reap_exec", "container", sb.ContainerID)
	}
	scope := instanceID
	if scope == "" {
		scope = "*"
	}
	for _, l := range procreap.Parse(res.Stdout) {
		key := "sandbox.log.reaped_process"
		if l.Survivor {
			key = "sandbox.log.reap_survivor"
		}
		log.Printf("[sandbox] %s", i18n.Log(key, "owner", ownerID, "run", scope, "container", sb.ContainerID, "process", l.Process))
	}
	if res.TimedOut || res.ExitCode != 0 {
		return apperr.New("sandbox.errors.reap_failed", "container", sb.ContainerID, "code", res.ExitCode, "stderr", strings.TrimSpace(res.Stderr))
	}
	return nil
}
