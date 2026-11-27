//go:build unix

package hostrun

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

type fakeMemory struct{ mb atomic.Int64 }

func (f *fakeMemory) AvailableMB() (int64, error) { return f.mb.Load(), nil }

func newFakeMemory(mb int64) *fakeMemory {
	f := &fakeMemory{}
	f.mb.Store(mb)
	return f
}

// lockedBuffer takes the runner's log while goroutines write to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

func fastMemoryChecks(t *testing.T) {
	t.Helper()
	old := memEvery
	memEvery = 200 * time.Millisecond
	t.Cleanup(func() { memEvery = old })
}

// allocEscapee is a script whose process leaves the run in a session of its own (a double fork), then
// takes 400 MB, touching every page.
const allocEscapee = `H="$HELPER"; M="$THULU_TEST_MARK"
export THULU_TEST_ALLOC_MB=400
if command -v perl >/dev/null 2>&1; then
  THULU_TEST_HELPER=alloc perl -e 'use POSIX; fork and exit; POSIX::setsid(); fork and exit; exec @ARGV' "$H" "helper-$M-alloc" >/dev/null 2>&1
else
  THULU_TEST_HELPER=alloc python3 -c 'import os, sys
if os.fork(): sys.exit(0)
os.setsid()
if os.fork(): sys.exit(0)
os.execv(sys.argv[1], sys.argv[1:])' "$H" "helper-$M-alloc" >/dev/null 2>&1
fi
touch "$OCTOPUS_ROOT/tmp/started"
sleep 30
`

// A process that left the run's session and grows past the run's cap takes the whole run down, before
// the run reports, and the report says why.
func TestMemoryLimitKillsRun(t *testing.T) {
	f := newKillFixture(t)
	fastMemoryChecks(t)
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

// fakeRun is a running job whose watcher says its processes hold usedMB.
func fakeRun(r *Runner, id string, usedMB int64) *memWatch {
	j := &job{r: r, spec: &runner.JobSpec{ID: id}}
	w := newMemWatch(id, 0, nil, nil, nil)
	w.usedKB.Store(usedMB * 1024)
	j.memRef.Store(w)
	r.addJob(j)
	return w
}

func TestMachineFloorKillsLargestRun(t *testing.T) {
	logs := captureLog(t)
	probe := newFakeMemory(500)
	r := &Runner{cfg: &Config{}, mem: probe}
	small, big := fakeRun(r, "small", 300), fakeRun(r, "big", 2048)
	r.checkFloor()
	select {
	case s := <-big.stopReq:
		if s.reason != runner.StopMachineLowMemory {
			t.Errorf("reason = %q, want %q", s.reason, runner.StopMachineLowMemory)
		}
		if msg := apperr.Text(s.err); !strings.Contains(msg, "500 MB") || !strings.Contains(msg, "768 MB") {
			t.Errorf("message = %q, want the available memory and the floor", msg)
		}
	default:
		t.Fatal("the largest run was not stopped")
	}
	// While the largest stops, nothing else does.
	r.checkFloor()
	if small.ending() {
		t.Error("the smaller run was stopped too")
	}
	if !strings.Contains(logs.String(), "500 MB available") {
		t.Errorf("log = %q, want the available MB", logs.String())
	}

	calm := &Runner{cfg: &Config{}, mem: newFakeMemory(4096)}
	w := fakeRun(calm, "one", 3000)
	calm.checkFloor()
	if w.ending() {
		t.Error("a run was stopped with the machine above its floor")
	}
}

func TestAdmissionPausesClaims(t *testing.T) {
	old := claimBackoff
	claimBackoff = 20 * time.Millisecond
	t.Cleanup(func() { claimBackoff = old })
	logs := captureLog(t)
	var claims atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runner/v1/claim", func(w http.ResponseWriter, _ *http.Request) {
		claims.Add(1)
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(runner.ClaimResponse{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	probe := newFakeMemory(500)
	r := &Runner{cfg: &Config{DaemonURL: srv.URL}, client: NewClient(srv.URL, runner.CredentialPrefix+"test", "test"),
		mem: probe, pushInv: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	loops := sync.WaitGroup{}
	for _, kind := range []string{runner.ClaimRun, runner.ClaimChat} {
		loops.Add(1)
		go func() {
			defer loops.Done()
			_ = r.claimLoop(ctx, ctx, &wg, claimPool{kind: kind, size: 1})
		}()
	}
	t.Cleanup(func() {
		cancel()
		loops.Wait()
	})

	time.Sleep(300 * time.Millisecond)
	if n := claims.Load(); n != 0 {
		t.Errorf("%d claims with 500 MB available, want none", n)
	}
	if st := r.admissionState(); st == nil || st.State != runner.AdmissionPausedLowMemory || st.AvailableMB != 500 || st.MinFreeMB != DefaultMinFreeMemoryMB {
		t.Errorf("admission = %+v, want paused at 500 MB of %d", st, DefaultMinFreeMemoryMB)
	}
	select {
	case <-r.pushInv:
	default:
		t.Error("the change did not ask for an inventory report")
	}
	if n := strings.Count(logs.String(), "not taking runs: 500 MB available, below the 2 GB minimum"); n != 1 {
		t.Errorf("%d pause lines, want 1:\n%s", n, logs.String())
	}

	probe.mb.Store(8192)
	for deadline := time.Now().Add(3 * time.Second); claims.Load() == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}
	if claims.Load() == 0 {
		t.Fatal("no claim with 8 GB available")
	}
	if st := r.admissionState(); st == nil || st.State != runner.AdmissionAccepting {
		t.Errorf("admission = %+v, want accepting", st)
	}
	if n := strings.Count(logs.String(), "taking runs again: 8 GB available"); n != 1 {
		t.Errorf("%d recovery lines, want 1:\n%s", n, logs.String())
	}
}

func TestNodeOptions(t *testing.T) {
	cases := []struct {
		name, spec, inherited string
		limit                 int64
		want                  string
	}{
		{"unset", "", "", 4096, "--max-old-space-size=3072"},
		{"other options kept", "--enable-source-maps", "", 4096, "--enable-source-maps --max-old-space-size=3072"},
		{"a cap already set", "--max-old-space-size=8192", "", 4096, "--max-old-space-size=8192"},
		{"inherited cap", "", "--max-old-space-size=8192", 4096, "--max-old-space-size=8192"},
		{"inherited options kept", "", "--enable-source-maps", 4096, "--enable-source-maps --max-old-space-size=3072"},
		{"small cap", "", "", 128, "--max-old-space-size=512"},
		{"cap off", "--enable-source-maps", "", -1, "--enable-source-maps"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("NODE_OPTIONS", c.inherited)
			if c.inherited == "" {
				if err := os.Unsetenv("NODE_OPTIONS"); err != nil {
					t.Fatal(err)
				}
			}
			env := map[string]string{}
			if c.spec != "" {
				env["NODE_OPTIONS"] = c.spec
			}
			j := &job{r: &Runner{cfg: &Config{}}, spec: &runner.JobSpec{ID: "j1", Env: env}, root: t.TempDir(), memLimit: c.limit}
			if got := envValue(j.env(), "NODE_OPTIONS"); got != c.want {
				t.Errorf("NODE_OPTIONS = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRunMemoryLimit(t *testing.T) {
	spec := func(limit int, projects ...string) *runner.JobSpec {
		s := &runner.JobSpec{MemoryLimitMB: limit}
		for _, p := range projects {
			s.Projects = append(s.Projects, runner.JobProject{ID: p})
		}
		return s
	}
	cases := []struct {
		name string
		cfg  *Config
		spec *runner.JobSpec
		want int64
	}{
		{"default", &Config{}, spec(0), DefaultRunMemoryLimitMB},
		{"machine", &Config{RunMemoryLimitMB: 6000}, spec(0), 6000},
		{"daemon below the machine", &Config{}, spec(2048), 2048},
		{"daemon above the machine", &Config{}, spec(9000), DefaultRunMemoryLimitMB},
		{"project wins", &Config{ProjectMemoryLimitMB: map[string]int{"p1": 8192}}, spec(2048, "p1"), 8192},
		{"largest project", &Config{ProjectMemoryLimitMB: map[string]int{"p1": 3000, "p2": 5000}}, spec(0, "p1", "p2"), 5000},
		{"project off", &Config{ProjectMemoryLimitMB: map[string]int{"p1": -1}}, spec(0, "p1"), -1},
		{"machine off", &Config{RunMemoryLimitMB: -1}, spec(2048), -1},
	}
	for _, c := range cases {
		if got := c.cfg.runMemoryLimitMB(c.spec); got != c.want {
			t.Errorf("%s: limit = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestMemoryParsers(t *testing.T) {
	top := parseTopMem([]byte("Processes: 600 total\n\nPID   MEM \n1076  371M\n933   1024K+\n77    5.1G-\n12    0B\nbad   1M\n"))
	want := map[int]int64{1076: 371 * 1024, 933: 1024, 77: 5347737, 12: 0}
	for pid, kb := range want {
		if top[pid] != kb {
			t.Errorf("top pid %d = %d KB, want %d", pid, top[pid], kb)
		}
	}
	if len(top) != len(want) {
		t.Errorf("top = %v, want %v", top, want)
	}
	if rss := parsePSRSS([]byte("  12  2048\n  13 abc\n")); len(rss) != 1 || rss[12] != 2048 {
		t.Errorf("ps rss = %v", rss)
	}
	meminfo := []byte("MemTotal:       16384000 kB\nMemAvailable:    1234567 kB\n")
	if kb, ok := parseMeminfo(meminfo, "MemAvailable"); !ok || kb != 1234567 {
		t.Errorf("MemAvailable = %d, %v", kb, ok)
	}
	if _, ok := parseMeminfo(meminfo, "VmSwap"); ok {
		t.Error("VmSwap found in meminfo")
	}
	for mb, want := range map[int64]string{768: "768 MB", 1229: "1.2 GB", 2048: "2 GB", 5222: "5.1 GB"} {
		if got := sizeText(mb); got != want {
			t.Errorf("sizeText(%d) = %q, want %q", mb, got, want)
		}
	}
}
