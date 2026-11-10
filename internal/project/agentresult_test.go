package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrateResultIntoWorktree(t *testing.T) {
	repo := t.TempDir()
	gitRunT(t, repo, "init", "-b", "main")
	writeF(t, filepath.Join(repo, "a.txt"), "base")
	gitRunT(t, repo, "add", ".")
	gitRunT(t, repo, "commit", "-m", "base")

	worktree := filepath.Join(t.TempDir(), "wt")
	gitRunT(t, repo, "worktree", "add", "-b", "task-x", worktree)

	agent := filepath.Join(t.TempDir(), "agent")
	gitRunT(t, "", "clone", repo, agent)
	gitRunT(t, agent, "checkout", "task-x")
	writeF(t, filepath.Join(agent, "b.txt"), "agent output")
	gitRunT(t, agent, "add", ".")
	gitRunT(t, agent, "commit", "-m", "agent work")
	gitRunT(t, agent, "push", "origin", "HEAD:octopus-result-x")

	if _, err := os.Stat(filepath.Join(worktree, "b.txt")); err == nil {
		t.Fatal("precondition: worktree already has agent output")
	}

	rewritten, err := integrateResultIntoWorktree(repo, worktree, "octopus-result-x")
	if err != nil {
		t.Fatalf("integrate: %v", err)
	}
	if rewritten {
		t.Error("a fast-forward is not a rewrite")
	}

	if _, err := os.Stat(filepath.Join(worktree, "b.txt")); err != nil {
		t.Errorf("worktree missing agent output after integrate: %v", err)
	}
	if got := gitOutT(t, worktree, "log", "-1", "--pretty=%s"); strings.TrimSpace(got) != "agent work" {
		t.Errorf("worktree HEAD = %q, want the agent commit", strings.TrimSpace(got))
	}
}

func TestIntegrateResultRejectsNonFastForward(t *testing.T) {
	repo := t.TempDir()
	gitRunT(t, repo, "init", "-b", "main")
	writeF(t, filepath.Join(repo, "a.txt"), "base")
	gitRunT(t, repo, "add", ".")
	gitRunT(t, repo, "commit", "-m", "base")

	worktree := filepath.Join(t.TempDir(), "wt")
	gitRunT(t, repo, "worktree", "add", "-b", "task-y", worktree)
	writeF(t, filepath.Join(worktree, "local.txt"), "local")
	gitRunT(t, worktree, "add", ".")
	gitRunT(t, worktree, "commit", "-m", "local divergence")

	agent := filepath.Join(t.TempDir(), "agent")
	gitRunT(t, "", "clone", repo, agent)
	gitRunT(t, agent, "checkout", "main")
	writeF(t, filepath.Join(agent, "other.txt"), "other")
	gitRunT(t, agent, "add", ".")
	gitRunT(t, agent, "commit", "-m", "divergent agent work")
	gitRunT(t, agent, "push", "origin", "HEAD:octopus-result-y")

	if _, err := integrateResultIntoWorktree(repo, worktree, "octopus-result-y"); err == nil {
		t.Fatal("expected a commit only the worktree has to stop the integration, got nil")
	}
	if got := gitOutT(t, worktree, "log", "-1", "--pretty=%s"); strings.TrimSpace(got) != "local divergence" {
		t.Errorf("worktree HEAD = %q, want its own commit kept", strings.TrimSpace(got))
	}

	if _, err := integrateResultIntoWorktree(repo, filepath.Join(t.TempDir(), "nope"), "x"); err == nil {
		t.Error("expected error for missing worktree")
	}
}

func TestIntegrateResultFollowsAnAgentRebase(t *testing.T) {
	repo := t.TempDir()
	gitRunT(t, repo, "init", "-b", "main")
	writeF(t, filepath.Join(repo, "a.txt"), "base")
	gitRunT(t, repo, "add", ".")
	gitRunT(t, repo, "commit", "-m", "base")
	worktree := filepath.Join(t.TempDir(), "wt")
	gitRunT(t, repo, "worktree", "add", "-b", "task-r", worktree)

	agent := filepath.Join(t.TempDir(), "agent")
	gitRunT(t, "", "clone", repo, agent)
	gitRunT(t, agent, "checkout", "task-r")
	writeF(t, filepath.Join(agent, "b.txt"), "agent output")
	gitRunT(t, agent, "add", ".")
	gitRunT(t, agent, "commit", "-m", "agent work")
	gitRunT(t, agent, "push", "origin", "HEAD:octopus-result-r")
	if _, err := integrateResultIntoWorktree(repo, worktree, "octopus-result-r"); err != nil {
		t.Fatalf("first integrate: %v", err)
	}

	writeF(t, filepath.Join(repo, "main.txt"), "upstream")
	gitRunT(t, repo, "add", ".")
	gitRunT(t, repo, "commit", "-m", "upstream change")
	gitRunT(t, agent, "fetch", "origin", "main")
	gitRunT(t, agent, "rebase", "origin/main")
	gitRunT(t, agent, "push", "--force", "origin", "HEAD:octopus-result-r")

	rewritten, err := integrateResultIntoWorktree(repo, worktree, "octopus-result-r")
	if err != nil {
		t.Fatalf("integrate after rebase: %v", err)
	}
	if !rewritten {
		t.Error("a rebased result must report a rewrite so the publish force-with-leases")
	}
	want := strings.TrimSpace(gitOutT(t, agent, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(gitOutT(t, worktree, "rev-parse", "HEAD")); got != want {
		t.Errorf("worktree HEAD = %s, want the agent's rebased tip %s", got, want)
	}
}

func TestIntegrateResultWithoutAPushIsANoOp(t *testing.T) {
	repo := t.TempDir()
	gitRunT(t, repo, "init", "-b", "main")
	writeF(t, filepath.Join(repo, "a.txt"), "base")
	gitRunT(t, repo, "add", ".")
	gitRunT(t, repo, "commit", "-m", "base")
	worktree := filepath.Join(t.TempDir(), "wt")
	gitRunT(t, repo, "worktree", "add", "-b", "task-n", worktree)

	rewritten, err := integrateResultIntoWorktree(repo, worktree, "octopus-result-n")
	if err != nil || rewritten {
		t.Fatalf("integrate with no result ref = (%v, %v), want (false, nil)", rewritten, err)
	}
}

func writeF(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func gitRunT(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := gitCmd(dir, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
	)
	return cmd
}
