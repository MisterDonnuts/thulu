package task

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

// Locked tests on a runner's code. The daemon has no worktree for a project that lives on a machine,
// so the job asks the runner to read the blobs at the run's end (runner.TestLock) and the run's end
// brings them back (runner.TestFiles). Locking and checking then follow the same rules as on the
// daemon's worktrees, with one difference: the exception for a locked test the default branch
// changed after the lock (movedOnDefault) needs the default branch's history, which the runner does
// not send, so a rebase that brings such a change asks a person.

// RunnerTestLock is what a stage run on a machine must read of the task's locked tests: in a
// lock_tests stage, what the run changes; elsewhere, the files other stages locked. nil asks nothing.
func (d *Dispatcher) RunnerTestLock(ctx context.Context, t *Task, stage Stage) *runner.TestLock {
	if t == nil || d.projects == nil || len(t.ProjectIDs) == 0 {
		return nil
	}
	info, _ := d.stagesFor(t).Get(stage)
	if info.ReleaseResources {
		return nil
	}
	var onRunner []string
	for _, pid := range t.ProjectIDs {
		if d.projectRunsOnRunner(ctx, pid) {
			onRunner = append(onRunner, pid)
		}
	}
	if len(onRunner) == 0 {
		return nil
	}
	if info.LockTests {
		return &runner.TestLock{Lock: onRunner}
	}
	byProject, err := d.lockedByOtherStages(ctx, t.ID, stage)
	if err != nil {
		return nil
	}
	check := map[string][]string{}
	for _, pid := range onRunner {
		for i := range byProject[pid] {
			check[pid] = append(check[pid], byProject[pid][i].Path)
		}
	}
	if len(check) == 0 {
		return nil
	}
	return &runner.TestLock{Check: check}
}

// lockRunnerTests locks what a lock_tests run on a machine reported for one project. The listed
// files count only under the paths the daemon itself read from the run's locked-tests.txt.
func (d *Dispatcher) lockRunnerTests(ctx context.Context, t *Task, run *lockRun, pid string, files runner.TestFiles) int {
	var tests []string
	blobs := map[string]string{}
	for _, p := range files.Changed {
		if !IsTestPath(p) {
			continue
		}
		tests = append(tests, p)
		if b, ok := files.Blobs[p]; ok {
			blobs[p] = b
		}
	}
	listed := map[string]string{}
	for p, b := range files.Listed {
		if underListed(p, run.listed) {
			listed[p] = b
		}
	}
	ref := ""
	if files.Head != "" {
		ref = pid + "@" + files.Head
	}
	return d.storeLocks(ctx, t, run, pid, lockFiles{tests: tests, blobs: blobs, listed: listed, base: files.Base, ref: ref})
}

// underListed reports whether p is one of the listed paths or a file under a listed directory.
func underListed(p string, listed []string) bool {
	return slices.ContainsFunc(listed, func(l string) bool { return p == l || strings.HasPrefix(p, l+"/") })
}

// runnerTestChanges compares a project's locked files with what the runner read at the run's end.
// A file the runner was not asked about (locked after the run started) proves nothing, so the result
// is incomplete and a pending approval stays pending.
func runnerTestChanges(pid string, list []LockedFile, files runner.TestFiles) ([]testChange, bool) {
	complete := true
	var changes []testChange
	for i := range list {
		f := &list[i]
		if !slices.Contains(files.Checked, f.Path) {
			complete = false
			continue
		}
		if now := files.Blobs[f.Path]; now != f.BlobSHA {
			changes = append(changes, testChange{Project: pid, Path: f.Path, Locked: f.BlobSHA, Now: now})
		}
	}
	return changes, complete
}

// runnerTestStash keeps what runners reported between the run's end (RecordRunnerReport) and its
// post-run (lockedTestsAfterRun), which follow each other in one daemon. A daemon that dies in
// between loses it, and the post-run says the lock could not be checked.
type runnerTestStash struct {
	mu    sync.Mutex
	byRun map[string][]runner.TestFiles
	order []string
}

// Far above the runs one daemon has between their end and their post-run; it bounds what reports
// nobody takes (a task deleted meanwhile) can hold.
const runnerTestStashMax = 256

func (s *runnerTestStash) put(instance string, files []runner.TestFiles) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byRun == nil {
		s.byRun = map[string][]runner.TestFiles{}
	}
	if _, ok := s.byRun[instance]; !ok {
		s.order = append(s.order, instance)
	}
	s.byRun[instance] = files
	for len(s.order) > runnerTestStashMax {
		delete(s.byRun, s.order[0])
		s.order = s.order[1:]
	}
}

// take returns the run's report by project and forgets it; nil when there is none.
func (s *runnerTestStash) take(instance string) map[string]runner.TestFiles {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, ok := s.byRun[instance]
	if !ok {
		return nil
	}
	delete(s.byRun, instance)
	s.order = slices.DeleteFunc(s.order, func(id string) bool { return id == instance })
	out := make(map[string]runner.TestFiles, len(files))
	for _, f := range files {
		out[f.ProjectID] = f
	}
	return out
}
