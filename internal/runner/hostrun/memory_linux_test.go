package hostrun

import (
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// In a systemd scope the kernel holds the cap: it kills the process that grows, and the runner then
// stops the whole run and says why.
func TestMemoryLimitKillsRunInScope(t *testing.T) {
	sup := probeScopes()
	if !sup.ok {
		t.Skip(apperr.Text(sup.reason))
	}
	f := newKillFixture(t)
	fastMemoryChecks(t)
	f.r.scope = sup
	j := f.job(allocEscapee)
	j.memLimit = 128
	res := f.execute(context.Background(), j, nil)
	if res.StopReason != runner.StopMemoryLimit || res.ExitCode != memoryExitCode {
		t.Errorf("finish = %+v, want exit %d with stop reason %q", res, memoryExitCode, runner.StopMemoryLimit)
	}
	if !strings.Contains(res.Error, "memory limit (128 MB)") {
		t.Errorf("error = %q, want it to name the 128 MB limit", res.Error)
	}
}
