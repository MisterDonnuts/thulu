package gitremote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestCloneAndPush(t *testing.T) {
	if _, err := backendPath(); err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	root := t.TempDir()
	served := setupBareRepo(t, root, "task1.git")

	const token = "scoped-task-token"
	authorized := 0
	h, err := New(root, func(r *http.Request, repo string) bool {
		if repo != "task1.git" {
			return false
		}
		_, pass, ok := r.BasicAuth()
		if ok && pass == token {
			authorized++
			return true
		}
		return false
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/git/", http.StripPrefix("/git", h))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cloneURL := strings.Replace(srv.URL, "http://", "http://octopus:"+token+"@", 1) + "/git/task1.git"
	work := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", cloneURL, work)

	if _, err := os.Stat(filepath.Join(work, "README.md")); err != nil {
		t.Fatalf("clone missing seeded file: %v", err)
	}

	writeFile(t, filepath.Join(work, "result.txt"), "agent output")
	runGit(t, work, "checkout", "-b", "result/agent-1")
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "agent result")
	runGit(t, work, "push", "origin", "result/agent-1")

	out := runGit(t, served, "rev-parse", "--verify", "result/agent-1")
	if strings.TrimSpace(out) == "" {
		t.Fatalf("pushed branch result/agent-1 not found in served repo")
	}
	if authorized == 0 {
		t.Errorf("authorize was never consulted")
	}
}

func TestUnauthorizedIsChallenged(t *testing.T) {
	if _, err := backendPath(); err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	root := t.TempDir()
	setupBareRepo(t, root, "task1.git")

	h, err := New(root, func(*http.Request, string) bool { return false })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(http.StripPrefix("/git", h))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/git/task1.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing Basic challenge header")
	}
}

func TestResolverServesArbitraryDir(t *testing.T) {
	if _, err := backendPath(); err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	repo := setupBareRepo(t, t.TempDir(), "webshop.git")

	var gotRepoPath string
	h, err := New("/unused", func(*http.Request, string) bool { return true })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.WithResolver(func(_ context.Context, repoPath string) (string, bool) {
		gotRepoPath = repoPath
		if repoPath == "task42/webshop" {
			return repo, true
		}
		return "", false
	})

	srv := httptest.NewServer(http.StripPrefix("/git", h))
	defer srv.Close()

	work := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", srv.URL+"/git/task42/webshop", work)
	if _, err := os.Stat(filepath.Join(work, "README.md")); err != nil {
		t.Fatalf("resolver clone missing seeded file: %v", err)
	}
	if gotRepoPath != "task42/webshop" {
		t.Errorf("resolver got repoPath %q, want task42/webshop", gotRepoPath)
	}

	resp, err := http.Get(srv.URL + "/git/task99/ghost/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unresolvable path status = %d, want 404", resp.StatusCode)
	}
}

func setupBareRepo(t *testing.T, root, name string) string {
	t.Helper()
	bare := testgit.BareRepoAt(t, filepath.Join(root, name))
	runGit(t, bare, "config", "http.receivepack", "true")
	return bare
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
