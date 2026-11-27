package hostrun

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

func writeUnder(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A lock_tests run on the machine reports what it changed, the default branch's blobs for those
// files and the files its locked-tests.txt names; a later run reports the locked paths it was asked
// about. The blobs are git's, as the daemon reads them on its own worktrees.
func TestRunReportsTheBlobsTheTestLockAsks(t *testing.T) {
	m := newMachine(t)
	ctx := context.Background()
	// main already has a test the run will rebase over unchanged.
	writeUnder(t, m.checkout, "pkg/old_test.go", "package pkg // main\n")
	gitIn(t, m.checkout, "add", ".")
	gitIn(t, m.checkout, "commit", "-q", "-m", "old test")
	gitIn(t, m.checkout, "push", "-q", "origin", "HEAD:main")

	p := runner.JobProject{ID: "p1", Name: "app", DefaultBranch: "main", Branch: "task-1", RepoURL: m.remote}
	j := m.job(t, p, false)
	j.spec.InstanceID = "tester-task-1-test_design-1"
	j.spec.TestLock = &runner.TestLock{Lock: []string{"p1"}}
	if err := j.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	j.recordStarts(ctx)
	wt := j.wts["p1"]
	gitIn(t, wt, "config", "core.hooksPath", "/nowhere")
	writeUnder(t, wt, "pkg/a_test.go", "package pkg // contract\n")
	writeUnder(t, wt, "pkg/a.go", "package pkg\n")
	writeUnder(t, wt, "fixtures/data.json", "{}\n")
	writeUnder(t, j.root, filepath.Join("runs", j.spec.InstanceID, runner.LockedTestsFile), "fixtures\n../escape\n# note\n")
	if _, err := j.afterRun(ctx, 0); err != nil {
		t.Fatal(err)
	}

	got := j.testFiles(ctx)
	if len(got) != 1 {
		t.Fatalf("testFiles = %+v; want one project", got)
	}
	tf := got[0]
	head := gitIn(t, wt, "rev-parse", "HEAD")
	if tf.ProjectID != "p1" || tf.Head != head {
		t.Errorf("project/head = %s/%s; want p1/%s", tf.ProjectID, tf.Head, head)
	}
	slices.Sort(tf.Changed)
	if !slices.Equal(tf.Changed, []string{"fixtures/data.json", "pkg/a.go", "pkg/a_test.go"}) {
		t.Errorf("changed = %v", tf.Changed)
	}
	if want := gitIn(t, wt, "rev-parse", "HEAD:pkg/a_test.go"); tf.Blobs["pkg/a_test.go"] != want {
		t.Errorf("a_test.go blob = %q, want %q", tf.Blobs["pkg/a_test.go"], want)
	}
	if tf.Base == nil || len(tf.Base) != 0 {
		t.Errorf("base = %v; want an empty map: main has none of the changed files", tf.Base)
	}
	if want := gitIn(t, wt, "rev-parse", "HEAD:fixtures/data.json"); len(tf.Listed) != 1 || tf.Listed["fixtures/data.json"] != want {
		t.Errorf("listed = %v; want the file under fixtures/", tf.Listed)
	}

	// The next stage's run is asked about the locked paths; it deletes one.
	j2 := m.job(t, p, false)
	j2.spec.TestLock = &runner.TestLock{Check: map[string][]string{"p1": {"pkg/a_test.go", "pkg/old_test.go"}}}
	if err := j2.linkProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(wt, "pkg/old_test.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := j2.afterRun(ctx, 0); err != nil {
		t.Fatal(err)
	}
	got = j2.testFiles(ctx)
	if len(got) != 1 || !slices.Equal(got[0].Checked, []string{"pkg/a_test.go", "pkg/old_test.go"}) {
		t.Fatalf("check report = %+v", got)
	}
	if _, ok := got[0].Blobs["pkg/old_test.go"]; ok || got[0].Blobs["pkg/a_test.go"] == "" || got[0].Changed != nil {
		t.Errorf("check blobs = %v (changed %v); want a_test.go only, the deleted file absent", got[0].Blobs, got[0].Changed)
	}

	// A read-only run, or one without a TestLock, reports nothing.
	j2.spec.ReadOnly = true
	if got := j2.testFiles(ctx); got != nil {
		t.Errorf("a read-only run reported %+v", got)
	}
	j2.spec.ReadOnly, j2.spec.TestLock = false, nil
	if got := j2.testFiles(ctx); got != nil {
		t.Errorf("a run without a TestLock reported %+v", got)
	}
}
