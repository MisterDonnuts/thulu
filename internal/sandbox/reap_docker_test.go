package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Two runs leave a detached sleep each in the same sandbox. Reaping run A kills only A's; reaping
// all leaves PID 1 and the main command. Needs Docker and the runtime image; skips without them.
func TestReapRunKillsOnlyThatRunsLeftovers(t *testing.T) {
	rig := newE2ERig(t, 2, 200, 3600)
	ctx := context.Background()
	const owner = "task-reap"
	t.Cleanup(func() {
		// Removing the container kills whatever a failing run of this test left in it.
		_ = rig.mgr.Destroy(context.Background(), owner)
	})
	leave := func(inst string) {
		t.Helper()
		_, _, err := rig.mgr.Exec(ctx, owner, "in_progress", AcquireDockerOpts{}, ExecOpts{
			Command:   "setsid nohup sleep 600 >/dev/null 2>&1 &",
			Env:       map[string]string{EnvRunInstance: inst},
			TimeoutMs: 10_000,
		})
		if err != nil {
			t.Fatalf("exec for %s: %v", inst, err)
		}
	}
	leave("run-a")
	leave("run-b")
	count := func(inst string) int {
		t.Helper()
		res, _, err := rig.mgr.Exec(ctx, owner, "in_progress", AcquireDockerOpts{}, ExecOpts{
			Command:   `n=0; for e in /proc/[0-9]*/environ; do tr '\0' '\n' < "$e" 2>/dev/null | grep -qx "` + EnvRunInstance + `=$WANT" && n=$((n+1)); done; echo $n`,
			Env:       map[string]string{"WANT": inst},
			TimeoutMs: 10_000,
		})
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for _, c := range strings.TrimSpace(res.Stdout) {
			n = n*10 + int(c-'0')
		}
		return n
	}
	time.Sleep(300 * time.Millisecond)
	if count("run-a") == 0 || count("run-b") == 0 {
		t.Fatal("the detached sleeps did not start")
	}

	if err := rig.mgr.ReapRun(ctx, owner, "run-a"); err != nil {
		t.Fatal(err)
	}
	if n := count("run-a"); n != 0 {
		t.Fatalf("%d processes of run-a survived its reap", n)
	}
	if count("run-b") == 0 {
		t.Fatal("reaping run-a killed run-b's process")
	}

	if err := rig.mgr.ReapAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	res, _, err := rig.mgr.Exec(ctx, owner, "in_progress", AcquireDockerOpts{}, ExecOpts{
		Command: `for p in /proc/[0-9]*; do [ "${p#/proc/}" = "$$" ] && continue; tr '\0' ' ' < "$p/cmdline" 2>/dev/null; echo; done | grep -v '^$' | grep -v 'for p in'`, TimeoutMs: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "sleep 600") {
		t.Fatalf("ReapAll left a run's process:\n%s", res.Stdout)
	}
}
