package hostrun

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// How often a run's tracker reads the process table. Claude Code starts every Bash command as the
// leader of a new session, so a server the agent starts in the background leaves the run's process
// group at once; the tracker keeps it by its parent while that one lives.
var trackEvery = 500 * time.Millisecond

// How often at most the tracker rewrites its record.
const trackSaveEvery = time.Second

// The folder, under the work dir, that holds one record per running job. It sits outside the run's
// folder, so a removal cut short cannot lose the record of what the run started.
const activeDir = "active"

// runRecord is what <WorkDir>/active/<job>.json says about a run: enough to find its processes
// again after the runner dies.
type runRecord struct {
	Job     string    `json:"job"`
	Kind    string    `json:"kind"`
	Root    string    `json:"root"`
	Leader  procRef   `json:"leader"`
	Started time.Time `json:"started"`
	Members []procRef `json:"members"`
}

type procRef struct {
	PID   int    `json:"pid"`
	Start int64  `json:"start"`
	Comm  string `json:"comm,omitempty"`
}

func refOf(p procInfo) procRef { return procRef{PID: p.PID, Start: p.Start, Comm: p.Comm} }

// runTracker keeps the set of processes a run started, keyed by pid and start time. A process joins
// when it is the leader, when its parent is a member, when its group or session is led by a member
// and it started after the run, or when its environment names the run. A member stays a member after
// its parent dies and launchd or init adopts it.
type runTracker struct {
	procs     procOS
	every     time.Duration
	file      string
	job       string
	root      string
	self      int
	selfGroup int
	// Rules a process out; a leftover's tracker uses it to keep off the runs still running.
	skip func(procInfo) bool

	mu      sync.Mutex
	rec     runRecord
	leader  procInfo
	members map[procKey]procInfo
	// The members the last read of the table found running.
	alive map[procKey]procInfo
	// Processes whose environment was read already.
	checked map[procKey]bool
	dirty   bool
	saved   time.Time

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

func newRunTracker(procs procOS, job, kind, root, file string, leaderPID int) *runTracker {
	leader, ok := procs.lookup(leaderPID)
	if !ok {
		// Not reaped yet, so the table should have it; without its start time every process counts
		// as started after the run, and the tree and the markers alone decide.
		leader = procInfo{PID: leaderPID}
	}
	t := &runTracker{
		procs: procs, every: trackEvery, file: file, job: job, root: root,
		self: os.Getpid(), selfGroup: ownGroup(),
		rec:     runRecord{Job: job, Kind: kind, Root: root, Leader: refOf(leader), Started: time.Now().UTC()},
		leader:  leader,
		members: map[procKey]procInfo{leader.key(): leader},
		alive:   map[procKey]procInfo{},
		checked: map[procKey]bool{},
		dirty:   true,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	return t
}

// newLeftoverTracker rebuilds the tracker of a run that is no longer running, from its record or
// from what its folder and markers say, for the sweep to kill what is left of it. Its members count
// only by pid and start time. With markRoot false, only the job id marks a process: a chat's folder
// is shared by all its turns.
func newLeftoverTracker(procs procOS, rec runRecord, file string, markRoot bool, skip func(procInfo) bool) *runTracker {
	leader := procInfo{PID: rec.Leader.PID, Start: rec.Leader.Start, Comm: rec.Leader.Comm}
	root := rec.Root
	if !markRoot {
		root = ""
	}
	t := &runTracker{
		procs: procs, every: trackEvery, file: file, job: rec.Job, root: root,
		self: os.Getpid(), selfGroup: ownGroup(), skip: skip,
		rec: rec, leader: leader,
		members: map[procKey]procInfo{},
		alive:   map[procKey]procInfo{},
		checked: map[procKey]bool{},
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	if leader.PID > 0 {
		t.members[leader.key()] = leader
	}
	for _, m := range rec.Members {
		if m.PID > 1 {
			t.members[procKey{PID: m.PID, Start: m.Start}] = procInfo{PID: m.PID, Start: m.Start, Comm: m.Comm}
		}
	}
	return t
}

// memberKeys returns every member the tracker holds, running or not.
func (t *runTracker) memberKeys() []procKey {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]procKey, 0, len(t.members))
	for k := range t.members {
		out = append(out, k)
	}
	return out
}

// start polls the table until halt, and reads it again whenever a member forks.
func (t *runTracker) start() {
	forks := t.forkWatch()
	go func() {
		defer close(t.done)
		var forked <-chan struct{}
		if forks != nil {
			defer forks.close()
			forked = forks.events()
		}
		tick := time.NewTicker(t.every)
		defer tick.Stop()
		for {
			t.refresh(false)
			t.watchForks(forks)
			t.mu.Lock()
			if time.Since(t.saved) >= trackSaveEvery {
				t.saveLocked()
			}
			t.mu.Unlock()
			select {
			case <-t.stop:
				return
			case <-tick.C:
			case <-forked:
			}
		}
	}()
}

func (t *runTracker) forkWatch() forkWatch {
	if t.procs.forks == nil {
		return nil
	}
	w, err := t.procs.forks()
	if err != nil {
		if !errors.Is(err, errors.ErrUnsupported) {
			logx.Warn(err, "hostrun.log.fork_watch_failed", "job", t.job, "every", t.every)
		}
		return nil
	}
	return w
}

// How many times in a row the tracker reads the table again for members it just started watching.
const forkRereads = 5

// watchForks watches the members that joined. A member may have forked between the read that found
// it and the watch; the table is read again until a read adds no member.
func (t *runTracker) watchForks(forks forkWatch) {
	if forks == nil {
		return
	}
	for range forkRereads {
		if forks.sync(t.Members()) == 0 {
			return
		}
		t.refresh(false)
	}
}

// halt stops the polling and writes the record as it stands.
func (t *runTracker) halt() {
	t.stopOnce.Do(func() { close(t.stop) })
	<-t.done
	t.save()
}

// refresh reads the table and takes in the processes that joined since. Every process started after
// the run has its environment read once; with markers, read again.
func (t *runTracker) refresh(markers bool) {
	procs, err := t.procs.list()
	if err != nil {
		logx.Warn(err, "hostrun.log.proc_list_failed", "job", t.job)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.adopt(procs)
	if t.scanMarkers(procs, markers) {
		t.adopt(procs)
	}
	t.prune(procs)
}

// candidate rules out what can never be the run's: pid 1, the runner and its direct children (its
// own git calls), and anything older than the run. A zombie is one only through its parent (adopt).
func (t *runTracker) candidate(p procInfo) bool {
	if p.key() == t.leader.key() && p.PID > 0 {
		return true
	}
	if p.PID <= 1 || p.PID == t.self || p.PPID == t.self || p.Start < t.leader.Start {
		return false
	}
	return t.skip == nil || !t.skip(p)
}

// adopt adds, until nothing changes, every process whose parent, group or session belongs to the run.
// A member's child that already exited joins too while its parent has not reaped it: the session
// leader of a `nohup cmd &` is gone in milliseconds, and its pid is what ties the group it leaves
// behind to the run (prune keeps it while that group lives).
func (t *runTracker) adopt(procs []procInfo) {
	byPID := make(map[int]procInfo, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	for grew := true; grew; {
		grew = false
		for _, p := range procs {
			k := p.key()
			if _, ok := t.members[k]; ok {
				// Its parent and group change when it is adopted or calls setsid.
				t.members[k] = p
				continue
			}
			if p.Zombie && !t.childOfMember(p, byPID) {
				continue
			}
			if t.candidate(p) && t.related(p, byPID) {
				t.members[k] = p
				t.dirty, grew = true, true
			}
		}
	}
}

func (t *runTracker) related(p procInfo, byPID map[int]procInfo) bool {
	return t.childOfMember(p, byPID) || t.leads(p.PGID, byPID) || (p.SID > 0 && t.leads(p.SID, byPID))
}

func (t *runTracker) childOfMember(p procInfo, byPID map[int]procInfo) bool {
	parent, ok := byPID[p.PPID]
	if !ok || p.PPID <= 1 {
		return false
	}
	_, member := t.members[parent.key()]
	return member
}

// leads tells whether id, a group or session id, is a member's pid. While a process holds that pid it
// must be the member itself; once the member is gone, the system gives its pid to no one while its
// group or session lives.
func (t *runTracker) leads(id int, byPID map[int]procInfo) bool {
	if id <= 1 || id == t.selfGroup {
		return false
	}
	if cur, ok := byPID[id]; ok {
		_, member := t.members[cur.key()]
		return member
	}
	for k := range t.members {
		if k.PID == id {
			return true
		}
	}
	return false
}

// scanMarkers adds the processes whose environment names the run: ones that left the tree before a
// poll saw their parent (a double fork, nohup, setsid).
func (t *runTracker) scanMarkers(procs []procInfo, all bool) bool {
	added := false
	for _, p := range procs {
		k := p.key()
		if _, ok := t.members[k]; ok || p.Zombie || !t.candidate(p) {
			continue
		}
		if !all {
			if t.checked[k] {
				continue
			}
			t.checked[k] = true
		}
		if t.marked(p.PID) {
			t.members[k] = p
			t.dirty, added = true, true
		}
	}
	return added
}

func (t *runTracker) marked(pid int) bool {
	env, err := t.procs.env(pid)
	if err != nil {
		return false
	}
	return (t.job != "" && envHas(env, runner.RunIDEnv, t.job)) || (t.root != "" && envHas(env, "OCTOPUS_ROOT", t.root))
}

// prune drops the members that exited, except the leader and any member whose pid still names a live
// group or session: the processes left in it are still to be adopted.
func (t *runTracker) prune(procs []procInfo) {
	seen := make(map[procKey]bool, len(procs))
	groups := map[int]bool{}
	for _, p := range procs {
		seen[p.key()] = true
		if !p.Zombie {
			groups[p.PGID], groups[p.SID] = true, true
		}
	}
	clear(t.alive)
	for k, m := range t.members {
		switch {
		case seen[k] && !m.Zombie:
			t.alive[k] = m
		case k == t.leader.key() || groups[k.PID]:
			// Kept: see above.
		default:
			delete(t.members, k)
			t.dirty = true
		}
	}
	for k := range t.checked {
		if !seen[k] {
			delete(t.checked, k)
		}
	}
}

// Members returns the members the last read of the table found running, by pid.
func (t *runTracker) Members() []procInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]procInfo, 0, len(t.alive))
	for _, m := range t.alive {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b procInfo) int { return a.PID - b.PID })
	return out
}

// Snapshot returns the record as it would be written now.
func (t *runTracker) Snapshot() runRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

func (t *runTracker) snapshotLocked() runRecord {
	rec := t.rec
	rec.Members = make([]procRef, 0, len(t.members))
	for _, m := range t.members {
		rec.Members = append(rec.Members, refOf(m))
	}
	slices.SortFunc(rec.Members, func(a, b procRef) int { return a.PID - b.PID })
	return rec
}

func (t *runTracker) save() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.saveLocked()
}

func (t *runTracker) saveLocked() {
	if t.file == "" || !t.dirty {
		return
	}
	t.saved = time.Now()
	b, err := json.Marshal(t.snapshotLocked())
	if err == nil {
		err = os.MkdirAll(filepath.Dir(t.file), 0o700)
	}
	if err == nil {
		err = fsx.WriteFile(t.file, b, 0o600)
	}
	if err != nil {
		logx.Warn(apperr.Wrap(err, "hostrun.errors.track_save", "path", t.file), "hostrun.log.track_save_failed", "job", t.job)
		return
	}
	t.dirty = false
}

// removeRecord drops the record once nothing of the run is left.
func (t *runTracker) removeRecord() {
	if t.file == "" {
		return
	}
	if err := os.Remove(t.file); err != nil && !os.IsNotExist(err) {
		logx.Warn(err, "hostrun.log.remove_failed", "path", t.file)
	}
}

// survivorList names the processes a kill left, for the run's warning.
func survivorList(left []procInfo) string {
	parts := make([]string, 0, len(left))
	for _, p := range left {
		parts = append(parts, strconv.Itoa(p.PID)+" ("+logx.Safe(p.Comm)+")")
	}
	return strings.Join(parts, ", ")
}
