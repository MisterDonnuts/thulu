package project

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/gitworktree"
)

// Agents can write task worktrees, and git runs programs a repo's config names, with the Docker
// socket in reach.
// So every daemon git call pins GIT_DIR from the daemon's clone (never the worktree's .git), loads
// only the clone's .git/config
// (read-only to containers via types.WorktreeGitMounts), pins exec knobs with -c, restricts
// protocols and scrubs inherited GIT_* vars.

var pinnedConfig = []string{
	"core.hooksPath=" + os.DevNull,
	"core.fsmonitor=false",
	"core.sshCommand=ssh",
	// An empty value resets the helper list, so no credential helper runs.
	"credential.helper=",
	"core.askPass=",
	// Never recurse into submodules or nested repos, where a planted .git/config would get a say.
	"submodule.recurse=false",
	"fetch.recurseSubmodules=false",
	"push.recurseSubmodules=no",
	"diff.ignoreSubmodules=all",
	"status.submoduleSummary=false",
	"commit.gpgSign=false",
	"tag.gpgSign=false",
	"protocol.allow=never",
}

func hardenedConfig() []string { return hardenedConfigFor(currentRemotePolicy().allowedProtocols()) }

func hardenedConfigFor(protos []string) []string {
	args := make([]string, 0, 2*(len(pinnedConfig)+len(protos)))
	for _, v := range pinnedConfig {
		args = append(args, "-c", v)
	}
	for _, p := range protos {
		args = append(args, "-c", "protocol."+p+".allow=always")
	}
	if saasMode() {
		// The egress check vets only the remote's host; a redirect would reach one it never saw
		// (gitegress.go).
		args = append(args, "-c", "http.followRedirects=false")
	}
	return args
}

func hardenedEnv(gitDir, workTree string) []string {
	return hardenedEnvFor(gitDir, workTree, currentRemotePolicy().allowedProtocols())
}

func hardenedEnvFor(gitDir, workTree string, protos []string) []string {
	parent := os.Environ()
	env := make([]string, 0, len(parent)+8)
	for _, kv := range parent {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL="+strings.Join(protos, ":"),
	)
	if gitDir != "" {
		env = append(env, "GIT_DIR="+gitDir)
	}
	if workTree != "" {
		env = append(env, "GIT_WORK_TREE="+workTree)
	}
	return env
}

type localGitExec struct {
	gitDir   string
	workTree string
	// file:// is opened only for clones of repos the daemon hosts itself (hosted.go); the policy
	// keeps it closed otherwise.
	hosted bool
	index  string
}

func (g localGitExec) protocols() []string {
	protos := currentRemotePolicy().allowedProtocols()
	if g.hosted && !slices.Contains(protos, "file") {
		protos = append(protos, "file")
	}
	return protos
}

func (g localGitExec) command(dir string, args []string) (*exec.Cmd, error) {
	return g.commandContext(context.Background(), dir, args)
}

func (g localGitExec) commandContext(ctx context.Context, dir string, args []string) (*exec.Cmd, error) {
	pins, err := g.gitEgressArgs(dir, args)
	if err != nil {
		return nil, err
	}
	protos := g.protocols()
	full := append(append(hardenedConfigFor(protos), pins...), args...)
	//nolint:gosec // G204: fixed binary; user-derived args are validated and sit after `--`/--end-of-options
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = hardenedEnvFor(g.gitDir, g.workTree, protos)
	if g.index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+g.index)
	}
	return cmd, nil
}

func (g localGitExec) Run(dir string, args ...string) error {
	return g.RunContext(context.Background(), dir, args...)
}

func (g localGitExec) RunContext(ctx context.Context, dir string, args ...string) error {
	cmd, err := g.commandContext(ctx, dir, args)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return apperr.Wrap(err, "project.errors.git_command",
			"args", strings.Join(args, " "), "output", strings.TrimSpace(string(out)))
	}
	return nil
}

func (g localGitExec) Output(dir string, args ...string) (string, error) {
	cmd, err := g.command(dir, args)
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", apperr.Wrap(err, "project.errors.git_command",
			"args", strings.Join(args, " "), "output", strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Safe only because the clone's checkout is never mounted into an agent and its .git only
// read-only.
func repoGit(projPath string) localGitExec {
	return localGitExec{gitDir: filepath.Join(projPath, ".git"), workTree: projPath, hosted: hostedClone(projPath)}
}

// Resolved from the clone's metadata, never from the worktree's agent-writable .git file.
func worktreeGitDir(projPath, worktreeDir string) (string, error) {
	common := filepath.Join(projPath, ".git")
	if fi, err := os.Lstat(common); err != nil || !fi.IsDir() {
		return "", apperr.New("project.errors.clone_no_git_dir", "path", projPath)
	}
	return gitworktree.AdminDir(common, worktreeDir)
}

func worktreeExec(projPath, worktreeDir string) (localGitExec, error) {
	gitDir, err := worktreeGitDir(projPath, worktreeDir)
	if err != nil {
		return localGitExec{}, err
	}
	return localGitExec{gitDir: gitDir, workTree: worktreeDir, hosted: hostedClone(projPath)}, nil
}
