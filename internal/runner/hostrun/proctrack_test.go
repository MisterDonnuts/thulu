//go:build unix

package hostrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

// ---- a fake process table -------------------------------------------------------------------------

type sentSignal struct {
	pid int
	sig syscall.Signal
}

type fakeTable struct {
	mu   sync.Mutex
	proc map[int]procInfo
	env  map[int][]string
	// Processes that live through SIGTERM, and through everything.
	deaf, immortal map[int]bool
	sent           []sentSignal
}

func newFakeTable(procs ...procInfo) *fakeTable {
	f := &fakeTable{proc: map[int]procInfo{}, env: map[int][]string{}, deaf: map[int]bool{}, immortal: map[int]bool{}}
	for _, p := range procs {
		f.proc[p.PID] = p
	}
	return f
}

func (f *fakeTable) set(p procInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proc[p.PID] = p
}

func (f *fakeTable) drop(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.proc, pid)
}

func (f *fakeTable) procOS() procOS {
	return procOS{
		list: func() ([]procInfo, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := make([]procInfo, 0, len(f.proc))
			for _, p := range f.proc {
				out = append(out, p)
			}
			return out, nil
		},
		lookup: func(pid int) (procInfo, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			p, ok := f.proc[pid]
			return p, ok
		},
		env: func(pid int) ([]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.env[pid], nil
		},
		signal: func(pid int, sig syscall.Signal) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.sent = append(f.sent, sentSignal{pid, sig})
			var targets []int
			if pid < 0 {
				for _, p := range f.proc {
					if p.PGID == -pid {
						targets = append(targets, p.PID)
					}
				}
			} else {
				targets = []int{pid}
			}
			for _, t := range targets {
				switch {
				case f.immortal[t]:
				case sig == syscall.SIGKILL, sig == syscall.SIGTERM && !f.deaf[t]:
					delete(f.proc, t)
				}
			}
			return nil
		},
		details: func(context.Context) (map[int]procDetail, map[int][]string) {
			return map[int]procDetail{}, map[int][]string{}
		},
	}
}

func (f *fakeTable) signalled(pid int) []syscall.Signal {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []syscall.Signal
	for _, s := range f.sent {
		if s.pid == pid || s.pid == -pid {
			out = append(out, s.sig)
		}
	}
	return out
}

func memberPIDs(t *runTracker) map[int]bool {
	m := map[int]bool{}
	for _, p := range t.Members() {
		m[p.PID] = true
	}
	return m
}

func shortTermWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := termWait
	termWait = d
	t.Cleanup(func() { termWait = old })
}

// The set follows the tree, then groups and sessions a member leads, and keeps a member its parent
// left behind; nothing older than the run and nothing outside it joins.
func TestTrackMembershipFollowsTheTreeGroupsAndMarkers(t *testing.T) {
	self := os.Getpid()
	f := newFakeTable(
		procInfo{PID: 100, PPID: self, PGID: 100, Start: 1000, Comm: "bash"},
		procInfo{PID: 101, PPID: 100, PGID: 100, Start: 1001, Comm: "claude"},
		// Claude Code's Bash tool: a session of its own.
		procInfo{PID: 102, PPID: 101, PGID: 102, SID: 102, Start: 1002, Comm: "bash"},
		procInfo{PID: 103, PPID: 102, PGID: 102, SID: 102, Start: 1003, Comm: "node"},
		procInfo{PID: 200, PPID: 1, PGID: 200, Start: 1005, Comm: "unrelated"},
		procInfo{PID: 201, PPID: 1, PGID: 102, Start: 999, Comm: "older"},
		procInfo{PID: self, PPID: 100, PGID: 100, Start: 1006, Comm: "runner"},
	)
	tr := newRunTracker(f.procOS(), "job1", "run", "/runs/job1", "", 100)
	tr.refresh(false)
	if got, want := memberPIDs(tr), map[int]bool{100: true, 101: true, 102: true, 103: true}; !equalSets(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}

	// The tool's shell exits: node goes to launchd and stays a member; a process node's group still
	// holds joins although its leader is gone.
	f.drop(102)
	f.set(procInfo{PID: 103, PPID: 1, PGID: 102, SID: 102, Start: 1003, Comm: "node"})
	f.set(procInfo{PID: 104, PPID: 1, PGID: 102, SID: 102, Start: 1010, Comm: "esbuild"})
	// A double fork no poll saw: only its environment names the run.
	f.set(procInfo{PID: 105, PPID: 1, PGID: 105, Start: 1011, Comm: "ng"})
	f.env[105] = []string{"PATH=/bin", runner.RunIDEnv + "=job1"}
	f.set(procInfo{PID: 106, PPID: 1, PGID: 106, Start: 1012, Comm: "other-run"})
	f.env[106] = []string{runner.RunIDEnv + "=job2"}
	tr.refresh(false)
	got := memberPIDs(tr)
	for _, pid := range []int{103, 104, 105} {
		if !got[pid] {
			t.Errorf("pid %d is not a member: %v", pid, got)
		}
	}
	for _, pid := range []int{106, 200, 201, self} {
		if got[pid] {
			t.Errorf("pid %d joined the run: %v", pid, got)
		}
	}
}

// The session leader of a `nohup cmd &` exits before any read sees it alive; while its parent has not
// reaped it, the zombie still ties the group it left to the run, and keeps doing so once reaped.
func TestTrackZombieLeaderTiesItsGroupToTheRun(t *testing.T) {
	self := os.Getpid()
	f := newFakeTable(
		procInfo{PID: 100, PPID: self, PGID: 100, Start: 1000, Comm: "bash"},
		procInfo{PID: 101, PPID: 100, PGID: 100, Start: 1001, Comm: "claude"},
		procInfo{PID: 102, PPID: 101, PGID: 102, Start: 1002, Comm: "sh", Zombie: true},
		// A platform binary: macOS hides its environment, so no marker can find it.
		procInfo{PID: 103, PPID: 1, PGID: 102, Start: 1003, Comm: "sh"},
		// Some other program's zombie in a group of its own.
		procInfo{PID: 300, PPID: 1, PGID: 300, Start: 1004, Comm: "other", Zombie: true},
		procInfo{PID: 301, PPID: 1, PGID: 300, Start: 1005, Comm: "other-child"},
	)
	tr := newRunTracker(f.procOS(), "job1", "run", "/runs/job1", "", 100)
	tr.refresh(false)
	if got, want := memberPIDs(tr), map[int]bool{100: true, 101: true, 103: true}; !equalSets(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	f.drop(102)
	f.set(procInfo{PID: 104, PPID: 103, PGID: 102, Start: 1010, Comm: "sleep"})
	tr.refresh(false)
	if got, want := memberPIDs(tr), map[int]bool{100: true, 101: true, 103: true, 104: true}; !equalSets(got, want) {
		t.Errorf("after the reap: members = %v, want %v", got, want)
	}
}

// A member's pid that comes back for another process is never signalled, nor are the runner and
// pid 1.
func TestKillNeverSignalsAReusedPidOrTheRunner(t *testing.T) {
	shortTermWait(t, 50*time.Millisecond)
	self := os.Getpid()
	f := newFakeTable(
		procInfo{PID: 100, PPID: self, PGID: 100, Start: 1000, Comm: "bash"},
		procInfo{PID: 101, PPID: 100, PGID: 100, Start: 1001, Comm: "server"},
		procInfo{PID: 102, PPID: 100, PGID: 100, Start: 1002, Comm: "deaf"},
	)
	f.deaf[102] = true
	tr := newRunTracker(f.procOS(), "job1", "run", "/runs/job1", "", 100)
	tr.refresh(false)
	// bash is reaped; 101 exits and its pid goes to a process of someone else's.
	f.drop(100)
	f.set(procInfo{PID: 101, PPID: 1, PGID: 101, Start: 5000, Comm: "stranger"})
	f.set(procInfo{PID: self, PPID: 1, PGID: 102, Start: 1003, Comm: "runner"})
	f.env[self] = []string{runner.RunIDEnv + "=job1"}

	left := killRun(context.Background(), tr, io.Discard)
	if len(left) != 0 {
		t.Fatalf("survivors: %+v", left)
	}
	if s := f.signalled(101); len(s) != 0 {
		t.Errorf("the reused pid 101 got %v", s)
	}
	if s := f.signalled(self); len(s) != 0 {
		t.Errorf("the runner got %v", s)
	}
	if s := f.signalled(1); len(s) != 0 {
		t.Errorf("pid 1 got %v", s)
	}
	if s := f.signalled(102); len(s) < 2 || s[len(s)-1] != syscall.SIGKILL {
		t.Errorf("the process deaf to SIGTERM got %v, want SIGKILL after SIGTERM", s)
	}
}

// A record or a pid file from before the machine booted names pids and groups that may belong to
// anyone now: the sweep signals none of them, and still removes the run's folder and record.
func TestSweepSignalsNothingOfARunFromBeforeBoot(t *testing.T) {
	shortTermWait(t, 50*time.Millisecond)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	boot := time.Now().Add(-time.Hour)
	f := newFakeTable(
		// Since the boot, 101 led a group of someone else's and exited; 500 is left in it. 100 is
		// someone else's process.
		procInfo{PID: 100, PPID: 1, PGID: 100, Start: 9000, Comm: "stranger"},
		procInfo{PID: 500, PPID: 1, PGID: 101, Start: 9001, Comm: "stranger-child"},
	)
	procs := f.procOS()
	procs.boot = func() (time.Time, bool) { return boot, true }
	work := t.TempDir()
	r := &Runner{cfg: &Config{WorkDir: work}, procs: procs}

	recRoot := filepath.Join(work, "runs", "job1")
	pidRoot := filepath.Join(work, "runs", "job2")
	for _, dir := range []string{recRoot, pidRoot, filepath.Join(work, activeDir)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	rec := runRecord{Job: "job1", Kind: kindRun, Root: recRoot, Started: boot.Add(-time.Hour),
		Leader: procRef{PID: 100, Start: 10}, Members: []procRef{{PID: 100, Start: 10}, {PID: 101, Start: 11}}}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.recordPath("job1"), string(b))
	writeFile(t, filepath.Join(pidRoot, pidFile), "101")
	old := boot.Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(pidRoot, pidFile), old, old); err != nil {
		t.Fatal(err)
	}

	r.sweep(context.Background(), true)
	for _, pid := range []int{100, 101, 500} {
		if s := f.signalled(pid); len(s) != 0 {
			t.Errorf("pid %d got %v", pid, s)
		}
	}
	for _, path := range []string{recRoot, pidRoot, r.recordPath("job1")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still there (%v)", path, err)
		}
	}
}

// What lives through every round comes back as a survivor, and the record keeps it.
func TestKillReturnsTheSurvivorsAndTheRecordKeepsThem(t *testing.T) {
	shortTermWait(t, 50*time.Millisecond)
	f := newFakeTable(
		procInfo{PID: 100, PPID: os.Getpid(), PGID: 100, Start: 1000, Comm: "bash"},
		procInfo{PID: 101, PPID: 100, PGID: 100, Start: 1001, Comm: "stuck"},
	)
	f.immortal[101] = true
	file := filepath.Join(t.TempDir(), activeDir, "job1.json")
	tr := newRunTracker(f.procOS(), "job1", "run", "/runs/job1", file, 100)
	tr.refresh(false)
	f.drop(100)
	left := killRun(context.Background(), tr, io.Discard)
	if len(left) != 1 || left[0].PID != 101 {
		t.Fatalf("survivors = %+v, want pid 101", left)
	}
	var rec runRecord
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range rec.Members {
		found = found || (m.PID == 101 && m.Start == 1001)
	}
	if rec.Job != "job1" || rec.Leader.PID != 100 || !found {
		t.Errorf("record = %+v, want job1 led by 100 with 101 among its members", rec)
	}
}

func equalSets(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func TestParseProcStatAndArgs(t *testing.T) {
	stat := "4242 (ng serve (x)) S 1 4200 4100 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 987654 100 10"
	p, err := parseProcStat([]byte(stat))
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != 4242 || p.PPID != 1 || p.PGID != 4200 || p.SID != 4100 || p.Start != 987654 || p.Comm != "ng serve (x)" {
		t.Errorf("parsed %+v", p)
	}
	buf := []byte{2, 0, 0, 0}
	buf = append(buf, "/usr/bin/node\x00\x00\x00node\x00serve\x00A=1\x00THULU_RUN_ID=j\x00\x00ptr_munge=\x00"...)
	argv, env, err := parseProcArgs(buf)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(argv, " ") != "node serve" || strings.Join(env, " ") != "A=1 THULU_RUN_ID=j" {
		t.Errorf("argv %q, env %q", argv, env)
	}
}

// ---- real processes -------------------------------------------------------------------------------

// markedProcs finds the processes a test started, by the marker in their environment and in their
// command line.
func markedProcs(t *testing.T, mark string) []int {
	t.Helper()
	found := map[int]bool{}
	if procs, err := listProcs(); err == nil {
		for _, p := range procs {
			if p.PID == os.Getpid() || p.Zombie {
				continue
			}
			if env, err := procEnv(p.PID); err == nil && envHas(env, "THULU_TEST_MARK", mark) {
				found[p.PID] = true
			}
		}
	}
	out, err := exec.Command("ps", "-A", "-o", "pid=,command=").Output()
	if err != nil {
		t.Logf("ps: %v", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.Contains(line, "helper-"+mark) {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid != os.Getpid() {
			if p, ok := lookupProc(pid); ok && !p.Zombie {
				found[pid] = true
			}
		}
	}
	pids := make([]int, 0, len(found))
	for pid := range found {
		pids = append(pids, pid)
	}
	return pids
}

// killMarked is every real-process test's cleanup: whatever the test started dies with it, passed or
// failed.
func killMarked(t *testing.T, mark string) {
	for _, pid := range markedProcs(t, mark) {
		if pid > 1 && pid != os.Getpid() {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Logf("kill %d: %v", pid, err)
			}
		}
	}
}

type killFixture struct {
	t        *testing.T
	mark     string
	helper   string
	r        *Runner
	beat     atomic.Value // "ok", "cancel", "gone", "fail" or "reset"
	finished chan runner.FinishRequest
}

func newKillFixture(t *testing.T) *killFixture {
	t.Helper()
	if !procTableSupported {
		t.Skip("no process table on this system")
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &killFixture{t: t, helper: helper, finished: make(chan runner.FinishRequest, 1),
		mark: fmt.Sprintf("m%d_%d", os.Getpid(), time.Now().UnixNano())}
	t.Cleanup(func() { killMarked(t, f.mark) })
	f.beat.Store("ok")
	oldGrace := killGrace
	killGrace = 2 * time.Second
	t.Cleanup(func() { killGrace = oldGrace })
	shortTermWait(t, time.Second)

	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "claude"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /runner/v1/jobs/{id}/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		switch f.beat.Load() {
		case "gone":
			http.Error(w, "job_not_held", http.StatusConflict)
		case "fail":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "reset":
			// The connection drops with no answer, as from a daemon that went away.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			http.Error(w, "no hijack", http.StatusServiceUnavailable)
		case "cancel":
			_ = json.NewEncoder(w).Encode(runner.HeartbeatResponse{LeaseSeconds: 60, Cancel: true})
		default:
			_ = json.NewEncoder(w).Encode(runner.HeartbeatResponse{LeaseSeconds: 60})
		}
	})
	for _, route := range []string{"POST /runner/v1/jobs/{id}/log", "PUT /runner/v1/jobs/{id}/outputs"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("POST /runner/v1/jobs/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var res runner.FinishRequest
		_ = json.NewDecoder(r.Body).Decode(&res)
		f.finished <- res
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.r = &Runner{
		cfg:    &Config{DaemonURL: srv.URL, WorkDir: filepath.Join(t.TempDir(), "work")},
		client: NewClient(srv.URL, runner.CredentialPrefix+"test", "test"),
	}
	return f
}

// escapees starts what an agent leaves behind: (a) a child in bash's group, (b) a double fork into a
// session of its own, (c) a nohup'd child deaf to SIGTERM, (d) a child that keeps the output pipe.
const escapees = `H="$HELPER"; M="$THULU_TEST_MARK"
THULU_TEST_HELPER=sleep "$H" "helper-$M-a" >/dev/null 2>&1 &
if command -v perl >/dev/null 2>&1; then
  THULU_TEST_HELPER=sleep perl -e 'use POSIX; fork and exit; POSIX::setsid(); fork and exit; exec @ARGV' "$H" "helper-$M-b" >/dev/null 2>&1 &
else
  THULU_TEST_HELPER=sleep python3 -c 'import os, sys
if os.fork(): sys.exit(0)
os.setsid()
if os.fork(): sys.exit(0)
os.execv(sys.argv[1], sys.argv[1:])' "$H" "helper-$M-b" >/dev/null 2>&1 &
fi
THULU_TEST_HELPER=ignore-term nohup sh -c 'trap "" TERM; exec "$0" "$1"' "$H" "helper-$M-c" >/dev/null 2>&1 &
THULU_TEST_HELPER=sleep "$H" "helper-$M-d" &
`

func (f *killFixture) job(script string) *job {
	f.t.Helper()
	entry := filepath.Join(f.t.TempDir(), "agent.sh")
	writeFile(f.t, entry, script)
	f.r.cfg.Entrypoint = entry
	spec := &runner.JobSpec{ID: "j" + f.mark, InstanceID: "i1",
		Env: map[string]string{"THULU_TEST_MARK": f.mark, "HELPER": f.helper}}
	j := f.r.newJob(&runner.ClaimResponse{Job: spec, HeartbeatSeconds: 1})
	j.every = 50 * time.Millisecond
	return j
}

// execute runs the job, ends it with end once its script says it started (nil: let it end alone),
// and checks that it returned, that nothing it started survives, and that its folder and record are
// gone.
func (f *killFixture) execute(ctx context.Context, j *job, end func()) runner.FinishRequest {
	f.t.Helper()
	started := filepath.Join(j.root, "tmp", "started")
	if end != nil {
		go func() {
			for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if _, err := os.Stat(started); err == nil {
					// Every escapee is running by now.
					time.Sleep(700 * time.Millisecond)
					end()
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	begin := time.Now()
	go func() {
		j.execute(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		f.t.Fatal("the job did not end within 20 s")
	}
	f.t.Logf("the job took %s", time.Since(begin).Round(time.Millisecond))
	var left []int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if left = markedProcs(f.t, f.mark); len(left) == 0 {
			break
		}
	}
	if len(left) > 0 {
		f.t.Errorf("processes the run started are still running: %v", left)
	}
	if _, err := os.Stat(j.root); !os.IsNotExist(err) {
		f.t.Errorf("the run's folder %s is still there: %v", j.root, err)
	}
	record := filepath.Join(f.r.cfg.WorkDir, activeDir, j.spec.ID+".json")
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		f.t.Errorf("the run's record %s is still there: %v", record, err)
	}
	select {
	case res := <-f.finished:
		return res
	case <-time.After(5 * time.Second):
		f.t.Fatal("the job never reported")
	}
	return runner.FinishRequest{}
}

func TestRunKillsEscapedProcessesOnNormalExit(t *testing.T) {
	f := newKillFixture(t)
	res := f.execute(context.Background(), f.job(escapees+"sleep 1.5\nexit 0\n"), nil)
	if res.ExitCode != 0 || len(res.Warnings) != 0 {
		t.Errorf("finish = %+v, want a clean exit with no warning", res)
	}
}

func TestRunKillsEscapedProcessesOnEveryEnd(t *testing.T) {
	const wait = "touch \"$OCTOPUS_ROOT/tmp/started\"\nsleep 30\n"
	cases := []struct {
		name   string
		script string
		end    func(f *killFixture, cancel context.CancelFunc)
		code   int
	}{
		{"canceled from the daemon", wait, func(f *killFixture, _ context.CancelFunc) { f.beat.Store("cancel") }, runner.ExitCanceled},
		{"no longer held", wait, func(f *killFixture, _ context.CancelFunc) { f.beat.Store("gone") }, runner.ExitCanceled},
		{"runner stopping", wait, func(_ *killFixture, cancel context.CancelFunc) { cancel() }, runner.ExitCanceled},
		{"max turns", "touch \"$OCTOPUS_ROOT/tmp/started\"\nsleep 1.5\nexit 1\n", nil, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newKillFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var end func()
			if c.end != nil {
				end = func() { c.end(f, cancel) }
			}
			res := f.execute(ctx, f.job(escapees+c.script), end)
			if res.ExitCode != c.code {
				t.Errorf("exit code %d, want %d (%+v)", res.ExitCode, c.code, res)
			}
		})
	}
}

// The middle process of a double fork exits before any poll: only the run's marker finds the server.
func TestRunKillsInstantDoubleFork(t *testing.T) {
	f := newKillFixture(t)
	script := `H="$HELPER"; M="$THULU_TEST_MARK"
if command -v perl >/dev/null 2>&1; then
  THULU_TEST_HELPER=sleep perl -e 'use POSIX; fork and exit; POSIX::setsid(); fork and exit; exec @ARGV' "$H" "helper-$M-b" >/dev/null 2>&1
else
  THULU_TEST_HELPER=sleep python3 -c 'import os, sys
if os.fork(): sys.exit(0)
os.setsid()
if os.fork(): sys.exit(0)
os.execv(sys.argv[1], sys.argv[1:])' "$H" "helper-$M-b" >/dev/null 2>&1
fi
sleep 0.3
`
	f.execute(context.Background(), f.job(script), nil)
}

// bash dies on the SIGTERM; a child of its group that ignores it, and holds no pipe, still gets the
// SIGKILL.
func TestKillEscalatesWhenBashExitsFirst(t *testing.T) {
	f := newKillFixture(t)
	script := `THULU_TEST_HELPER=ignore-term "$HELPER" "helper-$THULU_TEST_MARK-deaf" >/dev/null 2>&1 &
touch "$OCTOPUS_ROOT/tmp/started"
sleep 30
`
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := f.execute(ctx, f.job(script), cancel)
	if res.ExitCode != runner.ExitCanceled {
		t.Errorf("finish = %+v, want canceled", res)
	}
}

// An engine probe killed by its timeout takes what it started with it.
func TestRunGroupedTimeoutKillsTheGrandchild(t *testing.T) {
	f := newKillFixture(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	env := append(os.Environ(), "THULU_TEST_HELPER=sleep", "THULU_TEST_MARK="+f.mark)
	_, err := runCLI(ctx, env, []string{"sh", "-c", `"$0" "helper-$1" & echo $! > "$2"; sleep 30`, f.helper, f.mark, pidFile})
	if err == nil {
		t.Fatal("the probe outlived its timeout")
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if p, ok := lookupProc(child); !ok || p.Zombie {
			return
		}
	}
	t.Errorf("the probe's child %d is still running", child)
}
