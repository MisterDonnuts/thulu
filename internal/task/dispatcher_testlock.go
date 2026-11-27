package task

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// Locked tests: a stage with lock_tests writes the tests the code must pass, and the stages after it
// may not quietly rewrite them. When the lock stage closes a run with exit 0 the daemon records the
// git blob of every test file that run added or changed, plus the paths the agent listed in
// locked-tests.txt. After each later run it compares; a changed blob opens a system approval
// "test_change" in the current stage, which holds the task there until a person decides.

// The system approval a changed locked test opens.
const ApprovalKeyTestChange = "test_change"

const (
	lockedTestsFile = runner.LockedTestsFile
	// Uploaded by a pod to /tasks/{id}/locked-tests; read back like verdict.md.
	ReportKindLockedTests = "locked_tests"
)

// One locked file whose blob moved, as the approval's payload carries it.
type testChange struct {
	Project string `json:"project"`
	Path    string `json:"path"`
	Locked  string `json:"locked"`
	Now     string `json:"now"`
}

type testChangePayload struct {
	Ref   string       `json:"ref,omitempty"`
	Files []testChange `json:"files"`
}

// What the locked-tests check found at a run's end; the auditor's signals mode reads it.
type lockedTestsResult struct {
	// A test another stage locked changed.
	Touched bool
	// A project's locked tests could not be compared (its code on a machine that sent nothing, a
	// worktree git could not read).
	Unchecked bool
}

// After the worktrees are committed: lock in a lock_tests stage, check anywhere else that still has
// its code.
func (d *Dispatcher) lockedTestsAfterRun(ctx context.Context, t *Task, stage Stage, instanceID, outputDir string, exitCode int) lockedTestsResult {
	// Taken first, so a run that locks or checks nothing does not leave its report behind.
	reported := d.runnerTests.take(instanceID)
	if t == nil || d.projects == nil || len(t.ProjectIDs) == 0 {
		return lockedTestsResult{}
	}
	info, _ := d.stagesFor(t).Get(stage)
	switch {
	case info.ReleaseResources:
		return lockedTestsResult{}
	case info.LockTests:
		if exitCode == 0 {
			d.lockTests(ctx, t, stage, instanceID, outputDir, reported)
		}
		return lockedTestsResult{}
	default:
		return d.checkLockedTests(ctx, t, stage, reported)
	}
}

// HEADs of the task's worktrees when a lock_tests run started, so its post-run diff covers that run
// alone. Recorded in agent_run_started.
func (d *Dispatcher) lockStageHeads(ctx context.Context, t *Task, stage Stage) string {
	if info, _ := d.stagesFor(t).Get(stage); !info.LockTests {
		return ""
	}
	return d.currentRef(ctx, t)
}

func parseRefs(ref string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(ref, ",") {
		if pid, sha, ok := strings.Cut(part, "@"); ok && pid != "" && sha != "" {
			out[pid] = sha
		}
	}
	return out
}

func (d *Dispatcher) runStartHeads(ctx context.Context, instanceID string) map[string]string {
	raw, ok := d.store.EventPayloadForInstance(ctx, EventAgentRunStarted, instanceID)
	if !ok {
		return map[string]string{}
	}
	var p struct {
		Heads string `json:"heads"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return map[string]string{}
	}
	return parseRefs(p.Heads)
}

// Disk first (Docker), then what the pod uploaded for this run.
func (d *Dispatcher) lockedTestsList(ctx context.Context, taskID, instanceID, outputDir string) string {
	if body, ok := readFile(outputDir, lockedTestsFile); ok {
		return body
	}
	reports, err := d.store.ListReports(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.read_locked_tests", "task", taskID)
		return ""
	}
	for i := len(reports) - 1; i >= 0; i-- {
		if reports[i].Kind == ReportKindLockedTests && reports[i].InstanceID == instanceID {
			return reports[i].Body
		}
	}
	return ""
}

// What one lock_tests run knows while it locks, project by project.
type lockRun struct {
	owner  string
	stage  Stage
	pre    map[string]string
	listed []string
	// The files this stage locked before, keyed by project and path joined with a NUL.
	owned map[string]bool
	ref   string
	now   time.Time
}

func (d *Dispatcher) lockTests(ctx context.Context, t *Task, stage Stage, instanceID, outputDir string, reported map[string]runner.TestFiles) {
	listed, refused := parseLockedList(d.lockedTestsList(ctx, t.ID, instanceID, outputDir))
	if len(refused) > 0 {
		d.systemComment(ctx, t.ID, NoteOf("task.comments.locked_tests_refused", "paths", strings.Join(refused, ", ")))
	}
	run := lockRun{
		owner: t.SandboxRootID(), stage: stage, pre: d.runStartHeads(ctx, instanceID), listed: listed,
		owned: d.ownedLocks(ctx, t.ID, stage), ref: d.currentRef(ctx, t), now: time.Now().UTC(),
	}
	locked := 0
	var unchecked []string
	for _, pid := range t.ProjectIDs {
		if !d.projectRunsOnRunner(ctx, pid) {
			locked += d.lockProjectTests(ctx, t, &run, pid)
			continue
		}
		files, ok := reported[pid]
		if !ok {
			unchecked = append(unchecked, pid)
			continue
		}
		locked += d.lockRunnerTests(ctx, t, &run, pid, files)
	}
	d.warnUnchecked(ctx, t, unchecked)
	// The baseline moved: what a later stage's approval asked about is moot.
	d.waiveTestChanges(ctx, t, "", "task.approvals.events.tests_relocked")
	if locked == 0 {
		return
	}
	d.systemComment(ctx, t.ID, NoteOf("task.comments.tests_locked", "count", locked, "stage", d.stageNote(t, stage)))
	logx.Warn(d.store.RecordEvent(ctx, t.ID, EventTestsLocked, instanceID, "octopus",
		i18n.Text("task.labels.tests_locked_summary", "count", locked),
		map[string]any{"stage": string(stage), "count": locked, "ref": run.ref}),
		"task.log.record_tests_locked", "task", t.ID)
}

// How many files the run locked in one project.
func (d *Dispatcher) lockProjectTests(ctx context.Context, t *Task, run *lockRun, pid string) int {
	head, err := d.projects.WorktreeHead(ctx, pid, run.owner)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.lock_tests_failed", "task", t.ID, "project", pid, "err", err))
		return 0
	}
	tests := d.testsChangedByRun(ctx, t, run, pid, head)
	listed, err := d.projects.WorktreeFilesUnder(ctx, pid, run.owner, head, run.listed)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.lock_tests_failed", "task", t.ID, "project", pid, "err", err))
		return 0
	}
	blobs, err := d.projects.WorktreeBlobs(ctx, pid, run.owner, head, tests)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.lock_tests_failed", "task", t.ID, "project", pid, "err", err))
		return 0
	}
	base := d.defaultBlobs(ctx, pid, run.owner, tests)
	return d.storeLocks(ctx, t, run, pid, lockFiles{tests: tests, blobs: blobs, listed: listed, base: base, ref: run.ref})
}

// What a lock_tests run left in one project, read on the daemon's worktree or reported by a runner.
type lockFiles struct {
	// The test files the run added, changed or deleted.
	tests []string
	// Blobs at the run's end of tests (a deleted one is absent).
	blobs map[string]string
	// The files under the paths locked-tests.txt lists, with their blobs.
	listed map[string]string
	// Blobs of tests on the default branch; nil when it does not resolve.
	base map[string]string
	ref  string
}

// storeLocks records the files the run locks in one project and returns how many.
func (d *Dispatcher) storeLocks(ctx context.Context, t *Task, run *lockRun, pid string, f lockFiles) int {
	blobs := maps.Clone(f.blobs)
	if blobs == nil {
		blobs = map[string]string{}
	}
	for _, p := range f.tests {
		if _, ok := f.listed[p]; !ok && !run.locks(pid, p, blobs[p], f.base) {
			delete(blobs, p)
		}
	}
	// A listed file is locked whatever the globs say; a listed directory locks each file it holds
	// now (files added to it later are not locked). A listed path missing from this project belongs
	// to another one.
	maps.Copy(blobs, f.listed)
	paths := slices.Sorted(maps.Keys(blobs))
	locked := 0
	for _, p := range paths {
		err := d.store.PutLockedFile(ctx, LockedFile{TaskID: t.ID, ProjectID: pid, Path: p,
			BlobSHA: blobs[p], Stage: run.stage, LockedRef: f.ref, LockedAt: run.now})
		if err != nil {
			logx.Warn(err, "task.log.put_locked_file", "task", t.ID)
			continue
		}
		locked++
	}
	return locked
}

// The test files the run added, changed or deleted in the project, by the contract's globs.
func (d *Dispatcher) testsChangedByRun(ctx context.Context, t *Task, run *lockRun, pid, head string) []string {
	if run.pre[pid] == head {
		return nil
	}
	changed, err := d.projects.WorktreeChangedFiles(ctx, pid, run.owner, run.pre[pid], head)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.lock_tests_failed", "task", t.ID, "project", pid, "err", err))
	}
	var tests []string
	for _, p := range changed {
		if IsTestPath(p) {
			tests = append(tests, p)
		}
	}
	return tests
}

// Whether the run locks test file p of project pid, whose blob at HEAD is blob ("" when deleted).
func (r *lockRun) locks(pid, p, blob string, base map[string]string) bool {
	// A rebase onto the default branch shows its files as changed; those are not this stage's. A
	// file this stage locked before is its own whatever the default branch has: deleting it now must
	// move the baseline, or the next stage gets asked about the deletion.
	return r.owned[pid+"\x00"+p] || base == nil || blob != base[p]
}

// The files the stage already locked on this task, keyed by project and path joined with a NUL.
func (d *Dispatcher) ownedLocks(ctx context.Context, taskID string, stage Stage) map[string]bool {
	files, err := d.store.ListLockedFiles(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.list_locked_files", "task", taskID)
		return nil
	}
	out := map[string]bool{}
	for i := range files {
		if files[i].Stage == stage {
			out[files[i].ProjectID+"\x00"+files[i].Path] = true
		}
	}
	return out
}

// The blobs origin/<default> has for the paths; nil when the default branch does not resolve.
func (d *Dispatcher) defaultBlobs(ctx context.Context, pid, owner string, paths []string) map[string]string {
	if len(paths) == 0 {
		return nil
	}
	base := d.projects.WorktreeDefaultCommit(ctx, pid, owner)
	if base == "" {
		return nil
	}
	blobs, err := d.projects.WorktreeBlobs(ctx, pid, owner, base, paths)
	if err != nil {
		return nil
	}
	return blobs
}

// The locked files the default branch changed after the point each was locked from (the merge base
// of the commit that locked it and origin/<default>), mapped to their blob on the default branch.
// Only these may take the default branch's version without asking: a rebase brings that version. A
// file the default branch never touched going back to its blob there is a revert of the lock
// stage's work. A file whose locking commit no longer resolves is left out, so it asks.
func (d *Dispatcher) movedOnDefault(ctx context.Context, pid, owner string, list []LockedFile) map[string]string {
	def := d.projects.WorktreeDefaultCommit(ctx, pid, owner)
	if def == "" {
		return nil
	}
	byCommit := map[string][]string{}
	for i := range list {
		if c := parseRefs(list[i].LockedRef)[pid]; c != "" {
			byCommit[c] = append(byCommit[c], list[i].Path)
		}
	}
	out := map[string]string{}
	for commit, paths := range byCommit {
		mb := d.projects.WorktreeMergeBase(ctx, pid, owner, def, commit)
		if mb == "" {
			continue
		}
		then, err := d.projects.WorktreeBlobs(ctx, pid, owner, mb, paths)
		if err != nil {
			continue
		}
		now, err := d.projects.WorktreeBlobs(ctx, pid, owner, def, paths)
		if err != nil {
			continue
		}
		for _, p := range paths {
			if now[p] != then[p] {
				out[p] = now[p]
			}
		}
	}
	return out
}

func (d *Dispatcher) checkLockedTests(ctx context.Context, t *Task, stage Stage, reported map[string]runner.TestFiles) lockedTestsResult {
	byProject, err := d.lockedByOtherStages(ctx, t.ID, stage)
	if err != nil {
		return lockedTestsResult{Unchecked: true}
	}
	if len(byProject) == 0 {
		return lockedTestsResult{}
	}
	var changes []testChange
	var unchecked []string
	complete := true
	for _, pid := range t.ProjectIDs {
		list := byProject[pid]
		if len(list) == 0 {
			continue
		}
		var found []testChange
		var ok bool
		switch files, got := reported[pid]; {
		case !d.projectRunsOnRunner(ctx, pid):
			found, ok = d.projectTestChanges(ctx, t, pid, list)
		case got:
			found, ok = runnerTestChanges(pid, list, files)
		default:
			unchecked = append(unchecked, pid)
			continue
		}
		complete = complete && ok
		changes = append(changes, found...)
	}
	d.warnUnchecked(ctx, t, unchecked)
	switch {
	case len(changes) > 0:
		d.openTestChange(ctx, t, stage, changes)
	case complete:
		d.waiveTestChanges(ctx, t, stage, "task.approvals.events.tests_restored")
	default:
		// A worktree that could not be read proves nothing: a pending approval stays pending.
	}
	return lockedTestsResult{Touched: len(changes) > 0, Unchecked: len(unchecked) > 0 || !complete}
}

// The locked files of the task by project, leaving out the ones this stage locked itself.
func (d *Dispatcher) lockedByOtherStages(ctx context.Context, taskID string, stage Stage) (map[string][]LockedFile, error) {
	files, err := d.store.ListLockedFiles(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.list_locked_files", "task", taskID)
		return nil, err
	}
	byProject := map[string][]LockedFile{}
	for i := range files {
		if files[i].Stage != stage {
			byProject[files[i].ProjectID] = append(byProject[files[i].ProjectID], files[i])
		}
	}
	return byProject, nil
}

// The locked files of one project whose blob moved; false when the worktree could not be read.
func (d *Dispatcher) projectTestChanges(ctx context.Context, t *Task, pid string, list []LockedFile) ([]testChange, bool) {
	owner := t.SandboxRootID()
	head, err := d.projects.WorktreeHead(ctx, pid, owner)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.check_locked_tests_failed", "task", t.ID, "project", pid, "err", err))
		return nil, false
	}
	paths := make([]string, len(list))
	for i := range list {
		paths[i] = list[i].Path
	}
	blobs, err := d.projects.WorktreeBlobs(ctx, pid, owner, head, paths)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("task.log.check_locked_tests_failed", "task", t.ID, "project", pid, "err", err))
		return nil, false
	}
	upstream := d.movedOnDefault(ctx, pid, owner, list)
	var changes []testChange
	for i := range list {
		f := &list[i]
		now := blobs[f.Path]
		if now == f.BlobSHA {
			continue
		}
		// A rebase brought the default branch's version; deleting a locked test always counts.
		if moved, ok := upstream[f.Path]; ok && now != "" && now == moved {
			continue
		}
		changes = append(changes, testChange{Project: pid, Path: f.Path, Locked: f.BlobSHA, Now: now})
	}
	return changes, true
}

func (d *Dispatcher) pendingTestChanges(ctx context.Context, taskID string) []Approval {
	list, err := d.store.ListApprovals(ctx, taskID)
	if err != nil {
		logx.Warn(err, "task.log.list_test_changes", "task", taskID)
		return nil
	}
	var out []Approval
	for i := range list {
		if a := &list[i]; a.Source == ApprovalFromSystem && a.Key == ApprovalKeyTestChange && !a.Status.Settled() {
			out = append(out, *a)
		}
	}
	return out
}

func (d *Dispatcher) openTestChange(ctx context.Context, t *Task, stage Stage, changes []testChange) {
	payload, err := json.Marshal(testChangePayload{Ref: d.currentRef(ctx, t), Files: changes})
	if err != nil {
		return
	}
	names := make([]string, len(changes))
	for i, c := range changes {
		names[i] = fmt.Sprintf("`%s`", c.Path)
	}
	note := NoteOf("task.comments.locked_tests_changed", "files", strings.Join(names, ", "), "stage", d.stageNote(t, stage))
	pending := d.pendingTestChanges(ctx, t.ID)
	for i := range pending {
		a := &pending[i]
		if a.Stage != stage {
			continue
		}
		// Same question, newer list: one approval per stage, decided on what the last run left.
		// A changes_requested one goes back to pending, since the run that answered it changed them
		// again.
		logx.Warn(d.store.SetApprovalPayload(ctx, a.ID, payload), "task.log.update_test_change", "task", t.ID)
		if a.Status != ApprovalPending {
			_, err := d.store.SetApprovalStatus(ctx, a.ID, ApprovalPending, a.DecidedBy, a.DecidedNote, "")
			logx.Warn(err, "task.log.update_test_change", "task", t.ID)
		}
		d.systemComment(ctx, t.ID, note)
		return
	}
	a := &Approval{
		ID: mustGenerateID(), TaskID: t.ID, Key: ApprovalKeyTestChange,
		Label: i18n.Text("task.approvals.labels.test_change"), Stage: stage,
		Source: ApprovalFromSystem, Status: ApprovalPending, Payload: payload,
		CreatedBy: "octopus", CreatedAt: time.Now().UTC(),
	}
	if err := d.store.AddApproval(ctx, a); err != nil {
		logx.Warn(err, "task.log.open_test_change", "task", t.ID)
		return
	}
	d.systemComment(ctx, t.ID, note)
}

// stage "" waives them in every stage.
func (d *Dispatcher) waiveTestChanges(ctx context.Context, t *Task, stage Stage, summaryKey string) {
	pending := d.pendingTestChanges(ctx, t.ID)
	for i := range pending {
		a := &pending[i]
		if stage != "" && a.Stage != stage {
			continue
		}
		waived, err := d.store.SetApprovalStatus(ctx, a.ID, ApprovalWaived, "octopus", "", "")
		if err != nil {
			logx.Warn(err, "task.log.update_test_change", "task", t.ID)
			continue
		}
		d.store.recordApprovalEvent(ctx, EventApprovalWaived, waived, "octopus", summaryKey)
	}
}

// A person approved or waived the change: the files' current blobs become the baseline, so the next
// run does not ask again about the same edit. The stage that locked them stays the owner.
func (d *Dispatcher) relockTestChange(ctx context.Context, a *Approval) {
	if a == nil || a.Source != ApprovalFromSystem || a.Key != ApprovalKeyTestChange || !a.Status.Settled() {
		return
	}
	var p testChangePayload
	if len(a.Payload) == 0 || json.Unmarshal(a.Payload, &p) != nil {
		return
	}
	files, err := d.store.ListLockedFiles(ctx, a.TaskID)
	if err != nil {
		logx.Warn(err, "task.log.list_locked_files", "task", a.TaskID)
		return
	}
	for _, c := range p.Files {
		for _, f := range files {
			if f.ProjectID != c.Project || f.Path != c.Path {
				continue
			}
			f.BlobSHA, f.LockedRef, f.LockedAt = c.Now, p.Ref, time.Now().UTC()
			logx.Warn(d.store.PutLockedFile(ctx, f), "task.log.put_locked_file", "task", a.TaskID)
		}
	}
}

// A runner that did not report the blobs (one from before TestLock, or one that could not read its
// tree) leaves the daemon nothing to compare. Said once per task and project.
func (d *Dispatcher) warnUnchecked(ctx context.Context, t *Task, projects []string) {
	for _, pid := range projects {
		if d.store.hasTaskEvent(ctx, t.ID, EventLockedTestsUnchecked, pid) {
			continue
		}
		logx.Warn(d.store.RecordEvent(ctx, t.ID, EventLockedTestsUnchecked, pid, "octopus",
			NoteOf("task.comments.locked_tests_unenforceable", "project", pid).Text(),
			map[string]string{"project": pid}), "task.log.record_tests_locked", "task", t.ID)
	}
}
