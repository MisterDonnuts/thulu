package api

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/usage"
)

func TestLiveRunDropsARunThatAlreadyFinished(t *testing.T) {
	t.Parallel()
	done := []usage.Session{{ID: "r1", FinishedAt: time.Now()}}
	if got := liveRun("r1", done); got != "" {
		t.Errorf("liveRun(finished) = %q, want none: the task waits on a person", got)
	}
	if got := liveRun("r2", done); got != "r2" {
		t.Errorf("liveRun(no row yet) = %q, want r2: a running run has no usage row", got)
	}
	if got := liveRun("", done); got != "" {
		t.Errorf("liveRun(no current) = %q, want none", got)
	}
}
