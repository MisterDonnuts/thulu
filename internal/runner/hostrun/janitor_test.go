//go:build unix

package hostrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

// setsidEscapee is the shell lines of a process that leaves its run on its own: a double fork into
// a session of its own, the middle process gone at once. $H is the helper and $N its name.
const setsidEscapee = `if command -v perl >/dev/null 2>&1; then
  THULU_TEST_HELPER=sleep perl -e 'use POSIX; fork and exit; POSIX::setsid(); fork and exit; exec @ARGV' "$H" "$N" >/dev/null 2>&1
else
  THULU_TEST_HELPER=sleep python3 -c 'import os, sys
if os.fork(): sys.exit(0)
os.setsid()
if os.fork(): sys.exit(0)
os.execv(sys.argv[1], sys.argv[1:])' "$H" "$N" >/dev/null 2>&1
fi
`

// procsNamed returns the live processes whose command line holds name.
func procsNamed(t *testing.T, name string) []int {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,command=").Output()
	if err != nil {
		t.Logf("ps: %v", err)
	}
	var pids []int
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.Contains(line, name) {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid != os.Getpid() {
			if p, ok := lookupProc(pid); ok && !p.Zombie {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func waitNamed(t *testing.T, name string) []int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if pids := procsNamed(t, name); len(pids) > 0 {
			return pids
		}
	}
	t.Fatalf("no process named %s started", name)
	return nil
}

// leftAlive polls for up to d until no marked process is left but the ones in except, and returns
// the ones still running.
func leftAlive(t *testing.T, mark string, d time.Duration, except ...int) []int {
	t.Helper()
	var left []int
	for deadline := time.Now().Add(d); ; time.Sleep(100 * time.Millisecond) {
		left = left[:0]
		for _, pid := range markedProcs(t, mark) {
			if !slices.Contains(except, pid) {
				left = append(left, pid)
			}
		}
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
	}
}

func alive(pid int) bool {
	p, ok := lookupProc(pid)
	return ok && !p.Zombie
}

// testEnv is this process's environment without what would tie a child to a real run, plus extra.
func testEnv(extra map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "THULU_TEST_") || k == runner.RunIDEnv || k == "OCTOPUS_ROOT" || extra[k] != "" {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// startEscapee leaves a helper named name running outside any process group of the test, with env.
func startEscapee(t *testing.T, env map[string]string, name string) int {
	t.Helper()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env["H"], env["N"] = helper, name
	cmd := exec.Command("sh", "-c", setsidEscapee)
	cmd.Env = testEnv(env)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start %s: %v: %s", name, err, out)
	}
	return waitNamed(t, name)[0]
}

func newMark(prefix string) string {
	return fmt.Sprintf("%s%d_%d", prefix, os.Getpid(), time.Now().UnixNano())
}

// ---- a whole runner in a process of its own -------------------------------------------------------

type runnerProc struct {
	t        *testing.T
	mark     string
	helper   string
	work     string
	bin      string
	spec     runner.JobSpec
	url      string
	finished chan runner.FinishRequest
}

// newRunnerProc serves one job to whichever runner claims first, from a fake daemon.
func newRunnerProc(t *testing.T) *runnerProc {
	t.Helper()
	if !procTableSupported {
		t.Skip("no process table on this system")
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &runnerProc{t: t, mark: newMark("r"), helper: helper, work: filepath.Join(t.TempDir(), "work"),
		bin: t.TempDir(), finished: make(chan runner.FinishRequest, 1)}
	t.Cleanup(func() { killMarked(t, f.mark) })
	shortTermWait(t, time.Second)
	writeFile(t, filepath.Join(f.bin, "claude"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(f.bin, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.spec = runner.JobSpec{ID: "j" + f.mark, InstanceID: "i1",
		Env: map[string]string{"THULU_TEST_MARK": f.mark, "HELPER": helper}}
	var given atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runner/v1/claim", func(w http.ResponseWriter, r *http.Request) {
		var req runner.ClaimRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if slices.Contains(req.Kinds, runner.ClaimRun) && given.CompareAndSwap(false, true) {
			_ = json.NewEncoder(w).Encode(runner.ClaimResponse{Job: &f.spec, LeaseSeconds: 60, HeartbeatSeconds: 1})
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
		_ = json.NewEncoder(w).Encode(runner.ClaimResponse{})
	})
	mux.HandleFunc("POST /runner/v1/jobs/{id}/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(runner.HeartbeatResponse{LeaseSeconds: 60})
	})
	for _, route := range []string{"POST /runner/v1/jobs/{id}/log", "PUT /runner/v1/jobs/{id}/outputs", "PUT /runner/v1/inventory"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("POST /runner/v1/jobs/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var res runner.FinishRequest
		_ = json.NewDecoder(r.Body).Decode(&res)
		select {
		case f.finished <- res:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

type startedRunner struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

// start runs `thulu runner run` with script as every run's entrypoint. Whatever the test does, the
// process is gone when it ends.
func (f *runnerProc) start(script string) *startedRunner {
	f.t.Helper()
	entry := filepath.Join(f.t.TempDir(), "agent.sh")
	writeFile(f.t, entry, script)
	logPath := filepath.Join(f.t.TempDir(), "runner.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(f.helper)
	cmd.Env = testEnv(map[string]string{
		helperEnv: helperRunner, "THULU_TEST_DAEMON": f.url, "THULU_TEST_WORK": f.work, "THULU_TEST_ENTRY": entry,
		"THULU_TEST_MARK": f.mark, "CLAUDE_CONFIG_DIR": f.t.TempDir(),
		"PATH": f.bin + string(os.PathListSeparator) + "/usr/bin:/bin:/usr/sbin:/sbin",
	})
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	s := &startedRunner{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(s.exited)
	}()
	f.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-s.exited:
		case <-time.After(5 * time.Second):
		}
		if f.t.Failed() {
			b, _ := os.ReadFile(logPath)
			f.t.Logf("runner log:\n%s", b)
		}
	})
	return s
}

func (f *runnerProc) root() string { return filepath.Join(f.work, "runs", f.spec.ID) }

func (f *runnerProc) record() string { return filepath.Join(f.work, activeDir, f.spec.ID+".json") }

func (f *runnerProc) waitStarted() {
	f.t.Helper()
	started := filepath.Join(f.root(), "tmp", "started")
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			return
		}
	}
	f.t.Fatal("the run never started")
}

// waitRecorded waits until the run's record names pid.
func (f *runnerProc) waitRecorded(pid int) {
	f.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if rec, err := readRecord(f.record()); err == nil {
			for _, m := range rec.Members {
				if m.PID == pid {
					return
				}
			}
		}
	}
	f.t.Fatalf("the run's record never named pid %d", pid)
}

func (f *runnerProc) assertNothingLeft(except ...int) {
	f.t.Helper()
	if left := leftAlive(f.t, f.mark, 3*time.Second, except...); len(left) > 0 {
		f.t.Errorf("processes of the run are still running: %v", left)
	}
	if _, err := os.Stat(f.root()); !os.IsNotExist(err) {
		f.t.Errorf("the run's folder %s is still there: %v", f.root(), err)
	}
	if _, err := os.Stat(f.record()); !os.IsNotExist(err) {
		f.t.Errorf("the run's record %s is still there: %v", f.record(), err)
	}
}

// The incident: the runner dies with no cleanup at all while a run's server runs outside its process
// group. The next start finds the server by the run's record and its marker, kills it, and only then
// removes the folder and the record.
func TestCrashedRunnerLeavesNothingAfterTheNextStart(t *testing.T) {
	const script = `H="$HELPER"; M="$THULU_TEST_MARK"; N="helper-$M-server"
` + setsidEscapee + `touch "$OCTOPUS_ROOT/tmp/started"
THULU_TEST_HELPER=sleep "$H" "helper-$M-wait"
`
	for _, folderGone := range []bool{false, true} {
		name := "folder kept"
		if folderGone {
			name = "only the record and the marker left"
		}
		t.Run(name, func(t *testing.T) {
			f := newRunnerProc(t)
			run := f.start(script)
			f.waitStarted()
			server := waitNamed(t, "helper-"+f.mark+"-server")[0]
			f.waitRecorded(server)

			// SIGKILL: no handler, no cleanup, the run's processes keep running.
			if err := run.cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			<-run.exited
			if !alive(server) {
				t.Fatal("the server died with the runner; the test proves nothing")
			}
			if folderGone {
				if err := os.RemoveAll(f.root()); err != nil {
					t.Fatal(err)
				}
			}

			(&Runner{cfg: &Config{WorkDir: f.work}}).sweepAll(context.Background())

			if alive(server) {
				t.Errorf("the server %d outlived the next start of the runner", server)
			}
			f.assertNothingLeft(run.cmd.Process.Pid)
		})
	}
}

// SIGHUP, as when the terminal running `thulu runner run` closes, stops the runner the clean way: the
// run is canceled, everything it started is killed, and the daemon hears how it ended.
func TestSIGHUPStopsTheRunnerAndItsRuns(t *testing.T) {
	f := newRunnerProc(t)
	run := f.start(escapees + `touch "$OCTOPUS_ROOT/tmp/started"
THULU_TEST_HELPER=sleep "$HELPER" "helper-$THULU_TEST_MARK-wait"
`)
	f.waitStarted()
	waitNamed(t, "helper-"+f.mark+"-b")
	if err := run.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.exited:
	case <-time.After(40 * time.Second):
		t.Fatal("the runner did not stop within 40 s of SIGHUP")
	}
	if code := run.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("the runner exited %d, want 0", code)
	}
	select {
	case res := <-f.finished:
		if res.ExitCode != runner.ExitCanceled {
			t.Errorf("finish = %+v, want the run canceled", res)
		}
	default:
		t.Error("the run was never reported")
	}
	f.assertNothingLeft(run.cmd.Process.Pid)
}

func TestSignalsIncludeSIGHUP(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(StopSignals, sig) {
			t.Errorf("StopSignals %v lack %v", StopSignals, sig)
		}
	}
}

// A chat's turn died with the runner: its process group, named by the pid file, and a server it
// double-forked are killed at the next start. The chat keeps its folder for its next turn.
func TestSweepKillsChatLeftovers(t *testing.T) {
	if !procTableSupported {
		t.Skip("no process table on this system")
	}
	shortTermWait(t, time.Second)
	mark := newMark("c")
	t.Cleanup(func() { killMarked(t, mark) })
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	root := filepath.Join(work, "chats", "chat1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "entry.sh")
	writeFile(t, script, `H="$HELPER"; M="$THULU_TEST_MARK"; N="helper-$M-server"
sleep 600 &
echo $! > "$OCTOPUS_ROOT/sleep.pid"
`+setsidEscapee+`THULU_TEST_HELPER=sleep "$H" "helper-$M-wait" &
touch "$OCTOPUS_ROOT/started"
wait
`)
	cmd := exec.Command("bash", script)
	setProcessGroup(cmd)
	cmd.Env = testEnv(map[string]string{"HELPER": helper, "THULU_TEST_MARK": mark, "OCTOPUS_ROOT": root,
		runner.RunIDEnv: "turn" + mark})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	writeFile(t, filepath.Join(root, pidFile), strconv.Itoa(cmd.Process.Pid))
	server := waitNamed(t, "helper-"+mark+"-server")[0]
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never started")
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "sleep.pid"))
	if err != nil {
		t.Fatal(err)
	}
	sleeper, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}

	(&Runner{cfg: &Config{WorkDir: work}}).sweepAll(context.Background())

	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Error("the turn's script is still running")
	}
	for _, pid := range []int{server, sleeper} {
		if alive(pid) {
			t.Errorf("pid %d of the turn outlived the sweep", pid)
		}
	}
	if left := leftAlive(t, mark, 2*time.Second); len(left) > 0 {
		t.Errorf("processes of the turn are still running: %v", left)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("the chat lost its folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, pidFile)); !os.IsNotExist(err) {
		t.Errorf("the turn's pid file is still there: %v", err)
	}
}

// Within one tick the janitor kills a process of a job that is no longer running, and leaves alone a
// running job's process and a process of another runner's work dir.
func TestJanitorKillsProcessOfFinishedJob(t *testing.T) {
	if !procTableSupported {
		t.Skip("no process table on this system")
	}
	shortTermWait(t, time.Second)
	mark := newMark("t")
	t.Cleanup(func() { killMarked(t, mark) })
	work := t.TempDir()
	r := &Runner{cfg: &Config{WorkDir: work}}
	r.addJob(&job{spec: &runner.JobSpec{ID: "live" + mark}})
	start := func(job, root, name string) int {
		return startEscapee(t, map[string]string{"THULU_TEST_MARK": mark, runner.RunIDEnv: job, "OCTOPUS_ROOT": root},
			"helper-"+mark+"-"+name)
	}
	done := start("done"+mark, filepath.Join(work, "runs", "done"+mark), "done")
	live := start("live"+mark, filepath.Join(work, "runs", "live"+mark), "live")
	other := start("other"+mark, filepath.Join(t.TempDir(), "runs", "other"+mark), "other")

	const tick = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		r.janitor(ctx, tick)
		close(stopped)
	}()
	defer func() {
		cancel()
		<-stopped
	}()
	// One tick, plus the SIGTERM the helper obeys at once.
	gone := false
	for deadline := time.Now().Add(tick + 2*time.Second); time.Now().Before(deadline) && !gone; time.Sleep(50 * time.Millisecond) {
		gone = !alive(done)
	}
	if !gone {
		t.Errorf("the process %d of a finished job outlived a janitor tick", done)
	}
	time.Sleep(2 * tick)
	if !alive(live) {
		t.Errorf("the janitor killed %d, a process of a running job", live)
	}
	if !alive(other) {
		t.Errorf("the janitor killed %d, a process of another runner's run", other)
	}
}

// The runner stops a run that goes over the limit the daemon sent, on its own, and says why.
func TestLocalDeadlineEndsRun(t *testing.T) {
	f := newKillFixture(t)
	old := deadlineMargin
	deadlineMargin = 0
	t.Cleanup(func() { deadlineMargin = old })
	j := f.job(escapees + "touch \"$OCTOPUS_ROOT/tmp/started\"\nsleep 30\n")
	j.spec.DeadlineSeconds = 2
	res := f.execute(context.Background(), j, nil)
	if res.ExitCode != runner.ExitCanceled || res.StopReason != runner.StopDeadline {
		t.Errorf("finish = %+v, want canceled with stop reason %q", res, runner.StopDeadline)
	}
}

// A job that carries no limit (a chat turn from an older daemon, a workspace without one) still has
// one on the machine.
func TestRunWithoutLimitGetsTheFallbackDeadline(t *testing.T) {
	f := newKillFixture(t)
	old := fallbackDeadline
	fallbackDeadline = 2 * time.Second
	t.Cleanup(func() { fallbackDeadline = old })
	j := f.job(escapees + "touch \"$OCTOPUS_ROOT/tmp/started\"\nsleep 30\n")
	j.spec.DeadlineSeconds = 0
	res := f.execute(context.Background(), j, nil)
	if res.ExitCode != runner.ExitCanceled || res.StopReason != runner.StopDeadline {
		t.Errorf("finish = %+v, want canceled with stop reason %q", res, runner.StopDeadline)
	}
}

// A daemon that answers no heartbeat for longer than the lease and the restart grace (an error, or
// no answer at all) gets its run stopped by the runner.
func TestDaemonLossEndsRun(t *testing.T) {
	old := daemonRestartGrace
	daemonRestartGrace = 0
	t.Cleanup(func() { daemonRestartGrace = old })
	for _, mode := range []string{"fail", "reset"} {
		t.Run(mode, func(t *testing.T) {
			f := newKillFixture(t)
			f.beat.Store(mode)
			j := f.job(escapees + "touch \"$OCTOPUS_ROOT/tmp/started\"\nsleep 30\n")
			j.lease = time.Second
			res := f.execute(context.Background(), j, nil)
			if res.ExitCode != runner.ExitCanceled || res.StopReason != runner.StopDaemonLost {
				t.Errorf("finish = %+v, want canceled with stop reason %q", res, runner.StopDaemonLost)
			}
		})
	}
}

// A daemon restart slower than the lease must not cost the run: the new daemon adopts the job with a
// fresh lease (runner.Store.AdoptRunning), so the runner keeps it through the restart grace.
func TestDaemonSilentWithinRestartGraceKeepsRun(t *testing.T) {
	old := daemonRestartGrace
	daemonRestartGrace = time.Minute
	t.Cleanup(func() { daemonRestartGrace = old })
	f := newKillFixture(t)
	f.beat.Store("reset")
	j := f.job("sleep 3\nexit 0\n")
	j.lease = time.Second
	res := f.execute(context.Background(), j, nil)
	if res.ExitCode != 0 || res.StopReason != "" {
		t.Errorf("finish = %+v, want the run to end on its own with exit 0", res)
	}
}
