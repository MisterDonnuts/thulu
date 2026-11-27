package task

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testgit"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestIsTestPath(t *testing.T) {
	for p, want := range map[string]bool{
		"internal/x/y_test.go":          true,
		"web/src/a.test.ts":             true,
		"web/src/a.test.tsx":            true,
		"lib/a.test.js":                 true,
		"lib/a.test.jsx":                true,
		"lib/a.test.mjs":                false,
		"e2e/login.spec.ts":             true,
		"spec/models/user.spec.rb":      true,
		"test_models.py":                true,
		"pkg/models_test.py":            true,
		"tests/fixtures/data.json":      true,
		"packages/ui/tests/x.ts":        true,
		"src/__tests__/a.tsx":           true,
		"src/test/java/a/B.java":        true,
		"app/src/test/kotlin/A.kt":      true,
		"src/main/java/a/UserTest.java": true,
		"src/main/java/a/User.java":     false,
		"test/unit.go":                  false,
		"internal/x/y.go":               false,
		"testdata/a.txt":                false,
		"README.md":                     false,
		"contest.py":                    false,
	} {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestParseLockedList(t *testing.T) {
	paths, refused := parseLockedList("# keep these\nint/a_test.go\n\n./int/b_test.go\nint/a_test.go\n../etc/passwd\n/abs/c_test.go\n")
	if strings.Join(paths, ",") != "int/a_test.go,int/b_test.go" {
		t.Errorf("paths = %v", paths)
	}
	if strings.Join(refused, ",") != "../etc/passwd,/abs/c_test.go" {
		t.Errorf("refused = %v", refused)
	}
}

func lockBoard() stagepkg.Registry {
	return stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "intake", Label: "Intake", Position: 10, Initial: true},
		{ID: "test_design", Label: "Test design", Position: 20, LockTests: true},
		{ID: "code", Label: "Code", Position: 30, AutoAdvance: true},
		{ID: "release", Label: "Release", Position: 40, ReleaseResources: true},
		{ID: "shipped", Label: "Shipped", Position: 50, Terminal: true},
	})
}

type lockRig struct {
	d        *Dispatcher
	store    *Store
	projects *project.Store
	remote   string
	pid      string
	tk       *Task
	wt       string
}

func lockGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeIn(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newLockRig(t *testing.T) *lockRig {
	t.Helper()
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	d, store := newStageTestDispatcher(t, lockBoard())
	projects, err := project.NewStore(store.db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	remote := testgit.BareRepo(t)
	proj, err := projects.Create(t.Context(), "web", remote, project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	d.projects = projects
	tk := &Task{WorkspaceID: testWS, Title: "locked", Stage: "test_design", ProjectIDs: []string{proj.ID}}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	tk = d.ensureWorktrees(t.Context(), tk)
	wt := tk.WorktreePaths[proj.ID]
	if wt == "" {
		t.Fatalf("no worktree: %+v", tk)
	}
	return &lockRig{d: d, store: store, projects: projects, remote: remote, pid: proj.ID, tk: tk, wt: wt}
}

// What a run does to the code: edit, then the daemon commits as postStageRun would.
func (r *lockRig) commit(t *testing.T, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		writeIn(t, r.wt, rel, body)
	}
	if err := r.projects.CommitWorktree(t.Context(), r.pid, r.tk.ID, "run"); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// Starts a lock_tests run the way launch records it, and returns its instance id.
func (r *lockRig) startLockRun(t *testing.T, n string) string {
	t.Helper()
	inst := "tester-task-" + r.tk.ID + "-test_design-" + n
	heads := r.d.lockStageHeads(t.Context(), r.tk, "test_design")
	if heads == "" {
		t.Fatal("a lock_tests run recorded no heads")
	}
	if err := r.store.RecordEvent(t.Context(), r.tk.ID, EventAgentRunStarted, inst, "agent:tester", "",
		map[string]string{"heads": heads, "stage": "test_design"}); err != nil {
		t.Fatal(err)
	}
	return inst
}

// An upstream commit on main, pushed to the remote and fetched into the clone.
func (r *lockRig) upstream(t *testing.T, files map[string]string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "up")
	testgit.Clone(t, r.remote, src)
	for rel, body := range files {
		writeIn(t, src, rel, body)
	}
	lockGit(t, src, "add", ".")
	lockGit(t, src, "commit", "-m", "upstream")
	lockGit(t, src, "push", "origin", "HEAD:main")
	lockGit(t, r.wt, "fetch", "origin")
}

func lockedPaths(t *testing.T, store *Store, taskID string) map[string]LockedFile {
	t.Helper()
	files, err := store.ListLockedFiles(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]LockedFile{}
	for _, f := range files {
		out[f.Path] = f
	}
	return out
}

func pendingTestChange(t *testing.T, d *Dispatcher, taskID string) *Approval {
	t.Helper()
	list := d.pendingTestChanges(t.Context(), taskID)
	if len(list) > 1 {
		t.Fatalf("%d pending test_change approvals, want at most one", len(list))
	}
	if len(list) == 0 {
		return nil
	}
	return &list[0]
}

func TestLockTestsRecordsWhatTheStageWrote(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	// A test file already on main before the task; it changes upstream during the lock run.
	r.upstream(t, map[string]string{"pkg/up_test.go": "package pkg // v1\n"})
	lockGit(t, r.wt, "rebase", "origin/main")

	inst := r.startLockRun(t, "1")
	r.upstream(t, map[string]string{"pkg/up_test.go": "package pkg // v2\n"})
	// The run rebases onto main (bringing up_test.go v2) and writes its own tests.
	lockGit(t, r.wt, "rebase", "origin/main")
	r.commit(t, map[string]string{
		"pkg/a_test.go":           "package pkg // contract\n",
		"web/src/__tests__/b.tsx": "test('b')\n",
		"pkg/a.go":                "package pkg\n",
	})
	out := t.TempDir()
	writeIn(t, out, lockedTestsFile, "README.md\n../escape\nnot/there_test.go\n")
	r.d.lockTests(ctx, r.tk, "test_design", inst, out, nil)

	got := lockedPaths(t, r.store, r.tk.ID)
	for _, p := range []string{"pkg/a_test.go", "web/src/__tests__/b.tsx", "README.md"} {
		if f, ok := got[p]; !ok || f.BlobSHA == "" || f.Stage != "test_design" {
			t.Errorf("%s locked as %+v (ok %v)", p, f, ok)
		}
	}
	for _, p := range []string{"pkg/a.go", "pkg/up_test.go", "not/there_test.go", "../escape"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s locked: not a test this stage wrote", p)
		}
	}
	if ev := eventsOf(t, r.store, r.tk.ID, EventTestsLocked); len(ev) != 1 || ev[0]["count"] != float64(3) {
		t.Errorf("tests_locked events = %v", ev)
	}
	var refused bool
	full, _ := r.store.Get(ctx, r.tk.ID)
	for _, c := range full.Comments {
		if strings.Contains(c.Body, "../escape") {
			refused = true
		}
	}
	if !refused {
		t.Error("no note names the path outside the repository")
	}
}

func TestLockedTestChangeHoldsTheTaskUntilAPersonDecides(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	inst := r.startLockRun(t, "1")
	r.commit(t, map[string]string{"pkg/a_test.go": "package pkg // contract\n", "pkg/keep_test.go": "package pkg\n"})
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)

	tk := moveTo(t, r.store, r.tk.ID, "code")
	// Code that leaves the tests alone passes.
	r.commit(t, map[string]string{"pkg/a.go": "package pkg\n"})
	if res := r.d.checkLockedTests(ctx, tk, "code", nil); res != (lockedTestsResult{}) {
		t.Errorf("result = %+v with the tests untouched, want nothing for the auditor", res)
	}
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Fatalf("approval opened with the tests untouched: %+v", a)
	}

	// Weakening a test opens the approval in the current stage and holds auto-advance.
	r.commit(t, map[string]string{"pkg/a_test.go": "package pkg // weakened\n"})
	if res := r.d.checkLockedTests(ctx, tk, "code", nil); !res.Touched || res.Unchecked {
		t.Errorf("result = %+v, want the change reported to the auditor", res)
	}
	a := pendingTestChange(t, r.d, tk.ID)
	if a == nil || a.Stage != "code" || a.Source != ApprovalFromSystem || a.Key != ApprovalKeyTestChange {
		t.Fatalf("approval = %+v", a)
	}
	var p testChangePayload
	if err := json.Unmarshal(a.Payload, &p); err != nil || len(p.Files) != 1 || p.Files[0].Path != "pkg/a_test.go" || p.Files[0].Now == "" {
		t.Fatalf("payload = %s (%v)", a.Payload, err)
	}
	r.d.autoAdvanceStage(ctx, tk, tk.ID, "code", 0, "")
	if held, _ := r.store.Get(ctx, tk.ID); held.Stage != "code" || held.Status != StatusAwaitingApproval {
		t.Fatalf("after auto-advance: %q/%q, want code/awaiting_approval", held.Stage, held.Status)
	}

	// The run after the fix restores the test: the approval is waived by the daemon.
	r.commit(t, map[string]string{"pkg/a_test.go": "package pkg // contract\n"})
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Fatalf("approval still pending after the test came back: %+v", a)
	}

	// Deleting a locked test counts too; a person approves, and the deletion becomes the baseline.
	if err := os.Remove(filepath.Join(r.wt, "pkg/keep_test.go")); err != nil {
		t.Fatal(err)
	}
	r.commit(t, nil)
	r.d.checkLockedTests(ctx, tk, "code", nil)
	a = pendingTestChange(t, r.d, tk.ID)
	if a == nil {
		t.Fatal("deleting a locked test opened no approval")
	}
	// Idle, so the decision leaves the task in code and the next check still has its worktree
	// (moving on would enter release, which frees it).
	r.d.setStatus(ctx, tk.ID, StatusIdle)
	if _, err := r.d.DecideApproval(ctx, tk, a.ID, ApprovalApproved, "ana@x", "the test was wrong"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if f := lockedPaths(t, r.store, tk.ID)["pkg/keep_test.go"]; f.BlobSHA != "" || f.Stage != "test_design" {
		t.Errorf("after approval the lock is %+v, want the deletion as baseline, still owned by test_design", f)
	}
	// The next run does not ask again about the same edit, and the approval did not reopen.
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Errorf("approved change asked again: %+v", a)
	}
	approved := *a
	if _, err := r.store.ResetStaleApprovals(ctx, tk.ID, "code", nil, "something-else"); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.store.GetApproval(ctx, approved.ID); again.Status == ApprovalPending {
		t.Error("ResetStaleApprovals reopened an approved test_change")
	}
}

func TestLockedTestTakingTheDefaultBranchVersionIsNotAChange(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	inst := r.startLockRun(t, "1")
	r.commit(t, map[string]string{"pkg/a_test.go": "package pkg // ours\n"})
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)
	tk := moveTo(t, r.store, r.tk.ID, "code")

	// Someone lands the same file on main; the task rebases and takes main's version.
	r.upstream(t, map[string]string{"pkg/a_test.go": "package pkg // main's\n"})
	lockGit(t, r.wt, "checkout", "origin/main", "--", "pkg/a_test.go")
	r.commit(t, nil)
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Errorf("taking origin/main's blob opened %+v", a)
	}
}

// Sent back to test design, the stage deletes a test it had locked. The deletion is its call: the
// next stage must not be asked about it.
func TestLockStageDeletingItsOwnTestMovesTheBaseline(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	inst := r.startLockRun(t, "1")
	r.commit(t, map[string]string{"pkg/a_test.go": "package pkg // contract\n", "pkg/old_test.go": "package pkg\n"})
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)
	if f := lockedPaths(t, r.store, r.tk.ID)["pkg/old_test.go"]; f.BlobSHA == "" {
		t.Fatalf("old_test.go not locked: %+v", f)
	}

	inst = r.startLockRun(t, "2")
	if err := os.Remove(filepath.Join(r.wt, "pkg/old_test.go")); err != nil {
		t.Fatal(err)
	}
	r.commit(t, nil)
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)
	if f, ok := lockedPaths(t, r.store, r.tk.ID)["pkg/old_test.go"]; !ok || f.BlobSHA != "" {
		t.Fatalf("after the stage deleted it the lock is %+v (ok %v), want the deletion as baseline", f, ok)
	}

	tk := moveTo(t, r.store, r.tk.ID, "code")
	r.commit(t, map[string]string{"pkg/a.go": "package pkg\n"})
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Errorf("the next stage was asked about the lock stage's own deletion: %+v", a)
	}
}

// A worktree the check cannot read proves nothing: the pending approval stays, or the task would
// advance past a changed test.
func TestUnreadableWorktreeKeepsTheTestChangePending(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	inst := r.startLockRun(t, "1")
	r.commit(t, map[string]string{"pkg/a_test.go": "package pkg // contract\n"})
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)
	tk := moveTo(t, r.store, r.tk.ID, "code")
	r.commit(t, map[string]string{"pkg/a_test.go": "package pkg // weakened\n"})
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if pendingTestChange(t, r.d, tk.ID) == nil {
		t.Fatal("no approval for the weakened test")
	}

	if err := os.RemoveAll(r.wt); err != nil {
		t.Fatal(err)
	}
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if pendingTestChange(t, r.d, tk.ID) == nil {
		t.Error("the approval was waived on a worktree the check could not read")
	}
}

func TestLockedTestsOnARunnerOnlyWarn(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	if err := r.store.PutLockedFile(ctx, LockedFile{TaskID: r.tk.ID, ProjectID: r.pid, Path: "a_test.go", BlobSHA: "abc1234", Stage: "test_design"}); err != nil {
		t.Fatal(err)
	}
	r.d.warnUnchecked(ctx, r.tk, []string{r.pid})
	r.d.warnUnchecked(ctx, r.tk, []string{r.pid})
	if ev := eventsOf(t, r.store, r.tk.ID, EventLockedTestsUnchecked); len(ev) != 1 {
		t.Errorf("warned %d times, want once per task and project", len(ev))
	}
	if a := pendingTestChange(t, r.d, r.tk.ID); a != nil {
		t.Errorf("a runner project opened %+v", a)
	}
}

// Test design rewrote a test that already existed on main; Development puts main's version back.
// Main never touched the file after the task left it, so taking its blob is a revert of the locked
// work, not a rebase.
func TestRevertingALockedTestToTheDefaultBranchVersionOpensAnApproval(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	r.upstream(t, map[string]string{"pkg/old_test.go": "package pkg // v1\n"})
	lockGit(t, r.wt, "rebase", "origin/main")

	inst := r.startLockRun(t, "1")
	r.commit(t, map[string]string{"pkg/old_test.go": "package pkg // v1 plus the new case\n"})
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)
	if f := lockedPaths(t, r.store, r.tk.ID)["pkg/old_test.go"]; f.BlobSHA == "" {
		t.Fatalf("the rewritten test was not locked: %+v", f)
	}
	tk := moveTo(t, r.store, r.tk.ID, "code")

	lockGit(t, r.wt, "checkout", "origin/main", "--", "pkg/old_test.go")
	r.commit(t, nil)
	r.d.checkLockedTests(ctx, tk, "code", nil)
	a := pendingTestChange(t, r.d, tk.ID)
	if a == nil {
		t.Fatal("going back to main's version of a locked test opened no approval")
	}
	var p testChangePayload
	if err := json.Unmarshal(a.Payload, &p); err != nil || len(p.Files) != 1 || p.Files[0].Path != "pkg/old_test.go" {
		t.Fatalf("payload = %s (%v)", a.Payload, err)
	}
}

// Main changed a locked test after the task branched, and a real rebase resolved the conflict with
// main's side: the blob the task now has is main's, and nobody on the task chose it.
func TestARebaseOntoAMovedDefaultBranchOpensNoApproval(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	r.upstream(t, map[string]string{"pkg/old_test.go": "package pkg // v1\n"})
	lockGit(t, r.wt, "rebase", "origin/main")

	inst := r.startLockRun(t, "1")
	r.commit(t, map[string]string{"pkg/old_test.go": "package pkg // v2, the task's\n"})
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)
	tk := moveTo(t, r.store, r.tk.ID, "code")

	r.upstream(t, map[string]string{"pkg/old_test.go": "package pkg // v3, main's\n"})
	// In a rebase "ours" is the branch being rebased onto: main's side wins the conflict.
	lockGit(t, r.wt, "rebase", "-X", "ours", "origin/main")
	if got := lockGit(t, r.wt, "show", "HEAD:pkg/old_test.go"); got != "package pkg // v3, main's" {
		t.Fatalf("after the rebase the test reads %q", got)
	}
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Errorf("a rebase onto main's newer version opened %+v", a)
	}
}

// A directory in locked-tests.txt locks every file it holds, including the ones the run did not
// write, and a later edit to one of them asks a person.
func TestALockedDirectoryLocksTheFilesInIt(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	r.upstream(t, map[string]string{"testdata/golden/old.txt": "old\n"})
	lockGit(t, r.wt, "rebase", "origin/main")

	inst := r.startLockRun(t, "1")
	r.commit(t, map[string]string{"testdata/golden/new.txt": "new\n", "testdata/golden/deep/x.json": "{}\n"})
	out := t.TempDir()
	writeIn(t, out, lockedTestsFile, "testdata/golden/\n")
	r.d.lockTests(ctx, r.tk, "test_design", inst, out, nil)
	got := lockedPaths(t, r.store, r.tk.ID)
	for _, p := range []string{"testdata/golden/old.txt", "testdata/golden/new.txt", "testdata/golden/deep/x.json"} {
		if f, ok := got[p]; !ok || f.BlobSHA == "" {
			t.Errorf("%s locked as %+v (ok %v)", p, f, ok)
		}
	}
	if _, ok := got["testdata/golden"]; ok {
		t.Error("the directory itself was stored as a locked file")
	}

	tk := moveTo(t, r.store, r.tk.ID, "code")
	r.commit(t, map[string]string{"testdata/golden/old.txt": "edited\n"})
	r.d.checkLockedTests(ctx, tk, "code", nil)
	if a := pendingTestChange(t, r.d, tk.ID); a == nil {
		t.Error("editing a file of a locked directory opened no approval")
	}
}

// The agent of a lock_tests run commits in its first seconds, before the daemon would have read
// the worktree after the container started. That commit is the run's and gets locked.
type committingLauncher struct {
	*recordingLauncher
	onLaunch func()
}

func (l *committingLauncher) LaunchForStage(ctx context.Context, tk *Task, s Stage, at *types.AgentType,
	env, worktrees, stageDirs map[string]string, readOnly bool,
) (string, string, string, error) {
	cid, inst, out, err := l.recordingLauncher.LaunchForStage(ctx, tk, s, at, env, worktrees, stageDirs, readOnly)
	l.onLaunch()
	return cid, inst, out, err
}

func TestLockRunHeadsAreReadBeforeTheContainerStarts(t *testing.T) {
	r := newLockRig(t)
	ctx := context.Background()
	before := lockGit(t, r.wt, "rev-parse", "HEAD")
	l := &committingLauncher{recordingLauncher: newRecordingLauncher(), onLaunch: func() {
		r.commit(t, map[string]string{"pkg/early_test.go": "package pkg // first seconds\n"})
	}}
	r.d.launcher = l
	r.d.launchStage(ctx, r.tk, "test_design", "tester", &types.AgentType{Name: "tester"}, launchOpts{})
	if len(l.envs) != 1 {
		t.Fatalf("%d launches, want 1", len(l.envs))
	}
	inst := l.envs[0]["INSTANCE_ID"]
	if heads := r.d.runStartHeads(ctx, inst); heads[r.pid] != before {
		t.Fatalf("run start heads = %v, want %s (the HEAD before the container started)", heads, before)
	}
	r.d.lockTests(ctx, r.tk, "test_design", inst, "", nil)
	if f, ok := lockedPaths(t, r.store, r.tk.ID)["pkg/early_test.go"]; !ok || f.BlobSHA == "" {
		t.Errorf("the commit made right after launch was not locked: %+v (ok %v)", f, ok)
	}
}
