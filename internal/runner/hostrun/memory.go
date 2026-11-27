package hostrun

import (
	"context"
	"io"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// Memory on a runner's machine. Every run has a cap on what its processes take together (the agent
// CLI and everything it starts, in its process group or out of it); the runner stops its largest run
// when the machine's available memory drops under a floor; and it claims no new run while the
// machine has less available than min_free_memory_mb.

// The defaults of the memory settings in runner.json, in MB.
const (
	DefaultRunMemoryLimitMB = 4096
	DefaultMinFreeMemoryMB  = 2048
	DefaultMachineFloorMB   = 768
)

// How often a run's watcher adds up the memory of the run's processes, and the runner compares the
// machine's available memory with its floor.
var memEvery = 2 * time.Second

// The exit code a run stopped for memory reports, as if the system had killed it.
const memoryExitCode = 137

// orDefault is v, or def when v is 0. A negative v stays: it turns the check off.
func orDefault(v, def int) int64 {
	if v == 0 {
		return int64(def)
	}
	return int64(v)
}

func (c *Config) minFreeMB() int64 { return orDefault(c.MinFreeMemoryMB, DefaultMinFreeMemoryMB) }

func (c *Config) machineFloorMB() int64 {
	return orDefault(c.MachineMemoryFloorMB, DefaultMachineFloorMB)
}

// runMemoryLimitMB is the cap of a run of spec in MB, zero or less for none. A project's cap in
// runner.json comes first: the largest among the run's projects, or none when one of them turns it
// off. Then the daemon's value for the run, never above the machine's run_memory_limit_mb. Then the
// machine's run_memory_limit_mb.
func (c *Config) runMemoryLimitMB(spec *runner.JobSpec) int64 {
	var project int64
	for i := range spec.Projects {
		p := &spec.Projects[i]
		v := int64(c.ProjectMemoryLimitMB[p.ID])
		if v < 0 {
			return -1
		}
		project = max(project, v)
	}
	if project > 0 {
		return project
	}
	machine := orDefault(c.RunMemoryLimitMB, DefaultRunMemoryLimitMB)
	if machine > 0 && spec.MemoryLimitMB > 0 {
		return min(machine, int64(spec.MemoryLimitMB))
	}
	return machine
}

// withHeapCap adds a V8 heap cap to NODE_OPTIONS for a run capped at limitMB, so a node process that
// grows fails on its own heap limit before the run's cap stops the whole run. A value that sets one
// already stays as it is, and so do the other options. A script that runs
// `node --max-old-space-size=…` keeps its own value: node reads its command line after NODE_OPTIONS.
func withHeapCap(opts string, limitMB int64) string {
	if limitMB <= 0 || strings.Contains(opts, "--max-old-space-size") || strings.Contains(opts, "--max_old_space_size") {
		return opts
	}
	heap := max(512, limitMB*3/4)
	return strings.TrimSpace(opts + " --max-old-space-size=" + strconv.FormatInt(heap, 10))
}

// MemoryLimits is what runner.json sets about memory, defaults filled in, in MB. Zero or less means
// the check is off.
type MemoryLimits struct {
	RunMB, MinFreeMB, FloorMB int64
	Projects                  map[string]int
}

func (c *Config) MemoryLimits() MemoryLimits {
	return MemoryLimits{RunMB: orDefault(c.RunMemoryLimitMB, DefaultRunMemoryLimitMB), MinFreeMB: c.minFreeMB(),
		FloorMB: c.machineFloorMB(), Projects: c.ProjectMemoryLimitMB}
}

// SizeText is sizeText for the CLI.
func SizeText(mb int64) string { return sizeText(mb) }

// sizeText writes an amount of MB the way the runner's messages show it: "768 MB", "1.2 GB", "4 GB".
func sizeText(mb int64) string {
	if mb < 1024 {
		return strconv.FormatInt(mb, 10) + " MB" //i18n:ignore a unit, the same in every language
	}
	gb := math.Round(float64(mb)/1024*10) / 10
	return strconv.FormatFloat(gb, 'f', -1, 64) + " GB" //i18n:ignore a unit, the same in every language
}

// memoryProbe reads how much memory the machine can still give, in MB (memprobe_*.go).
type memoryProbe interface {
	AvailableMB() (int64, error)
}

func (r *Runner) memory() memoryProbe {
	if r.mem == nil {
		return hostMemory{}
	}
	return r.mem
}

// memSampler reads how much memory each process holds, in KB (memprobe_*.go).
type memSampler interface {
	sample(ctx context.Context, procs []procInfo) map[procKey]int64
}

func (r *Runner) sampler() memSampler {
	if r.newSampler == nil {
		return newHostSampler()
	}
	return r.newSampler()
}

// memStop is why a run's watcher stopped it: the reason the run reports and the message it shows.
type memStop struct {
	reason string
	err    error
}

// memWatch follows the memory of one run. Over the run's limit (or, in a systemd scope, once the
// kernel killed one of its processes for it) it kills the whole run. The runner's floor check asks
// it to kill the run through request.
type memWatch struct {
	job     string
	limitMB int64
	every   time.Duration
	sampler memSampler
	tracker *runTracker
	scope   *runScope
	// The run's own log, which gets the kill's lines too.
	out io.Writer

	usedKB  atomic.Int64
	ended   atomic.Bool
	stopReq chan memStop

	mu      sync.Mutex
	stopped *memStop

	quit     chan struct{}
	quitOnce sync.Once
	done     chan struct{}
}

func newMemWatch(job string, limitMB int64, t *runTracker, s memSampler, scope *runScope) *memWatch {
	return &memWatch{job: job, limitMB: limitMB, every: memEvery, sampler: s, tracker: t, scope: scope,
		stopReq: make(chan memStop, 1), quit: make(chan struct{}), done: make(chan struct{})}
}

// start watches until stop, or until it killed the run.
func (w *memWatch) start(ctx context.Context) {
	go func() {
		defer close(w.done)
		tick := time.NewTicker(w.every)
		defer tick.Stop()
		for {
			select {
			case <-w.quit:
				return
			case s := <-w.stopReq:
				w.kill(ctx, s)
				return
			case <-tick.C:
			}
			if s, over := w.check(ctx); over {
				w.kill(ctx, s)
				return
			}
		}
	}()
}

// stop ends the watching; a kill under way finishes first.
func (w *memWatch) stop() {
	w.quitOnce.Do(func() { close(w.quit) })
	<-w.done
}

// request asks the watcher to kill the run; false when a stop is already on its way.
func (w *memWatch) request(s memStop) bool {
	if !w.ended.CompareAndSwap(false, true) {
		return false
	}
	w.stopReq <- s
	return true
}

// ending tells whether the watcher kills, or killed, its run.
func (w *memWatch) ending() bool { return w.ended.Load() }

// usedMB is what the run's processes held at the last check.
func (w *memWatch) usedMB() int64 { return w.usedKB.Load() / 1024 }

// result is why the watcher stopped the run; nil when it did not.
func (w *memWatch) result() *memStop {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped
}

// check reads the run's memory once and tells whether the run went over its limit.
func (w *memWatch) check(ctx context.Context) (memStop, bool) {
	if w.scope != nil {
		w.usedKB.Store(w.scope.currentKB())
		if w.scope.oomKills() > 0 {
			return memStop{reason: runner.StopMemoryLimit,
				err: apperr.Precondition("hostrun.errors.memory_limit_kernel", "limit", sizeText(w.limitMB))}, true
		}
		return memStop{}, false
	}
	members := w.tracker.Members()
	held := w.sampler.sample(ctx, members)
	var sum, top int64
	var largest procInfo
	for _, m := range members {
		kb := held[m.key()]
		sum += kb
		if kb > top {
			top, largest = kb, m
		}
	}
	w.usedKB.Store(sum)
	if w.limitMB <= 0 || sum <= w.limitMB*1024 {
		return memStop{}, false
	}
	details, _ := w.tracker.procs.details(ctx)
	return memStop{reason: runner.StopMemoryLimit, err: apperr.Precondition("hostrun.errors.memory_limit",
		"limit", sizeText(w.limitMB), "command", commandOf(details, largest), "used", sizeText(top/1024),
		"total", sizeText(sum/1024))}, true
}

// noteKernelKill records a kill the kernel made in the run's scope after the watcher's last check,
// once the run ended: systemd may have stopped the whole scope before the next check read it.
func (w *memWatch) noteKernelKill(ctx context.Context) {
	if w.scope == nil || w.result() != nil || !w.scope.killedForMemory(ctx) {
		return
	}
	s := memStop{reason: runner.StopMemoryLimit,
		err: apperr.Precondition("hostrun.errors.memory_limit_kernel", "limit", sizeText(w.limitMB))}
	w.ended.Store(true)
	w.mu.Lock()
	w.stopped = &s
	w.mu.Unlock()
	log.Printf("[runner] %s", i18n.Log("hostrun.log.memory_stop", "job", w.job, "reason", apperr.Text(s.err)))
}

// kill ends every process of the run, before the run reports.
func (w *memWatch) kill(ctx context.Context, s memStop) {
	w.ended.Store(true)
	w.mu.Lock()
	w.stopped = &s
	w.mu.Unlock()
	log.Printf("[runner] %s", i18n.Log("hostrun.log.memory_stop", "job", w.job, "reason", apperr.Text(s.err)))
	killRun(ctx, w.tracker, w.out)
	if w.scope != nil {
		w.scope.kill(ctx)
	}
}

// watchMemory starts the run's watcher; out is the run's own log.
func (j *job) watchMemory(ctx context.Context, scope *runScope, out io.Writer) *memWatch {
	w := newMemWatch(j.spec.ID, j.memLimit, j.tracker, j.r.sampler(), scope)
	w.out = out
	j.memRef.Store(w)
	w.start(context.WithoutCancel(ctx))
	return w
}

// memoryFloor stops the largest run whenever the machine's available memory drops under
// machine_memory_floor_mb, until ctx ends.
func (r *Runner) memoryFloor(ctx context.Context, every time.Duration) {
	if r.cfg.machineFloorMB() <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.checkFloor()
	}
}

// checkFloor stops the run holding the most memory when the machine has less available than its
// floor. One at a time: while a run stops, the next check waits for its memory to come back.
func (r *Runner) checkFloor() {
	floor := r.cfg.machineFloorMB()
	if floor <= 0 {
		return
	}
	var largest *memWatch
	for _, j := range r.runningJobs() {
		w := j.memRef.Load()
		if w == nil {
			continue
		}
		if w.ending() {
			return
		}
		if largest == nil || w.usedMB() > largest.usedMB() {
			largest = w
		}
	}
	if largest == nil {
		return
	}
	avail, err := r.memory().AvailableMB()
	if err != nil || avail >= floor {
		return
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.machine_low_memory", "available", avail, "floor", floor,
		"job", largest.job, "used", sizeText(largest.usedMB())))
	largest.request(memStop{reason: runner.StopMachineLowMemory, err: apperr.Precondition(
		"hostrun.errors.machine_low_memory", "available", sizeText(avail), "floor", sizeText(floor),
		"used", sizeText(largest.usedMB()))})
}

// admission is whether the runner claims runs, as its last memory check found it.
type admission struct {
	mu          sync.Mutex
	known       bool
	paused      bool
	availableMB int64
	minFreeMB   int64
	since       time.Time
	probeFailed bool
}

// admit tells whether the runner may claim a run now: not while the machine has less memory
// available than min_free_memory_mb. It logs, and reports to the daemon, only when that changes.
func (r *Runner) admit() bool {
	minFree := r.cfg.minFreeMB()
	if minFree <= 0 {
		return true
	}
	avail, err := r.memory().AvailableMB()
	r.adm.mu.Lock()
	if err != nil {
		first := !r.adm.probeFailed
		r.adm.probeFailed = true
		r.adm.mu.Unlock()
		if first {
			logx.Warn(err, "hostrun.log.memory_probe_failed")
		}
		return true
	}
	paused := avail < minFree
	changed := !r.adm.known || r.adm.paused != paused
	tell := changed && (paused || r.adm.known)
	r.adm.availableMB, r.adm.minFreeMB = avail, minFree
	if changed {
		r.adm.since = time.Now().UTC()
	}
	r.adm.known, r.adm.paused = true, paused
	r.adm.mu.Unlock()
	if tell {
		key := "hostrun.log.admission_resumed"
		if paused {
			key = "hostrun.log.admission_paused"
		}
		log.Printf("[runner] %s", i18n.Log(key, "available", sizeText(avail), "min", sizeText(minFree)))
		select {
		case r.pushInv <- struct{}{}:
		default:
		}
	}
	return !paused
}

// admissionState is what the runner reports about admit; nil before its first check.
func (r *Runner) admissionState() *runner.AdmissionState {
	r.adm.mu.Lock()
	defer r.adm.mu.Unlock()
	if !r.adm.known {
		return nil
	}
	st := &runner.AdmissionState{State: runner.AdmissionAccepting, AvailableMB: r.adm.availableMB,
		MinFreeMB: r.adm.minFreeMB, Since: r.adm.since}
	if r.adm.paused {
		st.State = runner.AdmissionPausedLowMemory
	}
	return st
}

// logMemory says at start what the runner does about memory, and warns about each check turned off.
func (r *Runner) logMemory() {
	limit := orDefault(r.cfg.RunMemoryLimitMB, DefaultRunMemoryLimitMB)
	checks := []struct {
		setting string
		value   int64
	}{
		{"run_memory_limit_mb", limit},
		{"min_free_memory_mb", r.cfg.minFreeMB()},
		{"machine_memory_floor_mb", r.cfg.machineFloorMB()},
	}
	for _, c := range checks {
		if c.value < 0 {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.memory_check_off", "setting", c.setting))
		}
	}
	if limit > 0 {
		if r.scope.ok {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.memory_cap_cgroup", "limit", sizeText(limit)))
		} else {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.memory_cap_watcher", "limit", sizeText(limit),
				"reason", apperr.Text(r.scope.reason)))
		}
	}
}

// stillRunning is the processes of left that still run as themselves.
func stillRunning(procs procOS, left []procInfo) []procInfo {
	var out []procInfo
	for _, p := range left {
		if cur, ok := procs.lookup(p.PID); ok && cur.Start == p.Start && !cur.Zombie {
			out = append(out, p)
		}
	}
	return out
}
