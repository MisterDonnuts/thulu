package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func waitClone(t *testing.T, store *Store, name, repo string) (*Project, *Project) {
	t.Helper()
	done := make(chan *Project, 1)
	proj, err := store.CreateInBackground(t.Context(), name, repo, CreateOpts{WorkspaceID: testWS},
		func(p *Project) { done <- p })
	if err != nil {
		t.Fatalf("CreateInBackground: %v", err)
	}
	return proj, <-done
}

func TestCreateInBackgroundAnswersBeforeTheClone(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)

	proj, out := waitClone(t, store, "Bg", repo)
	if proj.CloneStatus != CloneCloning {
		t.Errorf("returned CloneStatus = %q, want %q", proj.CloneStatus, CloneCloning)
	}
	if out.CloneStatus != "" || out.CloneError != "" {
		t.Fatalf("clone ended as %q (%q), want ready", out.CloneStatus, out.CloneError)
	}
	got, err := store.Get(t.Context(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CloneStatus != "" || got.DefaultBranch != "main" {
		t.Errorf("stored row = status %q branch %q, want ready on main", got.CloneStatus, got.DefaultBranch)
	}
	if _, err := os.Stat(filepath.Join(got.Path, "README.md")); err != nil {
		t.Errorf("README not cloned: %v", err)
	}
	if _, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "abcdef0123456789"); err != nil {
		t.Errorf("a ready project must take worktrees: %v", err)
	}
}

func TestCreateInBackgroundRecordsTheFailure(t *testing.T) {
	store := newTestStore(t)
	broken := testgit.BareRepo(t)
	if err := os.WriteFile(filepath.Join(broken, "HEAD"), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	proj, out := waitClone(t, store, "Broken", broken)
	if out.CloneStatus != CloneFailed || out.CloneError == "" {
		t.Fatalf("clone ended as %q (%q), want failed with a reason", out.CloneStatus, out.CloneError)
	}
	got, err := store.Get(t.Context(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CloneStatus != CloneFailed || got.CloneError == "" {
		t.Errorf("stored row = %q (%q), want failed with the reason", got.CloneStatus, got.CloneError)
	}
	if strings.Contains(got.CloneError, "git clone") || strings.Contains(got.CloneError, "\n") {
		t.Errorf("CloneError = %q, want one sentence", got.CloneError)
	}
	if got.CloneDetail == "" || strings.Contains(got.CloneDetail, got.Path) {
		t.Errorf("CloneDetail = %q, want git's output without %s", got.CloneDetail, got.Path)
	}
	if _, err := os.Stat(got.Path); !os.IsNotExist(err) {
		t.Errorf("the half clone must go: stat = %v", err)
	}
	_, _, err = store.CreateTaskWorktree(t.Context(), proj.ID, "abcdef0123456789")
	if apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("worktree on a failed clone = %v, want a precondition error", err)
	}
	if wts, err := store.ListWorktrees(t.Context(), proj.ID); err != nil || len(wts) != 0 {
		t.Errorf("ListWorktrees on a failed clone = %v, %v; want an empty list", wts, err)
	}
}

func TestWorktreeWaitsForTheClone(t *testing.T) {
	store := newTestStore(t)
	proj := &Project{Name: "Pending", CloneStatus: CloneCloning}
	if _, err := store.addWorktree(proj, "abcdef0123456789", "origin/main", nil); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("worktree while cloning = %v, want a conflict", err)
	}
}

func TestFailInterruptedClonesMarksStuckRows(t *testing.T) {
	store := newTestStore(t)
	dir := filepath.Join(store.baseDir, "stuck")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	proj := &Project{ID: "stuck0000000001", Name: "Stuck", Path: dir, RepoURL: "https://example.com/o/r.git",
		WorkspaceID: testWS, Source: SourceGit, CloneStatus: CloneCloning, StageTemplate: map[string]string{}}
	if err := store.insertProject(t.Context(), proj, "{}"); err != nil {
		t.Fatal(err)
	}
	store.FailInterruptedClones(t.Context())
	got, err := store.Get(t.Context(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CloneStatus != CloneFailed || got.CloneError == "" {
		t.Errorf("stuck row = %q (%q), want failed with a reason", got.CloneStatus, got.CloneError)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the half clone must go: stat = %v", err)
	}
}

func TestRootFilesListsTheTopOfTheDefaultBranch(t *testing.T) {
	store := newTestStore(t)
	proj := newUploadProject(t, store, "Roots")
	files, commit, err := store.RootFiles(t.Context(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, ",") != "README.md,a.txt,b.txt" || commit == "" {
		t.Errorf("RootFiles = %v at %q, want README.md,a.txt,b.txt", files, commit)
	}
}

func TestCloneFailureNamesTheCommonCauses(t *testing.T) {
	for _, tc := range []struct{ output, key string }{
		{"fatal: unable to access 'https://1.1.1.1/o/r.git/': Failed to connect to 1.1.1.1 port 443 after 3 ms: Couldn't connect to server", "project.errors.clone_unreachable"},
		{"fatal: unable to access 'https://nx.example/o/r/': Could not resolve host: nx.example", "project.errors.clone_host_unknown"},
		{"remote: Repository not found.\nfatal: repository 'https://github.com/o/r/' not found", "project.errors.clone_repo_missing"},
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", "project.errors.clone_auth"},
		{"remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/o/r/'", "project.errors.clone_auth"},
		{"fatal: unable to access 'https://github.com/o/r/': The requested URL returned error: 403", "project.errors.clone_auth"},
		{"fatal: unable to access 'https://h/o/r/': SSL certificate problem: self-signed certificate", "project.errors.clone_tls"},
		{"warning: Could not find remote branch dev to clone.\nfatal: Remote branch dev not found in upstream origin", "project.errors.clone_branch_missing"},
		{"fatal: something nobody planned for", "project.errors.clone_git"},
	} {
		err := cloneFailure(tc.output)
		if got := apperr.Code(err); got != tc.key {
			t.Errorf("%q: key = %s, want %s", tc.output, got, tc.key)
		}
		if !strings.Contains(err.Error(), tc.output) {
			t.Errorf("%q: git's output must stay as the cause: %v", tc.output, err)
		}
	}
}

func TestCloneOutputDropsSecretsAndPaths(t *testing.T) {
	dest := "/data/data/projects/r1-1-1-1"
	repo := "https://u:pw@h.example/o/r.git"
	e := &apperr.Error{Key: gitCommandKey, Err: errors.New("exit status 128"), Args: []any{
		"args", "-c url.x:tok@h/.insteadOf=h clone -- " + repo + " " + dest,
		"output", "Cloning into '" + dest + "'...\nfatal: unable to access '" + repo + "/': tok refused\n",
	}}
	out := cloneOutput(e, "tok", repo, dest)
	for _, bad := range []string{dest, "Cloning into", "tok", "pw@"} {
		if strings.Contains(out, bad) {
			t.Errorf("output %q still has %q", out, bad)
		}
	}
	if !strings.HasPrefix(out, "fatal: unable to access") {
		t.Errorf("output = %q, want git's fatal line", out)
	}
}

func TestCloneReasonInTheCallersLanguage(t *testing.T) {
	ctx := i18n.WithLocale(context.Background(), "es")
	reason, detail := cloneReason(ctx, cloneFailure("fatal: Could not resolve host: nx.example"))
	if reason != i18n.T("es", "project.errors.clone_host_unknown") {
		t.Errorf("reason = %q, want the Spanish sentence alone", reason)
	}
	if detail != "fatal: Could not resolve host: nx.example" {
		t.Errorf("detail = %q, want git's output", detail)
	}
	reason, detail = cloneReason(ctx, apperr.Timeout("project.errors.clone_timeout"))
	if reason != i18n.T("es", "project.errors.clone_timeout") || detail != "" {
		t.Errorf("timeout = %q / %q, want the sentence and no detail", reason, detail)
	}
}

func masterOnlyRepo(t *testing.T) string {
	t.Helper()
	repo := testgit.BareRepo(t)
	for _, args := range [][]string{
		{"branch", "-m", "main", "master"},
		{"symbolic-ref", "HEAD", "refs/heads/master"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func TestCreateReadsTheRepositoryBranch(t *testing.T) {
	store := newTestStore(t)
	proj, out := waitClone(t, store, "Hello", masterOnlyRepo(t))
	if out.CloneStatus != "" {
		t.Fatalf("clone ended as %q (%q), want ready", out.CloneStatus, out.CloneError)
	}
	got, err := store.Get(t.Context(), proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultBranch != "master" {
		t.Errorf("DefaultBranch = %q, want master", got.DefaultBranch)
	}
	if _, _, err := store.RootFiles(t.Context(), proj.ID); err != nil {
		t.Errorf("RootFiles on the repository's branch: %v", err)
	}
}

func TestCreateRefusesAMissingNamedBranch(t *testing.T) {
	store := newTestStore(t)
	repo := masterOnlyRepo(t)
	_, err := store.Create(t.Context(), "Named", repo, CreateOpts{WorkspaceID: testWS, DefaultBranch: "main"})
	var e *apperr.Error
	if !errors.As(err, &e) || e.Key != "project.errors.clone_branch_missing" {
		t.Fatalf("Create on a missing branch = %v, want clone_branch_missing", err)
	}

	done := make(chan *Project, 1)
	if _, err := store.CreateInBackground(t.Context(), "NamedBg", repo,
		CreateOpts{WorkspaceID: testWS, DefaultBranch: "main"}, func(p *Project) { done <- p }); err != nil {
		t.Fatalf("CreateInBackground: %v", err)
	}
	out := <-done
	want := apperr.LocalizeCtx(t.Context(), apperr.New("project.errors.clone_branch_missing"))
	if out.CloneStatus != CloneFailed || out.CloneError != want {
		t.Errorf("background clone = %q (%q), want failed with %q", out.CloneStatus, out.CloneError, want)
	}

	named, err := store.Create(t.Context(), "Master", repo, CreateOpts{WorkspaceID: testWS, DefaultBranch: "master"})
	if err != nil || named.DefaultBranch != "master" {
		t.Errorf("Create on the named branch = %v, %v", named, err)
	}
}

func TestMissingCheckoutIsNotAServerError(t *testing.T) {
	store := newTestStore(t)
	proj := &Project{ID: "gone000000000001", Name: "Gone", Path: filepath.Join(store.baseDir, "gone"),
		RepoURL: "https://example.com/o/r.git", WorkspaceID: testWS, Source: SourceGit,
		DefaultBranch: "main", StageTemplate: map[string]string{}}
	if err := store.insertProject(t.Context(), proj, "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListWorktrees(t.Context(), proj.ID); apperr.Code(err) != "project.errors.clone_missing" {
		t.Errorf("ListWorktrees = %v, want clone_missing", err)
	}
	if _, _, err := store.RootFiles(t.Context(), proj.ID); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("RootFiles = %v, want a precondition error", err)
	}
}

func TestExcludesBackfillLeavesAMissingCloneMissing(t *testing.T) {
	store := newTestStore(t)
	dir := filepath.Join(store.baseDir, "gone")
	proj := &Project{ID: "gone000000000002", Name: "Gone", Path: dir,
		RepoURL: "https://example.com/o/r.git", WorkspaceID: testWS, Source: SourceGit,
		DefaultBranch: "main", StageTemplate: map[string]string{}}
	if err := store.insertProject(t.Context(), proj, "{}"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureProjectExcludes(t.Context(), proj.ID); apperr.Code(err) != "project.errors.clone_missing" {
		t.Errorf("EnsureProjectExcludes = %v, want clone_missing", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a .git was made for a missing clone: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListWorktrees(t.Context(), proj.ID); apperr.Code(err) != "project.errors.clone_missing" {
		t.Errorf("ListWorktrees on a bare .git/info = %v, want clone_missing", err)
	}
}
