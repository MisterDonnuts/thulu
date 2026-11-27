package project

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

var historySHARe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// History answers which commits a deployed commit contains, on the daemon's clone of a project.
type History struct {
	g   localGitExec
	dir string
}

type HistoryCommit struct {
	SHA     string
	Message string
}

// OpenHistory reads the clone at path as it is, without fetching.
func OpenHistory(path string) History { return History{g: repoGit(path), dir: path} }

// History fetches the default branch first, so a commit merged on GitHub minutes ago is there. A
// failed fetch is logged and the clone is read as it is: a commit it lacks answers as unknown.
func (s *Store) History(ctx context.Context, projectID, ghToken string) (History, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return History{}, err
	}
	if err := proj.checkout(); err != nil {
		return History{}, err
	}
	if ValidateBranchName(proj.DefaultBranch) == nil {
		logx.Warn(fetchDefaultBranch(proj, ghToken), "project.log.history_fetch", "project", projectID)
	}
	return OpenHistory(proj.Path), nil
}

// IsAncestor runs git merge-base --is-ancestor: exit 0 is true, 1 is false, anything else (a commit
// the clone does not have) is an error.
func (h History) IsAncestor(ctx context.Context, base, head string) (bool, error) {
	if !historySHARe.MatchString(base) || !historySHARe.MatchString(head) {
		return false, apperr.Invalid("project.errors.history_sha_invalid", "base", base, "head", head)
	}
	cmd, err := h.g.commandContext(ctx, h.dir, []string{"merge-base", "--is-ancestor", base, head})
	if err != nil {
		return false, err
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, apperr.Wrap(err, "project.errors.git_command",
		"args", "merge-base --is-ancestor", "output", strings.TrimSpace(string(out)))
}

// Commits lists base..head newest first, at most limit; complete is false when there were more.
func (h History) Commits(ctx context.Context, base, head string, limit int) (commits []HistoryCommit, complete bool, err error) {
	if !historySHARe.MatchString(base) || !historySHARe.MatchString(head) {
		return nil, false, apperr.Invalid("project.errors.history_sha_invalid", "base", base, "head", head)
	}
	if limit <= 0 {
		limit = 300
	}
	args := []string{"log", "--format=%H%x1f%B%x1e", "--max-count=" + strconv.Itoa(limit+1), "--end-of-options", base + ".." + head}
	cmd, err := h.g.commandContext(ctx, h.dir, args)
	if err != nil {
		return nil, false, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, false, apperr.Wrap(err, "project.errors.git_command",
			"args", strings.Join(args, " "), "output", strings.TrimSpace(stderr.String()))
	}
	for _, rec := range strings.Split(string(out), "\x1e") {
		sha, msg, ok := strings.Cut(strings.TrimLeft(rec, "\n"), "\x1f")
		if !ok || sha == "" {
			continue
		}
		commits = append(commits, HistoryCommit{SHA: sha, Message: strings.TrimSpace(msg)})
	}
	if len(commits) > limit {
		return commits[:limit], false, nil
	}
	return commits, true, nil
}
