package hostrun

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// On Linux with a systemd user manager that holds the memory controller, a run's script starts in a
// transient scope with MemoryMax: the kernel keeps the run under its cap, a process that calls setsid
// stays in the scope's cgroup, and one systemctl call kills everything left in it.

// scopeSupport says whether runs get a systemd scope, and why not when they do not.
type scopeSupport struct {
	ok     bool
	reason error
}

// How long a systemd-run or systemctl call may take.
const scopeTimeout = 10 * time.Second

var cgroupRoot = "/sys/fs/cgroup"

// probeScopes checks once, when the runner starts, that a scope with MemoryMax can be made, and that
// the user's systemd manager has the memory controller (without it MemoryMax is accepted and ignored).
var probeScopes = sync.OnceValue(func() scopeSupport {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return scopeSupport{reason: apperr.Precondition("hostrun.errors.scope_no_systemd")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), scopeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemd-run", "--user", "--scope", "--quiet", "-p", "MemoryMax=64M", "true").CombinedOutput()
	if err != nil {
		return scopeSupport{reason: apperr.Precondition("hostrun.errors.scope_failed", "err", strings.TrimSpace(string(out)))}
	}
	uid := strconv.Itoa(os.Getuid())
	file := filepath.Join(cgroupRoot, "user.slice", "user-"+uid+".slice", "user@"+uid+".service", "cgroup.controllers")
	b, err := os.ReadFile(file) //nolint:gosec // G304: a fixed path under /sys/fs/cgroup
	if err != nil || !hasWord(string(b), "memory") {
		return scopeSupport{reason: apperr.Precondition("hostrun.errors.scope_no_memory", "path", file)}
	}
	return scopeSupport{ok: true}
})

func hasWord(s, w string) bool {
	for _, f := range strings.Fields(s) {
		if f == w {
			return true
		}
	}
	return false
}

// runScope is the systemd scope a run's script runs in.
type runScope struct {
	unit   string
	leader int

	mu     sync.Mutex
	cgroup string
}

func newRunScope(job string) *runScope {
	return &runScope{unit: "thulu-run-" + unsafeName.ReplaceAllString(job, "_") + ".scope"}
}

// command is the script's command line inside the scope. systemd-run --scope execs the script, so
// the pid the runner started, and its process group, stay the script's. No --collect: from systemd 253
// a scope's OOMPolicy defaults to stop, so a process over MemoryMax gets the whole scope stopped and
// its cgroup removed before the watcher's next check; the failed unit, with Result=oom-kill, is what
// still says why (killedForMemory), until kill resets it.
func (s *runScope) command(bash, entry string, limitMB int64) (string, []string) {
	mb := strconv.FormatInt(limitMB, 10)
	return "systemd-run", []string{"--user", "--scope", "--quiet",
		"--unit=" + strings.TrimSuffix(s.unit, ".scope"), "-p", "MemoryMax=" + mb + "M", "-p", "MemorySwapMax=0",
		"--", bash, entry}
}

func (s *runScope) started(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leader = pid
}

// dir is the scope's cgroup directory, read from the script's /proc/<pid>/cgroup once systemd-run
// moved it there; empty until then.
func (s *runScope) dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cgroup != "" || s.leader <= 0 {
		return s.cgroup
	}
	b, err := fs.ReadFile(procFS, strconv.Itoa(s.leader)+"/cgroup")
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok && strings.HasSuffix(p, "/"+s.unit) {
			s.cgroup = filepath.Join(cgroupRoot, filepath.Clean("/"+p))
		}
	}
	return s.cgroup
}

// oomKills is how many processes the kernel killed in the scope for going over MemoryMax.
func (s *runScope) oomKills() int64 {
	dir := s.dir()
	if dir == "" {
		return 0
	}
	b, err := os.ReadFile(filepath.Join(dir, "memory.events")) //nolint:gosec // G304: the scope's own cgroup
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

// currentKB is what the scope's processes hold now, swap included.
func (s *runScope) currentKB() int64 {
	dir := s.dir()
	if dir == "" {
		return 0
	}
	var total int64
	for _, f := range []string{"memory.current", "memory.swap.current"} {
		b, err := os.ReadFile(filepath.Join(dir, f)) //nolint:gosec // G304: the scope's own cgroup
		if err != nil {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			total += n / 1024
		}
	}
	return total
}

// killedForMemory tells whether the kernel killed a process of the scope for going over MemoryMax:
// the cgroup's counter while the scope is there, or the unit's result once systemd stopped it.
func (s *runScope) killedForMemory(ctx context.Context) bool {
	if s.oomKills() > 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, scopeTimeout)
	defer cancel()
	out, err := s.systemctl(ctx, "show", "-p", "Result", "--value").Output() //i18n:ignore a systemd property name
	return err == nil && strings.TrimSpace(string(out)) == "oom-kill"
}

// dropFailed unloads the scope if it ended failed (systemd stopped it for memory): without
// --collect a failed unit stays listed until someone resets it.
func (s *runScope) dropFailed(ctx context.Context) {
	state, err := s.systemctl(ctx, "show", "-p", "ActiveState", "--value").Output()
	if err != nil || strings.TrimSpace(string(state)) != "failed" {
		return
	}
	if out, err := s.systemctl(ctx, "reset-failed").CombinedOutput(); err != nil {
		logx.Warn(apperr.Wrap(err, "hostrun.errors.scope_reset", "unit", s.unit, "out", strings.TrimSpace(string(out))),
			"hostrun.log.scope_reset_failed", "unit", s.unit)
	}
}

// systemctl is a `systemctl --user <args> <unit>` call on the scope.
func (s *runScope) systemctl(ctx context.Context, args ...string) *exec.Cmd {
	// G204: a fixed program, fixed verbs and the unit name the runner made.
	return exec.CommandContext(ctx, "systemctl", append(append([]string{"--user"}, args...), s.unit)...) //nolint:gosec // see above
}

// kill sends SIGKILL to whatever is still in the scope, setsid escapees included, then drops the unit
// if it failed (a scope that ended cleanly is dropped by systemd itself). A scope already gone makes
// systemctl kill fail, which is the normal case at a run's end; a failure while its cgroup is still
// there is not.
func (s *runScope) kill(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, scopeTimeout)
	defer cancel()
	out, err := s.systemctl(ctx, "kill", "--signal=SIGKILL").CombinedOutput()
	defer s.dropFailed(ctx)
	if err == nil {
		return
	}
	if dir := s.dir(); dir != "" {
		if _, serr := os.Stat(dir); serr == nil {
			logx.Warn(apperr.Wrap(err, "hostrun.errors.scope_kill", "unit", s.unit, "out", strings.TrimSpace(string(out))),
				"hostrun.log.scope_kill_failed", "unit", s.unit)
		}
	}
}
