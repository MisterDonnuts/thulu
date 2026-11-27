package task

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// A task whose only project lives on a runner, on the lock board.
func newRunnerLockRig(t *testing.T) *lockRig {
	t.Helper()
	d, store := newStageTestDispatcher(t, lockBoard())
	projects, err := project.NewStore(store.db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := projects.CreateOnRunner(t.Context(), "app", "", "runner1", project.CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	d.projects = projects
	tk := &Task{WorkspaceID: testWS, Title: "on a runner", Stage: "test_design", ProjectIDs: []string{proj.ID}}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	return &lockRig{d: d, store: store, projects: projects, pid: proj.ID, tk: tk}
}

// What the runner sent with the run's end, as the runner launcher hands it over.
func (r *lockRig) report(inst string, files runner.TestFiles) {
	files.ProjectID = r.pid
	r.d.RecordRunnerReport(context.Background(), r.tk.ID,
		RunnerReport{Runner: "mac", Instance: inst, TestFiles: []runner.TestFiles{files}})
}

func TestRunnerTestLockAsksWhatTheStageNeeds(t *testing.T) {
	r := newRunnerLockRig(t)
	ctx := t.Context()
	if got := r.d.RunnerTestLock(ctx, r.tk, "test_design"); got == nil || !slices.Equal(got.Lock, []string{r.pid}) || got.Check != nil {
		t.Errorf("lock stage asks %+v; want the runner project's changes", got)
	}
	if got := r.d.RunnerTestLock(ctx, r.tk, "code"); got != nil {
		t.Errorf("a stage with nothing locked yet asks %+v; want nothing", got)
	}
	for _, p := range []string{"pkg/a_test.go", "pkg/b_test.go"} {
		if err := r.store.PutLockedFile(ctx, LockedFile{TaskID: r.tk.ID, ProjectID: r.pid, Path: p, BlobSHA: "abc", Stage: "test_design"}); err != nil {
			t.Fatal(err)
		}
	}
	got := r.d.RunnerTestLock(ctx, r.tk, "code")
	if got == nil || len(got.Lock) != 0 || !slices.Equal(got.Check[r.pid], []string{"pkg/a_test.go", "pkg/b_test.go"}) {
		t.Errorf("a later stage asks %+v; want the locked paths read", got)
	}
	if got := r.d.RunnerTestLock(ctx, r.tk, "release"); got != nil {
		t.Errorf("a release stage asks %+v; want nothing", got)
	}
	plain := &Task{ID: "x", WorkspaceID: testWS, Stage: "code"}
	if got := r.d.RunnerTestLock(ctx, plain, "code"); got != nil {
		t.Errorf("a task without a runner project asks %+v", got)
	}
}

// The runner's report stands in for the daemon's worktree: the lock stage locks the tests it wrote
// and the files its locked-tests.txt names, and a later stage that changes one opens test_change.
func TestLockedTestsOnARunnerAreEnforcedFromItsReport(t *testing.T) {
	r := newRunnerLockRig(t)
	ctx := t.Context()
	out := t.TempDir()
	writeIn(t, out, lockedTestsFile, "README.md\n../escape\n")
	r.report("tester-task-x-test_design-1", runner.TestFiles{
		Head:    "c0ffee",
		Changed: []string{"pkg/a_test.go", "pkg/a.go", "pkg/up_test.go", "pkg/gone_test.go"},
		Blobs:   map[string]string{"pkg/a_test.go": "blobA", "pkg/a.go": "blobGo", "pkg/up_test.go": "blobUp"},
		// up_test.go only came along with a rebase onto main: main has the same blob.
		Base: map[string]string{"pkg/up_test.go": "blobUp", "pkg/gone_test.go": "blobGone"},
		// The runner read more than the daemon accepts from the list.
		Listed: map[string]string{"README.md": "blobReadme", "escape": "blobEscape"},
	})
	r.d.lockedTestsAfterRun(ctx, r.tk, "test_design", "tester-task-x-test_design-1", out, 0)

	got := lockedPaths(t, r.store, r.tk.ID)
	want := map[string]string{"pkg/a_test.go": "blobA", "README.md": "blobReadme"}
	if len(got) != len(want) {
		t.Errorf("locked %v; want %v", got, want)
	}
	for p, blob := range want {
		if f := got[p]; f.BlobSHA != blob || f.Stage != "test_design" || f.LockedRef != r.pid+"@c0ffee" {
			t.Errorf("%s locked as %+v; want blob %s", p, f, blob)
		}
	}
	if ev := eventsOf(t, r.store, r.tk.ID, EventLockedTestsUnchecked); len(ev) != 0 {
		t.Errorf("a reported lock still warned it could not be checked: %v", ev)
	}

	tk := moveTo(t, r.store, r.tk.ID, "code")
	checked := []string{"README.md", "pkg/a_test.go"}
	r.report("dev-task-x-code-2", runner.TestFiles{Head: "beef", Checked: checked,
		Blobs: map[string]string{"pkg/a_test.go": "blobA2", "README.md": "blobReadme"}})
	if res := r.d.lockedTestsAfterRun(ctx, tk, "code", "dev-task-x-code-2", "", 0); !res.Touched {
		t.Errorf("result = %+v, want the changed test reported", res)
	}
	a := pendingTestChange(t, r.d, tk.ID)
	if a == nil || a.Stage != "code" {
		t.Fatalf("test_change = %+v; want one pending in code", a)
	}

	// The next run puts the test back: the approval is moot.
	r.report("dev-task-x-code-3", runner.TestFiles{Head: "f00d", Checked: checked,
		Blobs: map[string]string{"pkg/a_test.go": "blobA", "README.md": "blobReadme"}})
	r.d.lockedTestsAfterRun(ctx, tk, "code", "dev-task-x-code-3", "", 0)
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Errorf("test_change still pending after the test came back: %+v", a)
	}

	// A path the runner was not asked about proves nothing: no approval, and none waived.
	r.report("dev-task-x-code-4", runner.TestFiles{Head: "f00d", Checked: []string{"pkg/a_test.go"},
		Blobs: map[string]string{"pkg/a_test.go": "blobA"}})
	if res := r.d.lockedTestsAfterRun(ctx, tk, "code", "dev-task-x-code-4", "", 0); !res.Unchecked || res.Touched {
		t.Errorf("result = %+v for a partial report, want unchecked", res)
	}
	if a := pendingTestChange(t, r.d, tk.ID); a != nil {
		t.Errorf("an unread path opened %+v", a)
	}

	// A runner that sent nothing (an old one) leaves the lock unchecked, said once.
	if res := r.d.lockedTestsAfterRun(ctx, tk, "code", "dev-task-x-code-5", "", 0); !res.Unchecked {
		t.Errorf("result = %+v for a runner that sent nothing, want unchecked", res)
	}
	if ev := eventsOf(t, r.store, r.tk.ID, EventLockedTestsUnchecked); len(ev) != 1 {
		t.Errorf("unchecked warnings = %d; want one for the run without a report", len(ev))
	}
}

func TestRunnerTestStashForgetsWhatNobodyTakes(t *testing.T) {
	var s runnerTestStash
	for i := range runnerTestStashMax + 10 {
		s.put(string(rune('a'+i%26))+string(rune('0'+i/26)), []runner.TestFiles{{ProjectID: "p"}})
	}
	if len(s.byRun) != runnerTestStashMax || len(s.order) != runnerTestStashMax {
		t.Errorf("stash holds %d (%d in order); want at most %d", len(s.byRun), len(s.order), runnerTestStashMax)
	}
	s.put("run", []runner.TestFiles{{ProjectID: "p1", Head: "h"}})
	if got := s.take("run"); got["p1"].Head != "h" {
		t.Errorf("take = %+v", got)
	}
	if got := s.take("run"); got != nil {
		t.Errorf("a second take = %+v; want nothing", got)
	}
}
