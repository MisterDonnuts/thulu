package api

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestFiresTaskFailedOnlyOnFailedRuns(t *testing.T) {
	if !firesTaskFailed(task.StatusFailed) {
		t.Fatal("failed must fire task.failed")
	}
	for _, s := range []task.RunStatus{
		task.StatusNotStarted, task.StatusNoAgent, task.StatusCredentialBlocked,
		task.StatusResumeFailed, task.StatusBlocked, task.StatusStopped, task.StatusQueued,
	} {
		if firesTaskFailed(s) {
			t.Errorf("%s must not fire task.failed", s)
		}
	}
}
