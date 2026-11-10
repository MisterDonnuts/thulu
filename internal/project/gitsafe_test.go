package project

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestMain(m *testing.M) {
	os.Setenv(envAllowLocalRemotes, "1")
	os.Exit(m.Run())
}

func mustWorktreeExec(t *testing.T, projPath, worktreeDir string) localGitExec {
	t.Helper()
	g, err := worktreeExec(projPath, worktreeDir)
	if err != nil {
		t.Fatalf("worktreeExec: %v", err)
	}
	return g
}

func markerScript(t *testing.T, dir, name, marker string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := "#!/bin/sh\necho \"$0 $*\" >> '" + marker + "'\ncat\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func plantHostileRepo(t *testing.T, dir, marker string) {
	t.Helper()
	scripts := t.TempDir()
	evil := markerScript(t, scripts, "evil.sh", marker)
	hooks := filepath.Join(scripts, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"pre-commit", "post-commit", "commit-msg", "pre-push",
		"post-checkout", "post-merge", "pre-rebase", "post-rewrite", "reference-transaction"} {
		markerScript(t, hooks, h, marker)
	}
	cfg := "[core]\n\tfsmonitor = " + evil + "\n\thooksPath = " + hooks +
		"\n\tsshCommand = " + evil + "\n\tpager = " + evil +
		"\n[filter \"evil\"]\n\tclean = " + evil + "\n\tsmudge = " + evil + "\n\trequired = true" +
		"\n[diff \"evil\"]\n\ttextconv = " + evil + "\n\tcommand = " + evil +
		"\n[diff]\n\texternal = " + evil +
		"\n[credential]\n\thelper = !" + evil +
		"\n[include]\n\tpath = " + filepath.Join(scripts, "more.cfg") + "\n"
	if err := os.WriteFile(filepath.Join(scripts, "more.cfg"), []byte("[core]\n\tfsmonitor = "+evil+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, repo := range []string{dir, filepath.Join(dir, "vendor", "nested")} {
		gitDir := filepath.Join(repo, ".git")
		if err := os.RemoveAll(gitDir); err != nil {
			t.Fatal(err)
		}
		gitRunT(t, "", "init", "-q", "-b", "main", repo)
		writeF(t, filepath.Join(repo, "seed.txt"), "seed\n")
		gitRunT(t, repo, "add", "seed.txt")
		gitRunT(t, repo, "commit", "-q", "-m", "seed")
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, h := range []string{"pre-commit", "post-commit", "pre-push"} {
			markerScript(t, filepath.Join(gitDir, "hooks"), h, marker)
		}
	}
	writeF(t, filepath.Join(dir, ".gitattributes"), "* filter=evil diff=evil merge=evil\n")
	writeF(t, filepath.Join(dir, "vendor", "nested", ".gitattributes"), "* filter=evil diff=evil\n")
	writeF(t, filepath.Join(dir, "vendor", "nested", "x.txt"), "nested\n")
	writeF(t, filepath.Join(dir, "agent.txt"), "agent output\n")
}

func TestDaemonGitIgnoresAgentPlantedConfig(t *testing.T) {
	store := newTestStore(t)
	bare := testgit.BareRepo(t)

	home := t.TempDir()
	marker := filepath.Join(t.TempDir(), "PWNED")
	globalEvil := markerScript(t, home, "g.sh", marker)
	writeF(t, filepath.Join(home, ".gitconfig"), "[core]\n\tfsmonitor = "+globalEvil+"\n\thooksPath = "+home+"\n")
	markerScript(t, home, "pre-commit", marker)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)

	proj, err := store.Create(t.Context(), "Hostile", bare, CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wt, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "evil1")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	plantHostileRepo(t, wt, marker)
	t.Setenv("GIT_DIR", filepath.Join(wt, ".git"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.fsmonitor'='"+globalEvil+"'")
	clean := func(step string) {
		t.Helper()
		if ran, err := os.ReadFile(marker); err == nil {
			t.Fatalf("%s: agent-planted git configuration executed on the daemon:\n%s", step, ran)
		}
	}
	clean("plant")

	if changed, err := store.WorktreeHasChanges(t.Context(), proj.ID, "evil1"); err != nil || !changed {
		t.Fatalf("WorktreeHasChanges = %v, %v", changed, err)
	}
	clean("status")
	err = store.CommitWorktree(t.Context(), proj.ID, "evil1", "agent work")
	if !errors.Is(err, ErrNestedRepo) || !strings.Contains(err.Error(), "vendor/nested/") {
		t.Fatalf("CommitWorktree with a nested repo = %v, want ErrNestedRepo naming vendor/nested/", err)
	}
	clean("refused commit")
	if err := os.RemoveAll(filepath.Join(wt, "vendor", "nested", ".git")); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWorktree(t.Context(), proj.ID, "evil1", "agent work"); err != nil {
		t.Fatalf("CommitWorktree: %v", err)
	}
	clean("commit")
	writeF(t, filepath.Join(wt, "agent2.txt"), "more\n")
	if err := store.CommitWorktree(t.Context(), proj.ID, "evil1", "more agent work"); err != nil {
		t.Fatalf("CommitWorktree (2nd): %v", err)
	}
	clean("second commit")
	if _, err := store.GetBranchStatus(t.Context(), proj.ID, "evil1"); err != nil {
		t.Fatalf("GetBranchStatus: %v", err)
	}
	clean("branch status")
	if _, err := store.PushBranch(t.Context(), proj.ID, "evil1"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	clean("push")
	if err := store.RemoveTaskWorktree(t.Context(), proj.ID, "evil1"); err != nil {
		t.Fatalf("RemoveTaskWorktree: %v", err)
	}
	clean("remove")

	os.Unsetenv("GIT_DIR")
	os.Unsetenv("GIT_CONFIG_PARAMETERS")
	log := gitOutT(t, bare, "log", "--format=%s", "task-evil1")
	if !strings.Contains(log, "more agent work") || !strings.Contains(log, "agent work") {
		t.Errorf("remote task branch log = %q", log)
	}
}

func TestWorktreeGitDirIgnoresPointerFile(t *testing.T) {
	store := newTestStore(t)
	proj, err := store.Create(t.Context(), "Pointer", testgit.BareRepo(t), CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wt, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, "ptr")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	want, err := worktreeGitDir(proj.Path, wt)
	if err != nil {
		t.Fatalf("worktreeGitDir: %v", err)
	}
	writeF(t, filepath.Join(wt, ".git"), "gitdir: /tmp/attacker\n")
	got, err := worktreeGitDir(proj.Path, wt)
	if err != nil || got != want {
		t.Fatalf("worktreeGitDir after tamper = %q, %v; want %q", got, err, want)
	}
	if !strings.HasPrefix(got, filepath.Join(proj.Path, ".git", "worktrees")+string(filepath.Separator)) {
		t.Errorf("gitdir %q is outside the clone", got)
	}
	if _, err := worktreeGitDir(proj.Path, t.TempDir()); err == nil {
		t.Error("expected an error for an unregistered worktree")
	}
}

func TestValidateRepoURL(t *testing.T) {
	t.Setenv(envAllowLocalRemotes, "")
	t.Setenv(envAllowSSH, "")
	good := []string{
		"https://github.com/o/r",
		"https://github.com/o/r.git",
		"https://ghe.corp.example:8443/org/repo.git",
		"https://10.0.0.5/org/repo.git",
	}
	for _, u := range good {
		if err := ValidateRepoURL(u); err != nil {
			t.Errorf("ValidateRepoURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{
		"",
		" https://github.com/o/r",
		"--upload-pack=touch /tmp/x",
		"-uhttps://github.com/o/r",
		"ext::sh -c touch% /tmp/pwned",
		"fd::17",
		"file:///data/projects/other-tenant",
		"/data/projects/other-tenant",
		"../other-tenant",
		"git@github.com:o/r.git",
		"ssh://git@github.com/o/r.git",
		"http://github.com/o/r",
		"git://github.com/o/r",
		"https://x-access-token:ghp_secret@github.com/o/r",
		"https://user@github.com/o/r",
		"https://localhost/o/r",
		"https://127.0.0.1/o/r",
		"https://[::1]/o/r",
		"https://169.254.169.254/latest/meta-data",
		"https://0.0.0.0/o/r",
		"https://github.com/",
		"https://github.com/o/r?x=1",
		"https://github.com/o/r\n",
	}
	for _, u := range bad {
		err := ValidateRepoURL(u)
		if err == nil {
			t.Errorf("ValidateRepoURL(%q) = nil, want rejection", u)
			continue
		}
		if !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("ValidateRepoURL(%q) error %v does not wrap ErrInvalidRepoURL", u, err)
		}
		if strings.Contains(err.Error(), "ghp_secret") {
			t.Errorf("error leaks the embedded token: %v", err)
		}
	}
}

func TestValidateRepoURLOptIns(t *testing.T) {
	t.Setenv(envAllowSSH, "1")
	t.Setenv(envAllowLocalRemotes, "")
	for _, u := range []string{"git@github.com:o/r.git", "ssh://git@ghe.corp/o/r.git"} {
		if err := ValidateRepoURL(u); err != nil {
			t.Errorf("with ssh allowed, ValidateRepoURL(%q) = %v", u, err)
		}
	}
	if err := ValidateRepoURL("/data/projects/x"); err == nil {
		t.Error("ssh opt-in must not allow local paths")
	}
	t.Setenv(envAllowLocalRemotes, "1")
	for _, u := range []string{"/data/projects/x", "file:///data/x", "https://localhost/o/r"} {
		if err := ValidateRepoURL(u); err != nil {
			t.Errorf("with local allowed, ValidateRepoURL(%q) = %v", u, err)
		}
	}
	for _, u := range []string{"ext::sh -c id", "https://169.254.169.254/x", "relative/path", "--upload-pack=x"} {
		if err := ValidateRepoURL(u); err == nil {
			t.Errorf("ValidateRepoURL(%q) = nil even with every opt-in", u)
		}
	}
}

func TestValidateRepoURLResolved(t *testing.T) {
	t.Setenv(envAllowLocalRemotes, "")
	orig := lookupIPAddr
	t.Cleanup(func() { lookupIPAddr = orig })
	answers := map[string]string{
		"metadata.evil.example": "169.254.169.254",
		"loop.evil.example":     "127.0.0.1",
		"ghe.corp.example":      "10.1.2.3",
	}
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if ip, ok := answers[host]; ok {
			return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
		}
		return nil, errors.New("no such host")
	}
	for _, u := range []string{"https://metadata.evil.example/o/r", "https://loop.evil.example/o/r", "https://nx.example/o/r"} {
		if err := ValidateRepoURLResolved(t.Context(), u); !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("ValidateRepoURLResolved(%q) = %v, want ErrInvalidRepoURL", u, err)
		}
	}
	if err := ValidateRepoURLResolved(t.Context(), "https://ghe.corp.example/o/r"); err != nil {
		t.Errorf("private GHES host rejected: %v", err)
	}
}

func TestValidateBranchName(t *testing.T) {
	for _, b := range []string{"main", "master", "release/1.2", "task-abc123", "feat_x", "octopus-result-9f"} {
		if err := ValidateBranchName(b); err != nil {
			t.Errorf("ValidateBranchName(%q) = %v", b, err)
		}
	}
	for _, b := range []string{
		"", "-x", "--upload-pack=touch /tmp/x", "--output=/etc/passwd", "a..b", "a b", "a~1", "a^",
		"a:b", "a?", "a*", "a[", "a\\b", "a@{1}", "/a", "a/", "a//b", ".a", "a/.b", "a.lock",
		"a/b.lock/c", "a.", "HEAD", "@", "a\x00b", "a\nb", strings.Repeat("a", 256),
	} {
		if err := ValidateBranchName(b); !errors.Is(err, ErrInvalidBranch) {
			t.Errorf("ValidateBranchName(%q) = %v, want ErrInvalidBranch", b, err)
		}
	}
}

func TestCreateRejectsHostileInput(t *testing.T) {
	store := newTestStore(t)
	marker := filepath.Join(t.TempDir(), "PWNED")
	_, err := store.Create(t.Context(), "BadBranch", testgit.BareRepo(t), CreateOpts{
		WorkspaceID: testWS, DefaultBranch: "--upload-pack=touch " + marker,
	})
	if !errors.Is(err, ErrInvalidBranch) {
		t.Errorf("Create with option-shaped default_branch = %v, want ErrInvalidBranch", err)
	}
	t.Setenv(envAllowLocalRemotes, "")
	victim := testgit.BareRepo(t)
	for _, u := range []string{victim, "file://" + victim, "ext::sh -c touch% " + marker} {
		if _, err := store.Create(t.Context(), "Steal", u, CreateOpts{WorkspaceID: testWS}); !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("Create(%q) = %v, want ErrInvalidRepoURL", u, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hostile input executed a command")
	}
}

func TestUpdateRejectsHostileDefaultBranch(t *testing.T) {
	store := newTestStore(t)
	proj, err := store.Create(t.Context(), "UpBranch", testgit.BareRepo(t), CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	bad := "--upload-pack=id"
	if _, err := store.Update(t.Context(), proj.ID, Patch{DefaultBranch: &bad}); !errors.Is(err, ErrInvalidBranch) {
		t.Errorf("Update = %v, want ErrInvalidBranch", err)
	}
	reloaded, _ := store.Get(t.Context(), proj.ID)
	if reloaded.DefaultBranch != "main" {
		t.Errorf("default_branch changed to %q", reloaded.DefaultBranch)
	}
}

func TestWorktreeGitRefusesStoredHostileBranch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "PWNED")
	w := WorktreeGit{
		Exec: localGitExec{}, Dir: t.TempDir(), Branch: "task-x",
		DefaultBranch: "--upload-pack=touch " + marker,
	}
	if _, err := w.Push(""); !errors.Is(err, ErrInvalidBranch) {
		t.Errorf("Push = %v, want ErrInvalidBranch", err)
	}
	if _, err := w.Status(""); !errors.Is(err, ErrInvalidBranch) {
		t.Errorf("Status = %v, want ErrInvalidBranch", err)
	}
	w.DefaultBranch = "main"
	w.Remote = "ext::sh -c touch% " + marker
	if _, err := w.Push(""); !errors.Is(err, ErrInvalidRepoURL) {
		t.Errorf("Push with ext:: remote = %v, want ErrInvalidRepoURL", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hostile stored value executed a command")
	}
}

func TestTaskIDCannotEscapeWorktreesDir(t *testing.T) {
	store := newTestStore(t)
	proj, err := store.Create(t.Context(), "Escape", testgit.BareRepo(t), CreateOpts{WorkspaceID: testWS})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, id := range []string{"../../x", "a/b", "-x", "", "a b"} {
		if _, _, err := store.CreateTaskWorktree(t.Context(), proj.ID, id); err == nil {
			t.Errorf("CreateTaskWorktree(%q) = nil, want rejection", id)
		}
	}
}

func TestAuthRewriteTargetsConfiguredHostOnly(t *testing.T) {
	t.Setenv("OCTOPUS_GITHUB_URL", "https://ghe.corp.example")
	args := authRewriteArgs("tok123")
	if len(args) != 2 || args[1] != "url.https://x-access-token:tok123@ghe.corp.example/.insteadOf=https://ghe.corp.example/" {
		t.Errorf("authRewriteArgs = %q", args)
	}
	if got := githubSSHToHTTPS("git@ghe.corp.example:o/r.git"); got != "https://ghe.corp.example/o/r.git" {
		t.Errorf("githubSSHToHTTPS = %q", got)
	}
	if got := githubSSHToHTTPS("git@github.com:o/r.git"); got != "git@github.com:o/r.git" {
		t.Errorf("github.com SSH URL rewritten under GHES config: %q", got)
	}
	t.Setenv("OCTOPUS_GITHUB_URL", "")
	if args := authRewriteArgs("tok123"); args[1] != "url.https://x-access-token:tok123@github.com/.insteadOf=https://github.com/" {
		t.Errorf("default authRewriteArgs = %q", args)
	}
	if authRewriteArgs("") != nil {
		t.Error("empty token must add no rewrite")
	}
}

func TestHardenedEnvScrubsInheritedGitVars(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "touch /tmp/x")
	t.Setenv("GIT_EXEC_PATH", "/tmp/evil")
	env := strings.Join(hardenedEnv("/r/.git", "/r"), "\n")
	for _, bad := range []string{"GIT_SSH_COMMAND=", "GIT_EXEC_PATH="} {
		if strings.Contains(env, bad) {
			t.Errorf("hardened env keeps %s", bad)
		}
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_DIR=/r/.git", "GIT_WORK_TREE=/r", "GIT_ALLOW_PROTOCOL="} {
		if !strings.Contains(env, want) {
			t.Errorf("hardened env missing %s", want)
		}
	}
}

func TestValidateRepoURLResolvedWordsLookupFailures(t *testing.T) {
	t.Setenv(envAllowLocalRemotes, "")
	orig := lookupIPAddr
	t.Cleanup(func() { lookupIPAddr = orig })
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "nx.example" {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return nil, &net.DNSError{Err: "dial udp [2001:db8::53]:53: connect: network is unreachable", Name: host}
	}
	cases := map[string]string{
		"https://nx.example/o/r": `host "nx.example" does not exist`,
		"https://github.com/o/r": `the server could not resolve "github.com"`,
	}
	for u, want := range cases {
		err := ValidateRepoURLResolved(t.Context(), u)
		if !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("ValidateRepoURLResolved(%q) = %v, want ErrInvalidRepoURL", u, err)
		}
		msg := apperr.Localize(err, "en")
		if !strings.Contains(msg, want) || strings.Contains(msg, "dial udp") || strings.Contains(msg, "lookup") {
			t.Errorf("ValidateRepoURLResolved(%q) says %q, want %q and no resolver output", u, msg, want)
		}
	}
}
