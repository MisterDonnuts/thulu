package daemon

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"
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
