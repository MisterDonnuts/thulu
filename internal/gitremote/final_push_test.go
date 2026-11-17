package gitremote

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MisterDonnuts/thulu/images"
)

// A pod's result-push.sh hands the run's result over with the final mark, so the daemon integrates it
// without publishing it to GitHub whatever the column says.
func TestThePodsResultPushIsMarkedFinal(t *testing.T) {
	if _, err := backendPath(); err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	served := setupBareRepo(t, t.TempDir(), "app.git")
	h, err := New(filepath.Dir(served), func(*http.Request, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	var marked, pushes atomic.Int32
	srv := httptest.NewServer(http.StripPrefix("/git", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			pushes.Add(1)
			if r.Header.Get(FinalPushHeader) != "" {
				marked.Add(1)
			}
		}
		h.ServeHTTP(w, r)
	})))
	defer srv.Close()

	root := t.TempDir()
	work := filepath.Join(root, "projects", "app")
	runGit(t, "", "clone", srv.URL+"/git/app.git", work)
	writeFile(t, filepath.Join(work, "result.txt"), "agent output")
	script, err := images.Runtime.ReadFile("snippets/result-push.sh")
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(t.TempDir(), "result-push.sh")
	writeFile(t, scriptPath, string(script))

	cmd := exec.CommandContext(t.Context(), "bash", "-c", `msg_en() { echo "$1"; }; source "$1"`, "bash", scriptPath)
	cmd.Env = append(os.Environ(), "OCTOPUS_ROOT="+root, "OCTOPUS_RESULT_REF=refs/heads/octopus-result-1",
		"OCTOPUS_API_KEY=", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("result-push.sh: %v\n%s", err, out)
	}
	if out := runGit(t, served, "show", "octopus-result-1:result.txt"); strings.TrimSpace(out) != "agent output" {
		t.Fatalf("the served repo's result branch holds %q", out)
	}
	if pushes.Load() == 0 || marked.Load() != pushes.Load() {
		t.Errorf("%d of %d result pushes carried %s", marked.Load(), pushes.Load(), FinalPushHeader)
	}
}
