package agentscripts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pipeRun sources engine-pipe.sh and runs engine through _thulu_engine_pipe, as the entrypoint
// does. It answers the combined output, the exit code and how long the call took.
func pipeRun(t *testing.T, engine string) (string, int, time.Duration, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "session.log")
	script := `source "$1"; _thulu_engine_pipe "$2" bash -c "$3"; echo "code=$?"`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bashOrSkip(t), "-c", script, "pipe",
		filepath.Join(snippetsDir(t), "engine-pipe.sh"), log, engine)
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	start := time.Now()
	out, err := cmd.CombinedOutput()
	took := time.Since(start)
	if err != nil {
		t.Fatalf("pipe: %v\n%s", err, out)
	}
	code := -1
	if i := strings.LastIndex(string(out), "code="); i >= 0 {
		_, _ = fmt.Sscanf(string(out[i:]), "code=%d", &code)
	}
	b, _ := os.ReadFile(log)
	return string(out), code, took, string(b)
}

// uniqueSleep is a sleep command line no other process on the machine runs, so the test can find
// and kill what it left even when it fails.
func uniqueSleep(t *testing.T) string {
	t.Helper()
	line := fmt.Sprintf("sleep %d", 700000+time.Now().UnixNano()%100000)
	t.Cleanup(func() { _ = exec.Command("pkill", "-f", "^"+line+"$").Run() })
	return line
}

func running(line string) bool {
	return exec.Command("pgrep", "-f", "^"+line+"$").Run() == nil
}

// K8S-7: a fake engine leaves `sleep` running in the background with the pipe as its stdout, then
// exits. With `engine | tee` the entrypoint waited for that sleep, and the pod ran until its
// deadline. Now the call returns at once with the engine's code, the output is in the log, and the
// sleep is gone.
func TestEnginePipeReturnsWhenTheEngineExitsAndKillsWhatItLeft(t *testing.T) {
	sleep := uniqueSleep(t)
	out, code, took, log := pipeRun(t, "echo before; "+sleep+" & echo after; exit 3")
	if took > 5*time.Second {
		t.Fatalf("took %s: the pipe waited for the engine's background child", took)
	}
	if code != 3 {
		t.Fatalf("exit code %d, want the engine's 3\n%s", code, out)
	}
	if !strings.Contains(log, "before") || !strings.Contains(log, "after") {
		t.Fatalf("session.log %q lacks the engine's output", log)
	}
	if !strings.Contains(out, "before") {
		t.Fatalf("stdout %q lacks the engine's output", out)
	}
	deadline := time.Now().Add(2 * time.Second)
	for running(sleep) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if running(sleep) {
		t.Fatal("the engine's background child survived the engine")
	}
}

// A child that left the engine's process group (its own session) is out of reach of the group
// kill, but it no longer holds the run: the copy stops after its grace.
func TestEnginePipeDoesNotWaitForAChildThatLeftTheGroup(t *testing.T) {
	sleep := uniqueSleep(t)
	detach := "perl -e 'use POSIX; POSIX::setsid(); exec @ARGV' --"
	if _, err := exec.LookPath("setsid"); err == nil {
		detach = "setsid"
	} else if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("needs setsid or perl")
	}
	_, code, took, log := pipeRun(t, "echo before; "+detach+" "+sleep+" & sleep 0.3; exit 0")
	if took > 10*time.Second {
		t.Fatalf("took %s: the pipe waited for a detached child", took)
	}
	if code != 0 || !strings.Contains(log, "before") {
		t.Fatalf("code %d, log %q", code, log)
	}
}

func TestEntrypointRunsTheEngineThroughThePipe(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(snippetsDir(t)), "entrypoint-base.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `_thulu_engine_pipe "$OUTPUT_DIR/session.log"`) {
		t.Fatal("entrypoint-base.sh does not run the engine through _thulu_engine_pipe")
	}
	if strings.Contains(src, `| tee -a "$OUTPUT_DIR/session.log"`) {
		t.Fatal("entrypoint-base.sh still pipes the engine into tee")
	}
}
