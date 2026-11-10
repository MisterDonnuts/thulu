package project

import (
	"errors"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type GitExec interface {
	Run(dir string, args ...string) error
	Output(dir string, args ...string) (string, error)
}

type WorktreeGit struct {
	Exec          GitExec
	Dir           string
	Branch        string
	DefaultBranch string
	Remote        string
}

func (w WorktreeGit) remote() string {
	if w.Remote == "" {
		return "origin"
	}
	return w.Remote
}

func (w WorktreeGit) urlRemote() bool { return strings.Contains(w.Remote, "://") }

// Must run at the top of every public method: rows written before input validation existed still
// reach here.
func (w WorktreeGit) validate() error {
	if err := ValidateBranchName(w.Branch); err != nil {
		return err
	}
	if err := ValidateBranchName(w.DefaultBranch); err != nil {
		return err
	}
	if w.Remote != "" {
		if err := ValidateRepoURL(w.Remote); err != nil {
			return err
		}
	}
	return nil
}

func (w WorktreeGit) baseRef() string {
	if w.urlRemote() {
		return "FETCH_HEAD"
	}
	return "origin/" + w.DefaultBranch
}

func (w WorktreeGit) HasChanges() (bool, error) {
	out, err := w.Exec.Output(w.Dir, "status", "--porcelain", "--ignore-submodules=all")
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.git_status")
	}
	return strings.TrimSpace(out) != "", nil
}

func (w WorktreeGit) Commit(message string) (committed bool, err error) {
	if err := w.refuseNestedRepos(); err != nil {
		return false, err
	}
	if err := w.Exec.Run(w.Dir, "add", "-A"); err != nil {
		return false, apperr.Wrap(err, "project.errors.git_add")
	}
	if w.Exec.Run(w.Dir, "diff", "--cached", "--quiet", "--no-ext-diff", "--no-textconv") == nil {
		return false, nil
	}
	if err := w.Exec.Run(w.Dir, "commit", "--no-verify", "-m", message); err != nil {
		return false, apperr.Wrap(err, "project.errors.git_commit")
	}
	return true, nil
}

var ErrNestedRepo error = apperr.Conflict("project.errors.nested_repo")

// `git add -A` would record a nested repo as a gitlink, after which git reads its agent-written
// .git config as a submodule.
// ls-files --others lists such a repo as one "dir/" entry without descending into it.
func (w WorktreeGit) refuseNestedRepos() error {
	out, err := w.Exec.Output(w.Dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return apperr.Wrap(err, "project.errors.git_ls_files")
	}
	var nested []string
	for _, p := range strings.Split(out, "\x00") {
		if strings.HasSuffix(p, "/") {
			nested = append(nested, p)
		}
	}
	if len(nested) > 0 {
		return apperr.Wrap(ErrNestedRepo, "project.errors.nested_repo_paths", "paths", strings.Join(nested, ", "))
	}
	return nil
}

func (w WorktreeGit) Push(ghToken string) (PushResult, error) {
	if err := w.validate(); err != nil {
		return PushResult{}, err
	}
	rebased, err := w.rebaseOntoDefault(ghToken)
	if err != nil {
		return PushResult{}, err
	}
	// -u only works with a named remote; a URL remote rejects it.
	pushArgs := []string{"push", "--no-verify"}
	if !w.urlRemote() {
		pushArgs = append(pushArgs, "-u")
	}
	if rebased {
		pushArgs = append(pushArgs, "--force-with-lease")
	}
	pushArgs = append(pushArgs, "--", w.remote(), w.Branch)
	if err := w.runAuth(ghToken, pushArgs...); err != nil {
		return PushResult{Rebased: rebased}, apperr.Wrap(err, "project.errors.git_push")
	}
	return PushResult{Rebased: rebased}, nil
}

// No rebase here: the agent rebases in its own clone, the only place it can resolve conflicts.
// Force-with-lease only when the branch no longer contains what was last pushed.
func (w WorktreeGit) Publish(ghToken string) (forced bool, err error) {
	if err := w.validate(); err != nil {
		return false, err
	}
	pushArgs := []string{"push", "--no-verify"}
	if !w.urlRemote() {
		pushArgs = append(pushArgs, "-u")
	}
	tracking := "refs/remotes/" + w.remote() + "/" + w.Branch
	if !w.urlRemote() {
		known := w.hasCommit(tracking)
		if !known {
			// The remote may already have this branch (pushed from elsewhere or
			// from the pod); a fetch failure means it does not.
			known = w.runAuth(ghToken, "fetch", "--no-recurse-submodules", "--", w.remote(),
				"+refs/heads/"+w.Branch+":"+tracking) == nil && w.hasCommit(tracking)
		}
		if known && w.Exec.Run(w.Dir, "merge-base", "--is-ancestor", tracking, "HEAD") != nil {
			forced = true
			pushArgs = append(pushArgs, "--force-with-lease")
		}
	}
	pushArgs = append(pushArgs, "--", w.remote(), w.Branch)
	if err := w.runAuth(ghToken, pushArgs...); err != nil {
		return forced, apperr.Wrap(err, "project.errors.git_push")
	}
	return forced, nil
}

func (w WorktreeGit) hasCommit(ref string) bool {
	return w.Exec.Run(w.Dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}") == nil
}

func (w WorktreeGit) Status(ghToken string) (BranchStatus, error) {
	if err := w.validate(); err != nil {
		return BranchStatus{}, err
	}
	if err := w.fetchDefault(ghToken); err != nil {
		return BranchStatus{}, err
	}
	base := w.baseRef()
	ahead, err := w.revCount(base + "..HEAD")
	if err != nil {
		return BranchStatus{}, apperr.Wrap(err, "project.errors.git_rev_list_ahead")
	}
	behind, err := w.revCount("HEAD.." + base)
	if err != nil {
		return BranchStatus{}, apperr.Wrap(err, "project.errors.git_rev_list_behind")
	}
	return BranchStatus{
		Branch:      w.Branch,
		Base:        base,
		Ahead:       ahead,
		Behind:      behind,
		UpToDate:    ahead == 0 && behind == 0,
		NeedsRebase: behind > 0,
	}, nil
}

func (w WorktreeGit) HasCommitsAhead() (bool, error) {
	if err := w.validate(); err != nil {
		return false, err
	}
	// A URL remote has no local default ref to compare against; let Push decide.
	if w.urlRemote() {
		return true, nil
	}
	n, err := w.revCount(w.DefaultBranch + ".." + w.Branch)
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.git_rev_list")
	}
	return n != 0, nil
}

const workflowsDir = ".github/workflows/"

// GitHub requires the token's workflows permission for pushed commits touching .github/workflows/.
// With no remote-tracking refs there is no baseline, so it answers false instead of flagging all
// history.
func (w WorktreeGit) TouchesWorkflows() (bool, error) {
	if err := w.validate(); err != nil {
		return false, err
	}
	remotes, err := w.Exec.Output(w.Dir, "for-each-ref", "--count=1", "--format=%(refname)", "refs/remotes/")
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.git_for_each_ref")
	}
	if strings.TrimSpace(remotes) == "" {
		return false, nil
	}
	// The first --not negates --remotes, the second restores the branch.
	out, err := w.Exec.Output(w.Dir, "log", "--format=", "--name-only", "--not", "--remotes", "--not",
		"--end-of-options", w.Branch, "--", workflowsDir)
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.git_log")
	}
	return strings.TrimSpace(out) != "", nil
}

func (w WorktreeGit) revCount(rng string) (int, error) {
	out, err := w.Exec.Output(w.Dir, "rev-list", "--count", "--end-of-options", rng, "--")
	if err != nil {
		return 0, err
	}
	return parseCount(out), nil
}

func (w WorktreeGit) fetchDefault(ghToken string) error {
	if err := w.runAuth(ghToken, "fetch", "--no-recurse-submodules", "--", w.remote(), w.DefaultBranch); err != nil {
		return apperr.Wrap(err, "project.errors.git_fetch")
	}
	return nil
}

func (w WorktreeGit) rebaseOntoDefault(ghToken string) (bool, error) {
	if err := w.fetchDefault(ghToken); err != nil {
		return false, err
	}
	base := w.baseRef()
	behind, err := w.revCount("HEAD.." + base)
	if err != nil {
		return false, apperr.Wrap(err, "project.errors.git_rev_list_behind")
	}
	if behind == 0 {
		return false, nil
	}
	if err := w.Exec.Run(w.Dir, "rebase", "--no-verify", "--end-of-options", base); err == nil {
		return true, nil
	}
	filesOut, filesErr := w.Exec.Output(w.Dir, "diff", "--name-only", "--diff-filter=U", "--no-ext-diff")
	logx.Warn(filesErr, "project.log.list_conflict_files", "dir", w.Dir)
	files := []string{}
	for _, line := range strings.Split(strings.TrimSpace(filesOut), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	logx.Warn(w.Exec.Run(w.Dir, "rebase", "--abort"), "project.log.rebase_abort", "dir", w.Dir)
	return false, &RebaseConflictError{Base: base, Files: files}
}

func (w WorktreeGit) runAuth(ghToken string, args ...string) error {
	full := append(authRewriteArgs(ghToken), args...)
	if err := w.Exec.Run(w.Dir, full...); err != nil {
		return errors.New(redactToken(err.Error(), ghToken))
	}
	return nil
}
