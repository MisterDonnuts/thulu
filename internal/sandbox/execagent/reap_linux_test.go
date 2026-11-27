//go:build linux

package execagent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/procreap"
)

// Same name as sandbox.EnvRunInstance; the sandbox package imports this one.
const runInstanceEnv = "OCTOPUS_RUN_INSTANCE"

// carrying counts the live processes whose environment holds runInstanceEnv=inst.
func carrying(inst string) int {
	want := []byte(runInstanceEnv + "=" + inst)
	n := 0
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		if st, err := os.ReadFile(d + "/stat"); err != nil || bytes.Contains(st, []byte(") Z ")) {
			continue
		}
		env, err := os.ReadFile(d + "/environ")
		if err != nil {
			continue
		}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if bytes.Equal(kv, want) {
				n++
				break
			}
		}
	}
	return n
}

// Two runs exec a detached `sleep 600` each through the exec agent, the way a run's sandbox exec
// reaches a Kubernetes sandbox pod. Reaping run A, with the same script the daemon sends at a run's
// end, kills A's sleep and leaves B's.
func TestReapByRunInstanceThroughTheExecAgent(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("needs /proc")
	}
	s := NewServer("t")
	ctx := context.Background()
	tag := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	a, b := "reap-a-"+tag, "reap-b-"+tag
	reap := func(inst string) ExecResult {
		return s.runCommand(ctx, ExecRequest{
			Command:    []string{"bash", "-lc", procreap.Script()},
			Env:        procreap.Matching(runInstanceEnv, inst),
			InheritEnv: true,
			TimeoutMs:  procreap.TimeoutMs,
		})
	}
	t.Cleanup(func() {
		// Whatever a failing run of this test left, by its marker.
		reap(a)
		reap(b)
	})
	for _, inst := range []string{a, b} {
		res := s.runCommand(ctx, ExecRequest{
			Command:    []string{"bash", "-lc", "setsid sleep 600 >/dev/null 2>&1 &"},
			Env:        map[string]string{runInstanceEnv: inst},
			InheritEnv: true,
			TimeoutMs:  10_000,
		})
		if res.ExitCode != 0 {
			t.Fatalf("exec for %s: %+v", inst, res)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for (carrying(a) == 0 || carrying(b) == 0) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if carrying(a) != 1 || carrying(b) != 1 {
		t.Fatalf("detached sleeps: a=%d b=%d, want one each", carrying(a), carrying(b))
	}

	res := reap(a)
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "reap pid=") {
		t.Fatalf("reap of %s: %+v", a, res)
	}
	if strings.Contains(res.Stdout, "survivor") {
		t.Fatalf("reap left survivors: %s", res.Stdout)
	}
	if n := carrying(a); n != 0 {
		t.Fatalf("%d processes of run A survived its reap", n)
	}
	if n := carrying(b); n != 1 {
		t.Fatalf("run B has %d processes after A's reap, want its sleep", n)
	}
}

// After a daemon restart nobody knows which turn or run was the last: Carrying kills what carries
// the marker under any value, and nothing that lacks it.
func TestReapCarryingKillsEveryValueOfTheMarker(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("needs /proc")
	}
	s := NewServer("t")
	ctx := context.Background()
	tag := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	a, b := "carry-a-"+tag, "carry-b-"+tag
	// The bystander carries a different variable, so it can be found and cleaned up by its own.
	const otherEnv = "OCTOPUS_REAP_TEST_BYSTANDER"
	reap := func(env map[string]string) ExecResult {
		return s.runCommand(ctx, ExecRequest{
			Command: []string{"bash", "-lc", procreap.Script()}, Env: env, InheritEnv: true, TimeoutMs: procreap.TimeoutMs,
		})
	}
	t.Cleanup(func() {
		reap(procreap.Matching(runInstanceEnv, a))
		reap(procreap.Matching(runInstanceEnv, b))
		reap(procreap.Matching(otherEnv, tag))
	})
	start := func(env map[string]string) {
		res := s.runCommand(ctx, ExecRequest{
			Command: []string{"bash", "-lc", "setsid sleep 600 >/dev/null 2>&1 &"}, Env: env, InheritEnv: true, TimeoutMs: 10_000,
		})
		if res.ExitCode != 0 {
			t.Fatalf("exec %v: %+v", env, res)
		}
	}
	start(map[string]string{runInstanceEnv: a})
	start(map[string]string{runInstanceEnv: b})
	start(map[string]string{otherEnv: tag})
	bystander := func() int {
		want := []byte(otherEnv + "=" + tag)
		n := 0
		dirs, _ := filepath.Glob("/proc/[0-9]*")
		for _, d := range dirs {
			if st, err := os.ReadFile(d + "/stat"); err != nil || bytes.Contains(st, []byte(") Z ")) {
				continue
			}
			if env, err := os.ReadFile(d + "/environ"); err == nil && bytes.Contains(env, want) {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(5 * time.Second)
	for (carrying(a) == 0 || carrying(b) == 0 || bystander() == 0) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if carrying(a) != 1 || carrying(b) != 1 || bystander() != 1 {
		t.Fatalf("detached sleeps: a=%d b=%d bystander=%d, want one each", carrying(a), carrying(b), bystander())
	}

	res := reap(procreap.Carrying(runInstanceEnv))
	if res.ExitCode != 0 || strings.Contains(res.Stdout, "survivor") {
		t.Fatalf("carrying reap: %+v", res)
	}
	if carrying(a) != 0 || carrying(b) != 0 {
		t.Fatalf("marked processes survived: a=%d b=%d", carrying(a), carrying(b))
	}
	if n := bystander(); n != 1 {
		t.Fatalf("the unmarked sleep was hit: %d left", n)
	}
}
