package project

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
)

// A hostile remote can advertise anything as HEAD, so an invalid branch name falls through.
func detectDefaultBranch(repoPath string) string {
	g := repoGit(repoPath)
	if out, err := g.Output(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimPrefix(strings.TrimSpace(out), "refs/remotes/origin/")
		if ValidateBranchName(ref) == nil {
			return ref
		}
	}
	if out, err := g.Output(repoPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ValidateBranchName(ref) == nil {
			return ref
		}
	}
	return "main"
}

// With a token, SSH GitHub URLs become HTTPS: the daemon image has no ssh client or keys.
// The URL is re-validated here because this is the call that hands it to git.
func gitCloneAuth(ctx context.Context, ghToken, repoURL, dest, branch string) error {
	if ghToken != "" {
		repoURL = githubSSHToHTTPS(repoURL)
	}
	if err := ValidateRepoURL(repoURL); err != nil {
		return err
	}
	args := append(authRewriteArgs(ghToken), "clone", "--no-recurse-submodules")
	if branch != "" {
		if err := ValidateBranchName(branch); err != nil {
			return err
		}
		args = append(args, "--branch", branch)
	}
	args = append(args, "--", repoURL, dest)
	err := (localGitExec{}).RunContext(ctx, "", args...)
	if err == nil {
		return nil
	}
	var e *apperr.Error
	if !errors.As(err, &e) || e.Key != gitCommandKey {
		return err
	}
	return cloneFailure(cloneOutput(e, ghToken, repoURL, dest))
}

// Must match the key localGitExec.RunContext and Output wrap failures with; its "output" arg is
// what git printed.
const gitCommandKey = "project.errors.git_command"

func cloneOutput(e *apperr.Error, ghToken, repoURL, dest string) string {
	var out string
	for i := 0; i+1 < len(e.Args); i += 2 {
		if s, ok := e.Args[i+1].(string); ok && e.Args[i] == "output" {
			out = s
		}
	}
	if out == "" && e.Err != nil {
		out = e.Err.Error()
	}
	out = strings.ReplaceAll(redactToken(out, ghToken), repoURL, redactURL(repoURL))
	out = strings.ReplaceAll(out, dest, filepath.Base(dest))
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "Cloning into ") { //i18n:ignore git output matched, not shown
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// First match wins, so narrower patterns come first ("remote branch … not found" before "not
// found").
var cloneFailures = []struct {
	key      string
	patterns []string
}{
	{"project.errors.clone_branch_missing", []string{
		"remote branch", "couldn't find remote ref", //i18n:ignore git output matched, not shown
	}},
	{"project.errors.clone_host_unknown", []string{
		"could not resolve host", "couldn't resolve host", "name or service not known", //i18n:ignore git output matched, not shown
	}},
	{"project.errors.clone_unreachable", []string{
		"failed to connect", "could not connect", "couldn't connect", "connection refused", //i18n:ignore git output matched, not shown
		"connection timed out", "operation timed out", "network is unreachable", //i18n:ignore git output matched, not shown
		"no route to host", //i18n:ignore git output matched, not shown
	}},
	{"project.errors.clone_tls", []string{
		"ssl certificate problem", "server certificate verification failed", "ssl_connect", //i18n:ignore git output matched, not shown
	}},
	{"project.errors.clone_auth", []string{
		"authentication failed", "could not read username", "could not read password", //i18n:ignore git output matched, not shown
		"terminal prompts disabled", "invalid username or password", "error: 403", //i18n:ignore git output matched, not shown
		"permission to ", //i18n:ignore git output matched, not shown
	}},
	{"project.errors.clone_repo_missing", []string{
		"repository not found", "not found", "error: 404", //i18n:ignore git output matched, not shown
		"does not appear to be a git repository", "does not exist", //i18n:ignore git output matched, not shown
	}},
}

func cloneFailure(output string) error {
	low := strings.ToLower(output)
	key := "project.errors.clone_git"
	for _, f := range cloneFailures {
		if containsAny(low, f.patterns) {
			key = f.key
			break
		}
	}
	return apperr.WrapKind(apperr.KindUnprocessable, errors.New(output), key)
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// The token goes via -c so it is never persisted, and only the configured GitHub host is rewritten
// so it never reaches another server.
func authRewriteArgs(ghToken string) []string {
	if ghToken == "" {
		return nil
	}
	web := githubcfg.WebURL()
	u, err := url.Parse(web)
	if err != nil {
		return nil
	}
	authed := *u
	authed.User = url.UserPassword("x-access-token", ghToken)
	return []string{"-c", fmt.Sprintf("url.%s/.insteadOf=%s/", authed.String(), web)}
}

func redactToken(msg, ghToken string) string {
	if ghToken == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, url.QueryEscape(ghToken), "***")
	return strings.ReplaceAll(msg, ghToken, "***")
}

// The clone is daemon-owned, but its working tree holds remote content (.gitattributes, nested
// repos), so it still uses the hardened executor.
func gitRun(dir string, args ...string) error { return repoGit(dir).Run(dir, args...) }

func gitOutput(dir string, args ...string) (string, error) { return repoGit(dir).Output(dir, args...) }
