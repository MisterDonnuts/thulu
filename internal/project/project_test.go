package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/testpg"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	projDir := filepath.Join(dir, "projects")
	os.MkdirAll(projDir, 0755)

	db := testpg.New(t)
	store, err := NewStore(db, projDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestCreateRequiresRepoURL(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Create(t.Context(), "foo", "", CreateOpts{}); err == nil {
		t.Fatal("expected error when repo_url empty")
	}
}

func TestCreateClonesAndPersists(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)

	proj, err := store.Create(t.Context(), "My Project", repo, CreateOpts{
		WorkspaceID: testWS,
		Description: "the flagship",
		StageTemplate: map[string]string{
			"refinement": "probe",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if proj.RepoURL != repo {
		t.Errorf("RepoURL = %q want %q", proj.RepoURL, repo)
	}
	if proj.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q want main", proj.DefaultBranch)
	}
	if proj.Description != "the flagship" {
		t.Errorf("Description = %q", proj.Description)
	}
	if proj.StageTemplate["refinement"] != "probe" {
		t.Errorf("StageTemplate not persisted: %v", proj.StageTemplate)
	}
	if _, err := os.Stat(filepath.Join(proj.Path, "README.md")); err != nil {
		t.Errorf("README not cloned: %v", err)
	}
}

func TestSanitizesDirName(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "My Project With Spaces", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if strings.ContainsRune(filepath.Base(proj.Path), ' ') {
		t.Errorf("project dir contains spaces: %s", proj.Path)
	}
}

func TestUpdate(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "Up", repo, CreateOpts{WorkspaceID: testWS})

	desc := "now with a description"
	updated, err := store.Update(t.Context(), proj.ID, Patch{Description: &desc})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Description != desc {
		t.Errorf("Description not updated: %q", updated.Description)
	}
	reloaded, _ := store.Get(t.Context(), proj.ID)
	if reloaded.Description != desc {
		t.Errorf("not persisted: %q", reloaded.Description)
	}
}

func TestWorktreePerTaskLifecycle(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "Wt", repo, CreateOpts{WorkspaceID: testWS})

	path1, branch1, err := store.CreateTaskWorktree(t.Context(), proj.ID, "abc123")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if branch1 != "task-abc123" {
		t.Errorf("branch = %q want task-abc123", branch1)
	}
	if _, err := os.Stat(path1); err != nil {
		t.Errorf("worktree dir not created: %v", err)
	}

	path2, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "abc123")
	if err != nil {
		t.Fatalf("CreateTaskWorktree (2nd): %v", err)
	}
	if path1 != path2 {
		t.Errorf("not idempotent: %q vs %q", path1, path2)
	}

	wts, err := store.ListWorktrees(t.Context(), proj.ID)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if len(wts) != 1 || wts[0].TaskID != "abc123" {
		t.Errorf("worktrees = %+v", wts)
	}

	if err := store.RemoveTaskWorktree(t.Context(), proj.ID, "abc123"); err != nil {
		t.Fatalf("RemoveTaskWorktree: %v", err)
	}
	if _, err := os.Stat(path1); err == nil {
		t.Error("worktree dir not removed")
	}
}

func TestRemoveTaskWorktree_DeletesResultBranch(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "Result", repo, CreateOpts{WorkspaceID: testWS})
	for _, id := range []string{"r1", "r2"} {
		if _, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, id); err != nil {
			t.Fatalf("CreateTaskWorktree %s: %v", id, err)
		}
		gitRunT(t, proj.Path, "branch", ResultBranchName(id), "task-"+id)
	}

	if err := store.RemoveTaskWorktree(t.Context(), proj.ID, "r1"); err != nil {
		t.Fatalf("RemoveTaskWorktree: %v", err)
	}
	refs := gitOutT(t, proj.Path, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if strings.Contains(refs, ResultBranchName("r1")) || strings.Contains(refs, "task-r1") {
		t.Errorf("r1 branches survived: %q", refs)
	}
	if !strings.Contains(refs, ResultBranchName("r2")) {
		t.Errorf("r2's result branch was removed too: %q", refs)
	}
	if err := store.RemoveTaskWorktree(t.Context(), proj.ID, "r1"); err != nil {
		t.Fatalf("RemoveTaskWorktree (2nd): %v", err)
	}
}

func TestCommitWorktreeNoOpWhenClean(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "CommitClean", repo, CreateOpts{WorkspaceID: testWS})

	if _, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "t1"); err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if err := store.CommitWorktree(t.Context(), proj.ID, "t1", "noop"); err != nil {
		t.Fatalf("CommitWorktree on clean wt should not error: %v", err)
	}
}

func TestCommitWorktreeWithChanges(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "CommitDirty", repo, CreateOpts{WorkspaceID: testWS})

	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "t2")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := store.CommitWorktree(t.Context(), proj.ID, "t2", "stage: probe"); err != nil {
		t.Fatalf("CommitWorktree: %v", err)
	}

	cmd := exec.Command("git", "log", "--oneline")
	cmd.Dir = wtPath
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "stage: probe") {
		t.Errorf("commit not in log: %s", out)
	}
}

func TestDeleteCleansUp(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "Del", repo, CreateOpts{WorkspaceID: testWS})
	store.CreateTaskWorktree(t.Context(), proj.ID, "x")

	if err := store.Delete(t.Context(), proj.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(proj.Path); err == nil {
		t.Error("project dir not removed")
	}
	if _, err := store.Get(t.Context(), proj.ID); err == nil {
		t.Error("project still in db")
	}
}

func TestList(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)

	list, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 projects, got %d", len(list))
	}

	store.Create(t.Context(), "Proj A", repo, CreateOpts{WorkspaceID: testWS})
	store.Create(t.Context(), "Proj B", repo, CreateOpts{WorkspaceID: testWS})

	list, err = store.List(t.Context())
	if err != nil {
		t.Fatalf("List after creates: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 projects, got %d", len(list))
	}

	store.Create(t.Context(), "Proj WS", repo, CreateOpts{
		WorkspaceID:   "ws-list",
		StageTemplate: map[string]string{"refinement": "probe"},
	})
	wsList, err := store.ListByWorkspace(t.Context(), "ws-list")
	if err != nil {
		t.Fatalf("ListByWorkspace: %v", err)
	}
	if len(wsList) != 1 {
		t.Fatalf("expected 1 project in ws-list, got %d", len(wsList))
	}
	if wsList[0].StageTemplate == nil {
		t.Fatal("ListByWorkspace returned nil StageTemplate")
	}
	if wsList[0].StageTemplate["refinement"] != "probe" {
		t.Errorf("ListByWorkspace StageTemplate = %v, want refinement=probe", wsList[0].StageTemplate)
	}
}

func TestEnsureStageDirs(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "Stage Test", repo, CreateOpts{WorkspaceID: testWS})

	stagePath, err := store.EnsureStageDirs(proj)
	if err != nil {
		t.Fatalf("EnsureStageDirs: %v", err)
	}
	if stagePath == "" {
		t.Error("expected non-empty stage path")
	}
	if _, err := os.Stat(stagePath); err != nil {
		t.Errorf("stage dir not created: %v", err)
	}
}

func TestTaskBranchName(t *testing.T) {
	got := TaskBranchName("abc123")
	if got != "task-abc123" {
		t.Errorf("TaskBranchName = %q, want task-abc123", got)
	}
}

func TestParseCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0\n", 0},
		{"  3  ", 3},
		{"42", 42},
		{"", 0},
		{"abc", 0},
	}
	for _, c := range cases {
		got := parseCount(c.in)
		if got != c.want {
			t.Errorf("parseCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDB_Accessor(t *testing.T) {
	store := newTestStore(t)
	if store.DB() == nil {
		t.Error("DB() should not be nil")
	}
}

func TestWorktreeHasChanges_Clean(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "WtClean", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "t1"); err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}

	changed, err := store.WorktreeHasChanges(t.Context(), proj.ID, "t1")
	if err != nil {
		t.Fatalf("WorktreeHasChanges: %v", err)
	}
	if changed {
		t.Error("expected no changes in clean worktree")
	}
}

func TestWorktreeHasChanges_Dirty(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "WtDirty", repo, CreateOpts{WorkspaceID: testWS})
	wtPath, _, _ := store.CreateTaskWorktree(t.Context(), proj.ID, "t2")

	os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("dirty"), 0644)

	changed, err := store.WorktreeHasChanges(t.Context(), proj.ID, "t2")
	if err != nil {
		t.Fatalf("WorktreeHasChanges: %v", err)
	}
	if !changed {
		t.Error("expected changes in dirty worktree")
	}
}

func TestBranchHasCommitsAhead_None(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "BCA", repo, CreateOpts{WorkspaceID: testWS})
	store.CreateTaskWorktree(t.Context(), proj.ID, "t3")

	ahead, err := store.BranchHasCommitsAhead(t.Context(), proj.ID, "t3")
	if err != nil {
		t.Fatalf("BranchHasCommitsAhead: %v", err)
	}
	if ahead {
		t.Error("expected 0 commits ahead on fresh worktree")
	}
}

func TestRebaseConflictError_Error(t *testing.T) {
	err := &RebaseConflictError{Base: "origin/main", Files: []string{"foo.go", "bar.go"}}
	msg := err.Error()
	if !strings.Contains(msg, "origin/main") {
		t.Errorf("error message missing base branch: %s", msg)
	}
	if !strings.Contains(msg, "2") {
		t.Errorf("error message missing file count: %s", msg)
	}
	if !strings.Contains(msg, "foo.go") {
		t.Errorf("error message missing file names: %s", msg)
	}
}

func TestGitOutput_InvalidDir(t *testing.T) {
	_, err := gitOutput("/nonexistent/path", "rev-parse", "HEAD")
	if err == nil {
		t.Error("expected error for invalid directory")
	}
}

func TestGitOutput_ValidCommand(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}

	out, err := gitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("gitOutput: %v", err)
	}
	if strings.TrimSpace(out) != "main" {
		t.Errorf("branch = %q, want main", strings.TrimSpace(out))
	}
}

func TestDetectDefaultBranch_ValidRepo(t *testing.T) {
	repo := testgit.BareRepo(t)
	dir := t.TempDir()
	clone := filepath.Join(dir, "clone")
	cmd := exec.Command("git", "clone", repo, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %s: %v", out, err)
	}
	branch := detectDefaultBranch(clone)
	if branch == "" {
		t.Error("expected non-empty default branch")
	}
}

func TestSanitizeDirName_EdgeCases(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"normal", "normal"},
		{"with spaces", "with-spaces"},
		{"multiple   spaces", "multiple---spaces"},
		{"trailing-", "trailing"},
		{"---leading", "leading"},
		{"", "project"},
	}
	for _, c := range cases {
		got := sanitizeDirName(c.input)
		if got != c.want {
			t.Errorf("sanitizeDirName(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestUpdate_NotFound(t *testing.T) {
	store := newTestStore(t)
	name := "updated name"
	_, err := store.Update(t.Context(), "nonexistent-id", Patch{Name: &name})
	if err == nil {
		t.Error("expected error when updating non-existent project")
	}
}

func TestDetectDefaultBranch_NoOrigin(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "develop"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Run()
	}

	branch := detectDefaultBranch(dir)
	if branch != "develop" {
		t.Errorf("fallback branch = %q, want develop", branch)
	}
}

func TestDetectDefaultBranch_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	branch := detectDefaultBranch(dir)
	if branch != "main" {
		t.Errorf("empty dir branch = %q, want main", branch)
	}
}

func TestCreateTaskWorktree_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.CreateTaskWorktree(t.Context(), "nonexistent-project", "task-1")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestCommitWorktree_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.CommitWorktree(t.Context(), "nonexistent-project", "task-1", "msg")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestCreateForkedWorktree(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "Fork", repo, CreateOpts{WorkspaceID: testWS})

	srcPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "src-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree source: %v", err)
	}
	os.WriteFile(filepath.Join(srcPath, "src.txt"), []byte("source"), 0644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "src commit"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = srcPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	forkPath, forkBranch, err := store.CreateForkedWorktree(t.Context(), proj.ID, "fork-task", "task-src-task")
	if err != nil {
		t.Fatalf("CreateForkedWorktree: %v", err)
	}
	if forkBranch != "task-fork-task" {
		t.Errorf("fork branch = %q, want task-fork-task", forkBranch)
	}
	if _, err := os.Stat(forkPath); err != nil {
		t.Errorf("fork worktree dir not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(forkPath, "src.txt")); err != nil {
		t.Errorf("src.txt not present in fork: %v", err)
	}

	forkPath2, _, err := store.CreateForkedWorktree(t.Context(), proj.ID, "fork-task", "task-src-task")
	if err != nil {
		t.Fatalf("CreateForkedWorktree (idempotent): %v", err)
	}
	if forkPath != forkPath2 {
		t.Errorf("not idempotent: %q vs %q", forkPath, forkPath2)
	}
}

func TestCreateForkedWorktree_EmptySourceBranch(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "ForkEmpty", repo, CreateOpts{WorkspaceID: testWS})
	_, _, err := store.CreateForkedWorktree(t.Context(), proj.ID, "fork-task", "")
	if err == nil {
		t.Error("expected error for empty sourceBranch")
	}
}

func TestCreateForkedWorktree_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.CreateForkedWorktree(t.Context(), "nonexistent-project", "fork-task", "task-something")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestGetBranchStatus_Fresh(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "BranchStatus", repo, CreateOpts{WorkspaceID: testWS})
	store.CreateTaskWorktree(t.Context(), proj.ID, "bs-task")

	status, err := store.GetBranchStatus(t.Context(), proj.ID, "bs-task")
	if err != nil {
		t.Fatalf("GetBranchStatus: %v", err)
	}
	if status.Branch != "task-bs-task" {
		t.Errorf("Branch = %q, want task-bs-task", status.Branch)
	}
	if status.Ahead != 0 {
		t.Errorf("Ahead = %d, want 0", status.Ahead)
	}
	if !status.UpToDate {
		t.Errorf("expected UpToDate=true on fresh worktree")
	}
}

func TestGetBranchStatus_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetBranchStatus(t.Context(), "nonexistent-project", "task-1")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestPushBranch_WithCommit(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "PushTest", repo, CreateOpts{WorkspaceID: testWS})
	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "push-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}

	os.WriteFile(filepath.Join(wtPath, "push.txt"), []byte("push"), 0644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "push test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = wtPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	result, err := store.PushBranch(t.Context(), proj.ID, "push-task")
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	_ = result
}

func TestPushBranch_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.PushBranch(t.Context(), "nonexistent-project", "task-1")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestCreate_DirectoryAlreadyExists_Disambiguates(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)

	// Names are unique per workspace, not globally: another workspace may already own this dir
	// name.
	projDir := filepath.Join(store.baseDir, "My-Project")
	os.MkdirAll(projDir, 0755)

	proj, err := store.Create(t.Context(), "My Project", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create with occupied directory: %v", err)
	}
	if proj.Path == projDir {
		t.Errorf("project path %q should not reuse the occupied directory", proj.Path)
	}
	if !strings.HasPrefix(filepath.Base(proj.Path), "My-Project-") {
		t.Errorf("disambiguated dir = %q, want My-Project-<id>", filepath.Base(proj.Path))
	}
}

func TestCreate_InvalidRepoURL(t *testing.T) {
	store := newTestStore(t)
	_, err := store.Create(t.Context(), "BadClone", "/nonexistent/path/to/repo.git", CreateOpts{WorkspaceID: testWS})
	if err == nil {
		t.Error("expected error when git clone fails")
	}
}

func TestCreate_RequiresName(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	_, err := store.Create(t.Context(), "", repo, CreateOpts{WorkspaceID: testWS})
	if err == nil {
		t.Error("expected error when name is empty")
	}
}

func TestUpdate_AllFields(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "UpdateAll", repo, CreateOpts{WorkspaceID: testWS})

	name := "NewName"
	branch := "develop"
	desc := "A description"
	tpl := map[string]string{"refinement": "probe"}
	updated, err := store.Update(t.Context(), proj.ID, Patch{
		Name:          &name,
		DefaultBranch: &branch,
		Description:   &desc,
		StageTemplate: &tpl,
	})
	if err != nil {
		t.Fatalf("Update all fields: %v", err)
	}
	if updated.Name != name {
		t.Errorf("Name = %q, want %q", updated.Name, name)
	}
	if updated.DefaultBranch != branch {
		t.Errorf("DefaultBranch = %q, want %q", updated.DefaultBranch, branch)
	}
	if updated.Description != desc {
		t.Errorf("Description = %q, want %q", updated.Description, desc)
	}
	if updated.StageTemplate["refinement"] != "probe" {
		t.Errorf("StageTemplate not updated: %v", updated.StageTemplate)
	}
}

func TestRemoveTaskWorktree_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.RemoveTaskWorktree(t.Context(), "nonexistent-project", "task-1")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestWorkTreeHasChanges_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.WorktreeHasChanges(t.Context(), "nonexistent-project", "task-1")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestGitRun_Error(t *testing.T) {
	err := gitRun("/nonexistent/dir/path", "status")
	if err == nil {
		t.Error("expected error for non-existent directory")
	}
}

func TestDelete_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.Delete(t.Context(), "nonexistent-id")
	if err == nil {
		t.Error("expected error when deleting nonexistent project")
	}
}

func TestListWorktrees_InvalidProjectPath(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "LWInvalid", repo, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	os.RemoveAll(proj.Path)
	_, err = store.ListWorktrees(t.Context(), proj.ID)
	if err == nil {
		t.Error("expected error when project directory is gone")
	}
	if apperr.Code(err) != "project.errors.clone_missing" || apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("err = %v (%s), want a precondition naming the missing clone", err, apperr.Code(err))
	}
	if _, _, err := store.RootFiles(t.Context(), proj.ID); apperr.Code(err) != "project.errors.clone_missing" {
		t.Errorf("RootFiles err = %v, want clone_missing", err)
	}
}

func TestWorktreeHasChanges_InvalidDir(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "WtInv", repo, CreateOpts{WorkspaceID: testWS})
	_, err := store.WorktreeHasChanges(t.Context(), proj.ID, "nonexistent-task")
	if err == nil {
		t.Error("expected error for nonexistent worktree directory")
	}
}

func TestCommitWorktree_InvalidDir(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "CWInv", repo, CreateOpts{WorkspaceID: testWS})
	err := store.CommitWorktree(t.Context(), proj.ID, "nonexistent-task", "msg")
	if err == nil {
		t.Error("expected error for nonexistent worktree directory")
	}
}

func TestBranchHasCommitsAhead_InvalidDir(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "BCAInv", repo, CreateOpts{WorkspaceID: testWS})
	_, err := store.BranchHasCommitsAhead(t.Context(), proj.ID, "nonexistent-task")
	if err == nil {
		t.Error("expected error for nonexistent worktree directory")
	}
}

func advanceOriginMain(t *testing.T, bareRepo string, msg string) {
	t.Helper()
	tmpDir := t.TempDir()
	tmpClone := filepath.Join(tmpDir, "tmp-clone")
	cloneCmd := exec.Command("git", "clone", bareRepo, tmpClone)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("advanceOriginMain clone: %s: %v", out, err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpClone
		cmd.Run()
	}
	if err := os.WriteFile(filepath.Join(tmpClone, "advance.txt"), []byte(msg), 0644); err != nil {
		t.Fatalf("advanceOriginMain write: %v", err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", msg},
		{"push", "origin", "main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpClone
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("advanceOriginMain git %v: %s: %v", args, out, err)
		}
	}
}

func TestRebaseOntoDefault_Rebased(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "RebaseTest", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "rb-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}

	advanceOriginMain(t, bare, "advance-main-1")

	w := WorktreeGit{Exec: mustWorktreeExec(t, proj.Path, wtPath), Dir: wtPath, Branch: taskBranchName("rb-task"), DefaultBranch: proj.DefaultBranch}
	rebased, err := w.rebaseOntoDefault("")
	if err != nil {
		t.Fatalf("rebaseOntoDefault: %v", err)
	}
	if !rebased {
		t.Error("expected rebased=true when branch is behind origin/main")
	}
}

func TestPushBranch_WithRebase(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "PushRebaseTest", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "pr-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wtPath, "task.txt"), []byte("task work"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "task commit"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = wtPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	advanceOriginMain(t, bare, "advance-main-for-push")

	result, err := store.PushBranch(t.Context(), proj.ID, "pr-task")
	if err != nil {
		t.Fatalf("PushBranch with rebase: %v", err)
	}
	if !result.Rebased {
		t.Error("expected Rebased=true when branch was behind origin/main")
	}
}

func TestRebaseOntoDefault_FetchFails(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "FetchFail", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "ff-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	os.RemoveAll(bare)
	w := WorktreeGit{Exec: mustWorktreeExec(t, proj.Path, wtPath), Dir: wtPath, Branch: taskBranchName("ff-task"), DefaultBranch: proj.DefaultBranch}
	_, err = w.rebaseOntoDefault("")
	if err == nil {
		t.Error("expected error when remote is unreachable")
	}
}

func TestRebaseOntoDefault_Conflict(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "ConflictTest", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "conflict-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("task version\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "task: modify readme"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = wtPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	tmpDir := t.TempDir()
	tmpClone := filepath.Join(tmpDir, "adv-clone")
	cloneCmd := exec.Command("git", "clone", bare, tmpClone)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %s: %v", out, err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpClone
		cmd.Run()
	}
	if err := os.WriteFile(filepath.Join(tmpClone, "README.md"), []byte("origin main version\n"), 0644); err != nil {
		t.Fatalf("write adv: %v", err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "origin: modify readme"},
		{"push", "origin", "main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpClone
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	w := WorktreeGit{Exec: mustWorktreeExec(t, proj.Path, wtPath), Dir: wtPath, Branch: taskBranchName("conflict-task"), DefaultBranch: proj.DefaultBranch}
	rebased, err := w.rebaseOntoDefault("")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	var conflictErr *RebaseConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *RebaseConflictError, got %T: %v", err, err)
	}
	if rebased {
		t.Error("rebased should be false on conflict")
	}
	_ = conflictErr
}

func TestPushBranch_RemoteUnreachable(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	proj, err := store.Create(t.Context(), "PushFail", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "pf-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "pf.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "pf"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = wtPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	os.RemoveAll(bare)
	_, err = store.PushBranch(t.Context(), proj.ID, "pf-task")
	if err == nil {
		t.Error("expected error when remote is unreachable")
	}
}

func TestCreate_DBClosed(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)
	store.DB().Close()
	_, err := store.Create(t.Context(), "DBClosed", bare, CreateOpts{WorkspaceID: testWS})
	if err == nil {
		t.Error("expected error when DB is closed during insert")
	}
}

func TestEnsureStageDirs_Fails(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "StageFail", repo, CreateOpts{WorkspaceID: testWS})

	stageDir := store.StageDir(proj)
	if err := os.MkdirAll(filepath.Dir(stageDir), 0755); err != nil {
		t.Fatalf("MkdirAll parent: %v", err)
	}
	if err := os.WriteFile(stageDir, []byte("blocking"), 0644); err != nil {
		t.Fatalf("WriteFile block: %v", err)
	}
	_, err := store.EnsureStageDirs(proj)
	if err == nil {
		t.Error("expected error when sidecar dir is blocked by a file")
	}
}

func TestGetBranchStatus_InvalidDir(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "BSInv", repo, CreateOpts{WorkspaceID: testWS})
	_, err := store.GetBranchStatus(t.Context(), proj.ID, "nonexistent-task")
	if err == nil {
		t.Error("expected error for nonexistent worktree directory")
	}
}

func TestCreateTaskWorktree_BranchAlreadyExists(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "WtBranchExists", repo, CreateOpts{WorkspaceID: testWS})

	wtPath, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "be-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	os.RemoveAll(wtPath)
	gitRun(proj.Path, "worktree", "prune")

	again, branch, err := store.CreateTaskWorktree(t.Context(), proj.ID, "be-task")
	if err != nil {
		t.Fatalf("CreateTaskWorktree with an existing branch: %v", err)
	}
	if again != wtPath || branch != "task-be-task" {
		t.Errorf("got (%q, %q), want (%q, task-be-task)", again, branch, wtPath)
	}
}

func TestBranchHasCommitsAhead_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.BranchHasCommitsAhead(t.Context(), "nonexistent-project", "task-1")
	if err == nil {
		t.Error("expected error for nonexistent project")
	}
}

func TestCreateForkedWorktree_SourceBranchMissing(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "ForkMissing", repo, CreateOpts{WorkspaceID: testWS})
	_, _, err := store.CreateForkedWorktree(t.Context(), proj.ID, "fork-task", "task-nonexistent-source")
	if err == nil {
		t.Error("expected error when source branch doesn't exist")
	}
}

func TestBranchHasCommitsAhead_WithCommit(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "BCA2", repo, CreateOpts{WorkspaceID: testWS})
	store.CreateTaskWorktree(t.Context(), proj.ID, "t-ahead")

	worktreeDir := store.taskWorktreeDir(proj, "t-ahead")
	os.WriteFile(filepath.Join(worktreeDir, "new.txt"), []byte("content"), 0644)
	for _, args := range [][]string{
		{"add", "."},
		{"commit", "-m", "new commit"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktreeDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	ahead, err := store.BranchHasCommitsAhead(t.Context(), proj.ID, "t-ahead")
	if err != nil {
		t.Fatalf("BranchHasCommitsAhead: %v", err)
	}
	if !ahead {
		t.Error("expected ahead=true after committing to worktree")
	}
}

func TestList_DBError(t *testing.T) {
	store := newTestStore(t)
	store.DB().Close()
	_, err := store.List(t.Context())
	if err == nil {
		t.Error("expected error from List after DB close")
	}
}

func TestList_NullStageTemplate(t *testing.T) {
	store := newTestStore(t)
	_, err := store.DB().Exec(`INSERT INTO projects
		(id, name, path, repo_url, default_branch, description,
		 ai_review_cmd, testing_cmd, stage_template, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW())`,
		"null-tpl", "null-tpl", "/tmp/null-tpl", "file:///tmp/null-tpl",
		"main", "", "", "", "null",
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	projs, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, p := range projs {
		if p.ID == "null-tpl" {
			found = true
			if p.StageTemplate == nil {
				t.Error("StageTemplate should be non-nil even when stored as 'null'")
			}
		}
	}
	if !found {
		t.Error("expected to find null-tpl project in list")
	}
}

func TestCreateTaskWorktreeInstallsExcludes(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "Excl", repo, CreateOpts{WorkspaceID: testWS})

	wt, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "task1")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}

	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = wt
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse --git-common-dir: %v", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(wt, commonDir)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")

	body, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, excludeBlockMarker) {
		t.Errorf("exclude missing marker; body=%q", got)
	}
	for _, pat := range []string{"node_modules/", "__pycache__/", "target/", ".venv/"} {
		if !strings.Contains(got, pat) {
			t.Errorf("exclude missing pattern %q; body=%q", pat, got)
		}
	}

	if err := os.MkdirAll(filepath.Join(wt, "node_modules", "left-pad"), 0755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "node_modules", "left-pad", "index.js"), []byte("x"), 0644); err != nil {
		t.Fatalf("write left-pad: %v", err)
	}
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = wt
	statusOut, _ := cmd.Output()
	if strings.Contains(string(statusOut), "node_modules") {
		t.Errorf("git status surfaced node_modules despite exclude: %q", statusOut)
	}
}

func TestEnsureWorktreeExcludesIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "ExclIdem", repo, CreateOpts{WorkspaceID: testWS})
	wt, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "tA")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}

	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = wt
	out, _ := cmd.Output()
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(wt, commonDir)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")
	before, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}

	if err := store.EnsureProjectExcludes(t.Context(), proj.ID); err != nil {
		t.Fatalf("EnsureProjectExcludes (2nd): %v", err)
	}
	after, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude (2nd): %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("exclude changed on idempotent call; before=%q after=%q", before, after)
	}
}

func TestEnsureProjectExcludesBareRepo(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)
	proj, _ := store.Create(t.Context(), "BareBackfill", repo, CreateOpts{WorkspaceID: testWS})

	if err := store.EnsureProjectExcludes(t.Context(), proj.ID); err != nil {
		t.Fatalf("EnsureProjectExcludes: %v", err)
	}
	excludePath := filepath.Join(proj.Path, ".git", "info", "exclude")
	if _, err := os.Stat(excludePath); err != nil {
		excludePath = filepath.Join(proj.Path, "info", "exclude")
	}
	body, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude at expected path: %v", err)
	}
	if !strings.Contains(string(body), excludeBlockMarker) {
		t.Errorf("exclude missing marker after backfill")
	}

	if err := store.EnsureProjectExcludes(t.Context(), proj.ID); err != nil {
		t.Fatalf("EnsureProjectExcludes (2nd): %v", err)
	}
	body2, _ := os.ReadFile(excludePath)
	if string(body) != string(body2) {
		t.Errorf("EnsureProjectExcludes not idempotent")
	}
}

func TestCreate_PersistsWorkspaceID(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)

	proj, err := store.Create(t.Context(), "Scoped", repo, CreateOpts{WorkspaceID: "ws-alpha"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if proj.WorkspaceID != "ws-alpha" {
		t.Errorf("Create returned WorkspaceID = %q, want ws-alpha", proj.WorkspaceID)
	}

	got, err := store.Get(t.Context(), proj.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WorkspaceID != "ws-alpha" {
		t.Errorf("Get WorkspaceID = %q, want ws-alpha", got.WorkspaceID)
	}

	list, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].WorkspaceID != "ws-alpha" {
		t.Errorf("List WorkspaceID = %+v, want one project in ws-alpha", list)
	}
}

func TestCreate_NameUniquePerWorkspace(t *testing.T) {
	store := newTestStore(t)
	repo := testgit.BareRepo(t)

	if _, err := store.Create(t.Context(), "Shared Name", repo, CreateOpts{WorkspaceID: "ws-a"}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	other, err := store.Create(t.Context(), "Shared Name", repo, CreateOpts{WorkspaceID: "ws-b"})
	if err != nil {
		t.Fatalf("same name in another workspace should succeed: %v", err)
	}
	if other.WorkspaceID != "ws-b" {
		t.Errorf("WorkspaceID = %q, want ws-b", other.WorkspaceID)
	}

	if _, err := store.Create(t.Context(), "Shared Name", repo, CreateOpts{WorkspaceID: "ws-a"}); err == nil {
		t.Error("duplicate name within one workspace should fail")
	}
}

const testWS = "ws-test"

func TestCreate_RefusesAnUnownedProject(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Create(t.Context(), "unowned", testgit.BareRepo(t), CreateOpts{}); err == nil {
		t.Fatal("Create with no WorkspaceID: want error, got nil")
	}
}
