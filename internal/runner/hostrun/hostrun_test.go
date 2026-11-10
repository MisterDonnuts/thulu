package hostrun

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigRoundTripAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "runner.json")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("a missing config loaded")
	}
	c := &Config{DaemonURL: "http://d", Credential: "orn_x", Projects: map[string]string{"p1": "/src/app"}}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkDir != filepath.Join(filepath.Dir(path), "work") || got.Concurrency != 1 {
		t.Errorf("defaults: work dir %q, concurrency %d", got.WorkDir, got.Concurrency)
	}
	if ids := got.ProjectIDs(); len(ids) != 1 || ids[0] != "p1" {
		t.Errorf("ProjectIDs = %v", ids)
	}
}

func TestRedactRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://x-access-token:ghs_secret@github.com/o/r.git": "https://github.com/o/r.git",
		"https://github.com/o/r.git":                           "https://github.com/o/r.git",
		"git@github.com:o/r.git":                               "git@github.com:o/r.git",
		"/srv/git/r.git":                                       "/srv/git/r.git",
	} {
		if got := redactRemote(in); got != want {
			t.Errorf("redactRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestTaskWorktreeTracksOriginAndPushesARebase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@t")
	}
	ctx := context.Background()
	tmp := t.TempDir()
	remote, repo := filepath.Join(tmp, "remote.git"), filepath.Join(tmp, "repo")
	gitIn(t, tmp, "init", "-q", "--bare", "-b", "main", remote)
	gitIn(t, tmp, "clone", "-q", remote, repo)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:main")

	wt := filepath.Join(tmp, "wt")
	if _, err := ensureTaskWorktree(ctx, repo, wt, "task-1", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "-A")
	gitIn(t, wt, "commit", "-q", "-m", "a")
	gitIn(t, wt, "push", "-q")
	if got := gitIn(t, tmp, "--git-dir", remote, "log", "--format=%s", "-1", "task-1"); got != "a" {
		t.Fatalf("remote task-1 = %q after the agent's push", got)
	}

	other := filepath.Join(tmp, "other")
	gitIn(t, tmp, "clone", "-q", remote, other)
	gitIn(t, other, "commit", "-q", "--allow-empty", "-m", "upstream")
	gitIn(t, other, "push", "-q", "origin", "HEAD:main")
	gitIn(t, wt, "fetch", "-q", "origin")
	gitIn(t, wt, "rebase", "-q", "origin/main")
	commit, _, err := pushBranch(ctx, wt, "task-1")
	if err != nil {
		t.Fatalf("push after a rebase: %v", err)
	}
	if got := gitIn(t, tmp, "--git-dir", remote, "rev-parse", "task-1"); got != commit {
		t.Errorf("remote task-1 = %s, want %s", got, commit)
	}
}

func TestSweepEndsLeftoverRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix")
	}
	runs := t.TempDir()
	root := filepath.Join(runs, "job1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "entry.sh")
	if err := os.WriteFile(script, []byte("sleep 60\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	if err := os.WriteFile(filepath.Join(root, pidFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	stranger := filepath.Join(runs, "job2")
	if err := os.MkdirAll(stranger, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stranger, pidFile), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	sweepLeftovers(context.Background(), runs)

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the leftover run is still going")
	}
	if left, _ := os.ReadDir(runs); len(left) != 0 {
		t.Errorf("run folders left: %v", left)
	}
}

func TestSweepEndsAGroupWhoseLeaderDied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix")
	}
	runs := t.TempDir()
	root := filepath.Join(runs, "job1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	childPid := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $! > \""+childPid+"\"; wait")
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var child int
	for i := 0; i < 50 && child == 0; i++ {
		if b, err := os.ReadFile(childPid); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				child = n
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("the child never started")
	}
	defer func() {
		if err := syscall.Kill(child, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Log(err)
		}
	}()
	if err := os.WriteFile(filepath.Join(root, pidFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the leader exited cleanly; it was meant to be killed")
	}

	sweepLeftovers(context.Background(), runs)

	for i := 0; i < 100; i++ {
		if syscall.Kill(child, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the engine left in the group is still running")
}

func TestRelativeConfigPathGivesAnAbsoluteWorkDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := (&Config{DaemonURL: "http://d"}).Save("runner.json"); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig("runner.json")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.WorkDir) {
		t.Errorf("work dir %q is relative", c.WorkDir)
	}
}
