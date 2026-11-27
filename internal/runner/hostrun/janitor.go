package hostrun

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// What the runner keeps track of to clean up after runs that are no longer running: the records in
// <WorkDir>/active, the pid files in the run and chat folders, and the THULU_RUN_ID every process of
// a run carries. Folder existence alone never decides anything, and a folder or record goes only
// once nothing of its run is left running.

// How often the janitor looks for what ended runs left behind.
var janitorEvery = time.Minute

// The kinds of folder a run lives in.
const (
	kindRun  = "run"
	kindChat = "chat"
)

// running tells whether this runner process is running the job.
func (r *Runner) running(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.jobs[id]
	return ok
}

func (r *Runner) addJob(j *job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jobs == nil {
		r.jobs = map[string]*job{}
	}
	r.jobs[j.spec.ID] = j
}

func (r *Runner) dropJob(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, id)
}

func (r *Runner) runningJobs() []*job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*job, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j)
	}
	return out
}

func (r *Runner) procOS() procOS {
	if r.procs.list == nil {
		return hostProcs
	}
	return r.procs
}

// sweepAll runs when the runner starts, before it claims anything: no job is running, so whatever
// a run of this work dir left is a leftover.
func (r *Runner) sweepAll(ctx context.Context) {
	r.sweep(ctx, true)
	r.pruneMirrors(ctx)
}

// janitor sweeps every interval, and at once when the wall clock shows the machine slept.
func (r *Runner) janitor(ctx context.Context, every time.Duration) {
	check := max(every/12, 10*time.Millisecond)
	t := time.NewTicker(check)
	defer t.Stop()
	// Round(0) drops the monotonic reading: it stops while the machine sleeps, the wall clock does not.
	last := time.Now().Round(0)
	lastSweep := last
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now().Round(0)
		woke := now.Sub(last) > 2*every
		if woke {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.machine_woke", "gap", now.Sub(last).Round(time.Second)))
		}
		if woke || now.Sub(lastSweep) >= every {
			r.sweep(ctx, false)
			now = time.Now().Round(0)
			lastSweep = now
		}
		last = now
	}
}

// sweep kills what runs that are no longer running left, then removes their folders and records.
// Marked processes go first: a folder may only go once its run has nothing left running.
func (r *Runner) sweep(ctx context.Context, startup bool) {
	if r.cfg.WorkDir == "" {
		return
	}
	skip := r.ownedByRunning()
	r.sweepMarked(ctx, skip)
	r.sweepRecords(ctx, startup, skip)
	r.sweepRunFolders(ctx, startup, skip)
	r.sweepChatFolders(ctx, skip)
}

// ownedByRunning tells whether a process belongs to a job this runner is running: a member of its
// tracker, or a process whose environment names it. A leftover's tracker never takes one, even
// through a pid the system gave again.
func (r *Runner) ownedByRunning() func(procInfo) bool {
	jobs := r.runningJobs()
	if len(jobs) == 0 {
		return nil
	}
	members := map[procKey]bool{}
	for _, j := range jobs {
		if t := j.trackerRef.Load(); t != nil {
			for _, k := range t.memberKeys() {
				members[k] = true
			}
		}
	}
	procs := r.procOS()
	memo := map[procKey]bool{}
	return func(p procInfo) bool {
		k := p.key()
		if v, ok := memo[k]; ok {
			return v
		}
		owned := members[k]
		if !owned {
			if env, err := procs.env(p.PID); err == nil {
				if id := envValue(env, runner.RunIDEnv); id != "" {
					owned = r.running(id)
				}
			}
		}
		memo[k] = owned
		return owned
	}
}

type runMark struct {
	job, root string
}

// mark reads which run of this work dir a process belongs to: its THULU_RUN_ID, when its
// OCTOPUS_ROOT is a run or chat folder of this runner. Runs of another runner on the machine are
// not this one's to kill.
func (r *Runner) mark(procs procOS, pid int) runMark {
	env, err := procs.env(pid)
	if err != nil {
		return runMark{}
	}
	job, root := envValue(env, runner.RunIDEnv), envValue(env, "OCTOPUS_ROOT")
	if job == "" || r.kindOf(root) == "" {
		return runMark{}
	}
	return runMark{job: job, root: root}
}

// kindOf tells whether root is one of this runner's run folders (kindRun) or chat folders
// (kindChat); empty for anything else, which the sweep never removes.
func (r *Runner) kindOf(root string) string {
	if root == "" || filepath.Base(root) == "." || filepath.Base(root) == ".." {
		return ""
	}
	switch filepath.Dir(filepath.Clean(root)) {
	case filepath.Join(r.cfg.WorkDir, "runs"):
		return kindRun
	case r.cfg.chatsDir():
		return kindChat
	}
	return ""
}

// sweepMarked kills every process of this work dir's runs whose run this runner is not running. A
// marked process's descendants go with it.
func (r *Runner) sweepMarked(ctx context.Context, skip func(procInfo) bool) {
	procs := r.procOS()
	list, err := procs.list()
	if err != nil {
		logx.Warn(err, "hostrun.log.sweep_list_failed")
		return
	}
	self := os.Getpid()
	seen := make(map[procKey]bool, len(list))
	found := map[string]*runRecord{}
	r.marksMu.Lock()
	if r.marks == nil {
		r.marks = map[procKey]runMark{}
	}
	for _, p := range list {
		if p.Zombie || p.PID <= 1 || p.PID == self {
			continue
		}
		k := p.key()
		seen[k] = true
		m, ok := r.marks[k]
		if !ok {
			m = r.mark(procs, p.PID)
			r.marks[k] = m
		}
		if m.job != "" {
			rec := found[m.job]
			if rec == nil {
				rec = &runRecord{Job: m.job, Root: m.root}
				found[m.job] = rec
			}
			rec.Members = append(rec.Members, refOf(p))
		}
	}
	for k := range r.marks {
		if !seen[k] {
			delete(r.marks, k)
		}
	}
	r.marksMu.Unlock()
	// After the list: a process of a job exists only once the job counts as running.
	for id, rec := range found {
		if r.running(id) {
			continue
		}
		t := newLeftoverTracker(procs, *rec, "", false, skip)
		killRun(ctx, t, nil)
	}
}

func (r *Runner) recordPath(jobID string) string {
	return filepath.Join(r.cfg.WorkDir, activeDir, unsafeName.ReplaceAllString(jobID, "_")+".json")
}

// sweepRecords kills what every record of a job this runner is not running names, then removes the
// job's folder and the record.
func (r *Runner) sweepRecords(ctx context.Context, startup bool, skip func(procInfo) bool) {
	dir := filepath.Join(r.cfg.WorkDir, activeDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		file := filepath.Join(dir, e.Name())
		rec, err := readRecord(file)
		if rec.Job == "" {
			rec.Job = name
		}
		if r.running(rec.Job) {
			continue
		}
		if err != nil {
			// Nothing to find its processes by; the markers still name them.
			logx.Warn(err, "hostrun.log.record_unreadable", "path", file)
			removeFile(file)
			continue
		}
		r.sweepRun(ctx, rec, file, startup, skip)
	}
}

func readRecord(file string) (runRecord, error) {
	var rec runRecord
	b, err := os.ReadFile(file) //nolint:gosec // G304: a record this runner wrote in its own work dir
	if err != nil {
		return rec, apperr.Wrap(err, "hostrun.errors.record_read", "path", file)
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return runRecord{}, apperr.Wrap(err, "hostrun.errors.record_read", "path", file)
	}
	return rec, nil
}

// sweepRunFolders handles the run folders no record names: a run from before records, or one whose
// record went with a crash. Its pid file finds its process group.
func (r *Runner) sweepRunFolders(ctx context.Context, startup bool, skip func(procInfo) bool) {
	runsDir := filepath.Join(r.cfg.WorkDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || r.running(e.Name()) {
			continue
		}
		if _, err := os.Stat(r.recordPath(e.Name())); err == nil {
			// sweepRecords kept it: something of the run still runs.
			continue
		}
		root := filepath.Join(runsDir, e.Name())
		rec := r.pidFileRecord(ctx, e.Name(), kindRun, root)
		r.sweepRun(ctx, rec, "", startup, skip)
	}
}

// sweepChatFolders handles a chat whose last turn left its pid file: the runner died during the turn.
// The folder and the session stay for the chat's next turn.
func (r *Runner) sweepChatFolders(ctx context.Context, skip func(procInfo) bool) {
	entries, err := os.ReadDir(r.cfg.chatsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		root := filepath.Join(r.cfg.chatsDir(), e.Name())
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, pidFile)); err != nil {
			continue
		}
		rec := r.pidFileRecord(ctx, "chat:"+e.Name(), kindChat, root)
		r.sweepRun(ctx, rec, "", false, skip)
	}
}

// The pid file is written right after its run's script starts; a file system may keep its time to
// the second.
const pidFileSlack = 2 * time.Second

// pidFileRecord rebuilds what a record would say about the run in root from its pid file: the run
// started when the file was written.
func (r *Runner) pidFileRecord(ctx context.Context, job, kind, root string) runRecord {
	rec := runRecord{Job: job, Kind: kind, Root: root}
	if info, err := os.Stat(filepath.Join(root, pidFile)); err == nil {
		rec.Started = info.ModTime()
	}
	rec.Leader = r.pidFileLeader(ctx, root, rec.Started)
	return rec
}

// pidFileLeader reads the leader a run left in root's pid file: the live process when its command
// line still names root, the bare pid when it is gone and its group lives on (no new process gets a
// pid equal to a live group id), nothing when the pid now belongs to another process or the run
// started before the machine booted. With the bare pid, its Start is when the run started, so the
// group's processes older than the run stay out.
func (r *Runner) pidFileLeader(ctx context.Context, root string, started time.Time) procRef {
	b, err := os.ReadFile(filepath.Join(root, pidFile)) //nolint:gosec // G304: a file this runner wrote in its own work dir
	if err != nil || r.beforeBoot(started) {
		return procRef{}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return procRef{}
	}
	procs := r.procOS()
	if p, ok := procs.lookup(pid); ok && !p.Zombie {
		if out, err := commandLine(ctx, pid); err == nil && strings.Contains(out, root) {
			return refOf(p)
		}
		return procRef{}
	}
	if !groupHasMembers(ctx, pid) {
		return procRef{}
	}
	ref := procRef{PID: pid}
	if procs.startAt != nil && !started.IsZero() {
		ref.Start = procs.startAt(started.Add(-pidFileSlack))
	}
	return ref
}

// beforeBoot tells whether t is before the machine last booted: nothing a run started then can still
// run, and the pids and groups its record names may belong to anyone now.
func (r *Runner) beforeBoot(t time.Time) bool {
	procs := r.procOS()
	if t.IsZero() || procs.boot == nil {
		return false
	}
	boot, ok := procs.boot()
	return ok && t.Before(boot)
}

// sweepRun kills what is left of one run and, once nothing is, removes its folder and record. A
// chat's folder is held while it is swept, so no turn starts in it meanwhile.
func (r *Runner) sweepRun(ctx context.Context, rec runRecord, file string, startup bool, skip func(procInfo) bool) {
	kind := r.kindOf(rec.Root)
	if kind == kindChat {
		unlock, ok := r.tryLockRepo(chatLockKey(rec.Root))
		if !ok {
			// A turn of this chat is running; the next sweep looks again.
			return
		}
		defer unlock()
	}
	// Without a leader or members only the markers could find the run, and sweepMarked looked for
	// them already. A run from before the machine booted has nothing left running.
	if r.beforeBoot(rec.Started) {
		log.Printf("[runner] %s", i18n.Log("hostrun.log.swept_before_boot", "job", rec.Job, "started", rec.Started.Format(time.RFC3339)))
	} else if rec.Leader.PID > 0 || len(rec.Members) > 0 {
		t := newLeftoverTracker(r.procOS(), rec, file, kind != "", skip)
		if left := killRun(ctx, t, nil); len(left) > 0 {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.run_kept_alive", "job", rec.Job, "path", rec.Root, "count", len(left)))
			return
		}
	}
	if !r.removeLeftover(ctx, kind, rec.Root, rec.Job, startup) {
		return
	}
	if file != "" {
		removeFile(file)
	}
}

// removeLeftover removes what a run left in its folder once nothing of it runs. A run's folder goes
// whole, after its clones' work is saved; a chat keeps its folder and session and loses the turn's
// code, outputs and pid file. A running runner leaves the folders keep_runs keeps.
func (r *Runner) removeLeftover(ctx context.Context, kind, root, jobID string, startup bool) bool {
	switch kind {
	case kindRun:
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return true
		}
		if !startup && r.cfg.KeepRuns {
			removeFile(filepath.Join(root, pidFile))
			return true
		}
		if !saveLeftovers(ctx, root, filepath.Join(r.cfg.WorkDir, "repos"), jobID) {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.run_kept", "path", root))
			return false
		}
		removeSession(root)
		if err := os.RemoveAll(root); err != nil {
			logx.Warn(err, "hostrun.log.remove_failed", "path", root)
			return false
		}
		log.Printf("[runner] %s", i18n.Log("hostrun.log.swept", "job", jobID))
	case kindChat:
		for _, name := range []string{"projects", "ro", "runs", "output", pidFile} {
			p := filepath.Join(root, name)
			logx.Warn(os.RemoveAll(p), "hostrun.log.remove_failed", "path", p)
		}
		log.Printf("[runner] %s", i18n.Log("hostrun.log.chat_turn_swept", "chat", filepath.Base(root)))
	}
	return true
}

// killChatLeftovers stops what the chat's last turn left running before the next turn starts in its
// folder. The turn holds the chat, so every process marked with the folder is a leftover.
func (r *Runner) killChatLeftovers(ctx context.Context, root string) {
	if _, err := os.Stat(filepath.Join(root, pidFile)); err != nil {
		return
	}
	rec := r.pidFileRecord(ctx, "chat:"+filepath.Base(root), kindChat, root)
	if left := killRun(ctx, newLeftoverTracker(r.procOS(), rec, "", true, r.ownedByRunning()), nil); len(left) > 0 {
		// The pid file stays for the sweep, and the markers still name them.
		return
	}
	removeFile(filepath.Join(root, pidFile))
}

// pruneMirrors drops the worktrees a crash left registered in the mirrors: a read-only run's or a
// chat turn's, whose folder went without `git worktree remove`.
func (r *Runner) pruneMirrors(ctx context.Context) {
	dir := filepath.Join(r.cfg.WorkDir, "repos")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repo := filepath.Join(dir, e.Name())
		if _, err := git(ctx, repo, "worktree", "prune"); err != nil {
			logx.Warn(err, "hostrun.log.prune_failed", "repo", repo)
		}
	}
}

func removeFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logx.Warn(err, "hostrun.log.remove_failed", "path", path)
	}
}
