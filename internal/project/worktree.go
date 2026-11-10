package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	TaskID string `json:"task_id,omitempty"`
}

func taskBranchName(taskID string) string { return "task-" + taskID }

// Must match images/snippets/result-push.sh. Lives only in the daemon's /git/ repo, never on the
// project's remote.
func ResultBranchName(taskID string) string { return "octopus-result-" + taskID }

func validateTaskID(taskID string) error {
	if taskID == "" || len(taskID) > 128 || strings.HasPrefix(taskID, "-") {
		return apperr.Invalid("project.errors.invalid_task_id", "id", taskID)
	}
	for _, r := range taskID {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return apperr.Invalid("project.errors.invalid_task_id", "id", taskID)
		}
	}
	return nil
}

func (s *Store) taskWorktreeDir(proj *Project, taskID string) string {
	return filepath.Join(s.baseDir, worktreesDirName,
		fmt.Sprintf("%s--%s", filepath.Base(proj.Path), taskBranchName(taskID)))
}

// The executor is pinned to the gitdir the clone records, never to the agent-writable `.git` file.
func (s *Store) hostWorktreeGit(ctx context.Context, projectID, taskID string) (WorktreeGit, error) {
	if err := validateTaskID(taskID); err != nil {
		return WorktreeGit{}, err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return WorktreeGit{}, err
	}
	dir := s.taskWorktreeDir(proj, taskID)
	exec, err := worktreeExec(proj.Path, dir)
	if err != nil {
		return WorktreeGit{}, err
	}
	return WorktreeGit{
		Exec:          exec,
		Dir:           dir,
		Branch:        taskBranchName(taskID),
		DefaultBranch: proj.DefaultBranch,
	}, nil
}

func (s *Store) HostWorktreeGit(ctx context.Context, projectID, taskID string) (WorktreeGit, error) {
	return s.hostWorktreeGit(ctx, projectID, taskID)
}

type TaskWorktree struct {
	Path       string
	Branch     string
	StaleBase  error
	BaseCommit string
}

func (s *Store) CreateTaskWorktree(ctx context.Context, projectID, taskID string) (path, branch string, err error) {
	wt, err := s.CreateTaskWorktreeAuth(ctx, projectID, taskID, "")
	return wt.Path, wt.Branch, err
}

// The token lives only in the git process's arguments, never in .git/config or the remote URL.
func (s *Store) CreateTaskWorktreeAuth(ctx context.Context, projectID, taskID, ghToken string) (TaskWorktree, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return TaskWorktree{}, err
	}
	if err := proj.ready(); err != nil {
		return TaskWorktree{}, err
	}
	if err := ValidateBranchName(proj.DefaultBranch); err != nil {
		return TaskWorktree{}, err
	}
	return s.addWorktree(proj, taskID, "origin/"+proj.DefaultBranch, &ghToken)
}

// Branches from the source's local ref with no fetch: the task branch exists only locally until a
// PR.
func (s *Store) CreateForkedWorktree(ctx context.Context, projectID, newTaskID, sourceBranch string) (path, branch string, err error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(sourceBranch) == "" {
		return "", "", apperr.Invalid("project.errors.source_branch_required")
	}
	if verr := ValidateBranchName(sourceBranch); verr != nil {
		return "", "", verr
	}
	wt, err := s.addWorktree(proj, newTaskID, sourceBranch, nil)
	return wt.Path, wt.Branch, err
}

func (s *Store) addWorktree(proj *Project, taskID, startPoint string, fetchToken *string) (TaskWorktree, error) {
	if err := validateTaskID(taskID); err != nil {
		return TaskWorktree{}, err
	}
	if err := proj.ready(); err != nil {
		return TaskWorktree{}, err
	}
	wt := TaskWorktree{Branch: taskBranchName(taskID)}
	worktreeDir := s.taskWorktreeDir(proj, taskID)

	if _, statErr := os.Stat(worktreeDir); statErr == nil {
		abs, absErr := filepath.Abs(worktreeDir)
		if absErr != nil {
			return TaskWorktree{}, apperr.Wrap(absErr, "project.errors.resolve_worktree_path")
		}
		wt.Path = abs
		return wt, nil
	}

	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0o750); err != nil {
		return TaskWorktree{}, apperr.Wrap(err, "project.errors.worktree_parent_dir")
	}

	var fetchErr error
	if fetchToken != nil {
		fetchErr = fetchDefaultBranch(proj, *fetchToken)
	}
	// An existing branch without a worktree was parked by ParkTaskWorktree: check it out again,
	// don't branch over it.
	args := []string{"worktree", "add", "-b", wt.Branch, "--", worktreeDir, startPoint}
	parked := branchExists(proj.Path, wt.Branch)
	if parked {
		args = []string{"worktree", "add", "--", worktreeDir, wt.Branch}
	}
	if err := gitRun(proj.Path, args...); err != nil {
		return TaskWorktree{}, apperr.Wrap(err, "project.errors.worktree_add")
	}
	logx.Warn(installExcludeBlock(filepath.Join(proj.Path, ".git")), "project.log.install_exclude", "path", proj.Path)

	if fetchErr != nil && *fetchToken != "" && !parked {
		wt.StaleBase = fetchErr
		wt.BaseCommit = shortCommit(proj.Path, startPoint)
	}

	abs, absErr := filepath.Abs(worktreeDir)
	if absErr != nil {
		return TaskWorktree{}, apperr.Wrap(absErr, "project.errors.resolve_worktree_path")
	}
	wt.Path = abs
	return wt, nil
}

func (s *Store) FetchDefaultBranch(ctx context.Context, projectID, ghToken string) error {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return err
	}
	if err := ValidateBranchName(proj.DefaultBranch); err != nil {
		return err
	}
	return fetchDefaultBranch(proj, ghToken)
}

func fetchDefaultBranch(proj *Project, ghToken string) error {
	args := append(authRewriteArgs(ghToken), "fetch", "--no-recurse-submodules", "--", "origin", proj.DefaultBranch)
	err := gitRun(proj.Path, args...)
	if err == nil {
		return nil
	}
	err = apperr.Wrap(errors.New(redactToken(err.Error(), ghToken)), "project.errors.git_fetch")
	logx.Warn(err, "project.log.fetch_origin", "branch", proj.DefaultBranch, "path", proj.Path)
	return err
}

func shortCommit(dir, ref string) string {
	out, err := gitOutput(dir, "rev-parse", "--short", "--verify", ref+"^{commit}")
	if err != nil || strings.TrimSpace(out) == "" {
		return "?"
	}
	return strings.TrimSpace(out)
}

// Never run git inside the worktree (`git worktree remove` reads the agent-writable `.git` file):
// delete the dir and prune instead.
func (s *Store) RemoveTaskWorktree(ctx context.Context, projectID, taskID string) error {
	if err := validateTaskID(taskID); err != nil {
		return err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return err
	}
	worktreeDir := s.taskWorktreeDir(proj, taskID)
	branch := taskBranchName(taskID)

	logx.Warn(os.RemoveAll(worktreeDir), "project.log.remove_worktree_dir", "path", worktreeDir)
	logx.Warn(gitRun(proj.Path, "worktree", "prune"), "project.log.worktree_prune", "path", proj.Path)
	logx.Warn(gitRun(proj.Path, "branch", "-D", "--", branch), "project.log.delete_branch", "branch", branch)
	result := ResultBranchName(taskID)
	logx.Warn(gitRun(proj.Path, "update-ref", "-d", "refs/heads/"+result), "project.log.delete_result_branch", "branch", result)
	return nil
}

func (s *Store) ParkTaskWorktree(ctx context.Context, projectID, taskID string) error {
	if err := validateTaskID(taskID); err != nil {
		return err
	}
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return err
	}
	worktreeDir := s.taskWorktreeDir(proj, taskID)
	if _, statErr := os.Lstat(worktreeDir); errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if err := s.CommitWorktree(ctx, projectID, taskID,
		i18n.Text("project.commits.park_snapshot")); err != nil {
		return apperr.Wrap(err, "project.errors.park_snapshot")
	}
	if err := os.RemoveAll(worktreeDir); err != nil {
		return apperr.Wrap(err, "project.errors.remove_worktree_dir")
	}
	logx.Warn(gitRun(proj.Path, "worktree", "prune"), "project.log.worktree_prune", "path", proj.Path)
	return nil
}

func branchExists(dir, branch string) bool {
	return gitRun(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil
}

func (s *Store) ListWorktrees(ctx context.Context, projectID string) ([]Worktree, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err = proj.checkout(); err != nil {
		if proj.CloneStatus == CloneCloning || proj.CloneStatus == CloneFailed {
			return []Worktree{}, nil
		}
		return nil, err
	}
	out, err := gitOutput(proj.Path, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.git_worktree_list")
	}

	worktrees := []Worktree{}
	var cur Worktree
	flush := func() {
		if cur.Path != "" && strings.HasPrefix(cur.Branch, "task-") {
			worktrees = append(worktrees, cur)
		}
		cur = Worktree{}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			cur.Branch = filepath.Base(strings.TrimPrefix(line, "branch "))
			if strings.HasPrefix(cur.Branch, "task-") {
				cur.TaskID = strings.TrimPrefix(cur.Branch, "task-")
			}
		}
	}
	flush()
	return worktrees, nil
}

func (s *Store) CommitWorktree(ctx context.Context, projectID, taskID, message string) error {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return err
	}
	_, err = w.Commit(message)
	return err
}

func (s *Store) WorktreeHasChanges(ctx context.Context, projectID, taskID string) (bool, error) {
	w, err := s.hostWorktreeGit(ctx, projectID, taskID)
	if err != nil {
		return false, err
	}
	return w.HasChanges()
}

func TaskBranchName(taskID string) string { return taskBranchName(taskID) }

// Persisted in existing repos' info/exclude; changing it makes installExcludeBlock append a second
// block.
const excludeBlockMarker = "# octopus-managed-exclude"

func excludeBlockBody() string {
	return excludeBlockMarker + " " + i18n.Text("project.files.exclude_header") + "\n" + excludePatterns
}

const excludePatterns = `node_modules/
__pycache__/
*.pyc
*.pyo
.venv/
venv/
env/
.tox/
.pytest_cache/
.mypy_cache/
.ruff_cache/
.gradle/
target/
dist/
build/
out/
.next/
.nuxt/
.parcel-cache/
.turbo/
.cache/
.idea/
.vscode/
coverage/
*.log
`

func (s *Store) EnsureProjectExcludes(ctx context.Context, projectID string) error {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return err
	}
	// On a missing clone MkdirAll would create a bogus .git, and every later git command would
	// fail.
	if err := proj.checkout(); err != nil {
		return err
	}
	return installExcludeBlock(filepath.Join(proj.Path, ".git"))
}

// commonDir must be computed by the daemon, never asked of git from inside a worktree whose `.git`
// file the agent controls.
func installExcludeBlock(commonDir string) error {
	infoDir := filepath.Join(commonDir, "info")
	if err := os.MkdirAll(infoDir, 0o750); err != nil {
		return apperr.Wrap(err, "project.errors.create_info_dir", "dir", infoDir)
	}
	excludePath := filepath.Join(infoDir, "exclude")
	//nolint:gosec // G304: excludePath is built from `git rev-parse --git-common-dir` output, not user input
	existing, readErr := os.ReadFile(excludePath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return apperr.Wrap(readErr, "project.errors.read_exclude", "path", excludePath)
	}
	if strings.Contains(string(existing), excludeBlockMarker) {
		return nil
	}
	var prefix string
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	payload := []byte(prefix + "\n" + excludeBlockBody())
	//nolint:gosec // G306: info/exclude must be readable by git running as any uid; 0644 mirrors git's own defaults
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return apperr.Wrap(err, "project.errors.open_exclude", "path", excludePath)
	}
	defer logx.Close(f, excludePath)
	if _, err := f.Write(payload); err != nil {
		return apperr.Wrap(err, "project.errors.write_exclude", "path", excludePath)
	}
	return nil
}
