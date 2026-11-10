// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func runArgv(t *testing.T, env []string, argv []string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", argv, err, out)
	}
	return string(out)
}

func readGitConfig(t *testing.T, repo string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGitCredentialHelperReadsEnvAtUseTime(t *testing.T) {
	requireGit(t)
	if gitCredentialHelper("bad name; rm -rf /") != "" {
		t.Fatal("an invalid env var name must yield no helper")
	}
	repo := t.TempDir()
	runArgv(t, nil, []string{"git", "init", "-q", repo})
	runArgv(t, nil, []string{"git", "-C", repo, "config", "credential.helper", gitCredentialHelper("OCTOPUS_API_KEY")})

	cmd := exec.Command("git", "-C", repo, "credential", "fill")
	cmd.Env = append(os.Environ(), "OCTOPUS_API_KEY=sk-live", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader("protocol=http\nhost=daemon:8585\n\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("credential fill: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "password=sk-live") || !strings.Contains(string(out), "username=octopus") {
		t.Errorf("helper must answer with the env key, got:\n%s", out)
	}
	if strings.Contains(readGitConfig(t, repo), "sk-live") {
		t.Error("the key must never be written to .git/config")
	}
}

func TestCloneCommandPersistsHelperNotKey(t *testing.T) {
	requireGit(t)
	remote := testgit.BareRepo(t)
	dest := filepath.Join(t.TempDir(), "app")
	wt := WorktreeMount{Name: "app", CloneURL: remote, Branch: "main", CredentialEnv: "OCTOPUS_API_KEY"}
	runArgv(t, []string{"OCTOPUS_API_KEY=sk-live"}, cloneCommand(wt, dest))

	cfg := readGitConfig(t, dest)
	if strings.Contains(cfg, "sk-live") {
		t.Errorf("clone wrote the key to .git/config:\n%s", cfg)
	}
	if !strings.Contains(cfg, "OCTOPUS_API_KEY") {
		t.Errorf("clone must persist the env-reading helper for the agent's later push:\n%s", cfg)
	}
}

func TestPVCCloneScrubsOriginCredential(t *testing.T) {
	requireGit(t)
	remote := testgit.BareRepo(t)
	dest := filepath.Join(t.TempDir(), "app")
	runArgv(t, nil, []string{"git", "clone", "-q", remote, dest})
	runArgv(t, nil, []string{"git", "-C", dest, "remote", "set-url", "origin", "http://octopus:old-key@daemon:8585/git/t1/app"})

	wt := WorktreeMount{Name: "app", CloneURL: "http://daemon:8585/git/t2/app", CredentialEnv: "OCTOPUS_API_KEY"}
	out := runArgv(t, []string{"OCTOPUS_API_KEY=sk-live"}, pvcCloneCommand(wt, dest))
	if !strings.Contains(out, "already hydrated") {
		t.Fatalf("existing clone must be reused, got: %s", out)
	}
	cfg := readGitConfig(t, dest)
	if strings.Contains(cfg, "old-key") {
		t.Errorf("key survived in origin:\n%s", cfg)
	}
	if !strings.Contains(cfg, "http://daemon:8585/git/t1/app") {
		t.Errorf("origin must keep its path, only lose the credential:\n%s", cfg)
	}
	if !strings.Contains(cfg, "OCTOPUS_API_KEY") {
		t.Errorf("helper must be installed on an adopted clone:\n%s", cfg)
	}
}

// A new pod's first clone can be refused until kube-router programs its NetworkPolicy; the clone
// must retry.
func TestCloneCommandRetriesUntilTheRemoteAnswers(t *testing.T) {
	requireGit(t)
	remote := testgit.BareRepo(t)
	away := remote + ".away"
	if err := os.Rename(remote, away); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(3*time.Second, func() { _ = os.Rename(away, remote) })
	defer timer.Stop()

	dest := filepath.Join(t.TempDir(), "app")
	wt := WorktreeMount{Name: "app", CloneURL: remote, Branch: "main", CredentialEnv: "OCTOPUS_API_KEY"}
	out := runArgv(t, nil, cloneCommand(wt, dest))
	if !strings.Contains(out, "clone attempt 1 failed") {
		t.Errorf("the first attempt should have failed and been retried:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Fatalf("clone did not land after the retries: %v\n%s", err, out)
	}
}
