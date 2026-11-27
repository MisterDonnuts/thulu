package agentscripts

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type verdictSink struct {
	mu     sync.Mutex
	status int
	calls  []string
	ctype  string
	key    string
	body   string
}

func (v *verdictSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, r.Method+" "+r.URL.RequestURI())
	v.ctype, v.key = r.Header.Get("Content-Type"), r.Header.Get("X-API-Key")
	b, _ := io.ReadAll(r.Body)
	v.body = string(b)
	w.WriteHeader(v.status)
}

func runPostStageVerdict(t *testing.T, api, outDir string) string {
	t.Helper()
	dir := snippetsDir(t)
	wrapper := "source " + filepath.Join(dir, "i18n.sh") +
		"; source " + filepath.Join(dir, "post-results.sh") + "; _thulu_post_stage_verdict"
	cmd := exec.Command(bashOrSkip(t), "-c", wrapper)
	cmd.Env = append(os.Environ(), "OUTPUT_DIR="+outDir, "TASK_ID=t1", "OCTOPUS_API="+api,
		"INSTANCE_ID=qa-task-t1-verify-7", "OCTOPUS_API_KEY=run-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("_thulu_post_stage_verdict: %v\n%s", err, out)
	}
	return string(out)
}

func TestPostStageVerdictUploadsOnceWithTheRunKey(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	sink := &verdictSink{status: http.StatusCreated}
	srv := httptest.NewServer(sink)
	defer srv.Close()

	out := t.TempDir()
	const verdict = "---\nverdict: kick_back\nreason: gates_failed\n---\nTC-2 fails\n"
	if err := os.WriteFile(filepath.Join(out, "verdict.md"), []byte(verdict), 0o600); err != nil {
		t.Fatal(err)
	}
	runPostStageVerdict(t, srv.URL, out)
	runPostStageVerdict(t, srv.URL, out)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.calls) != 1 || sink.calls[0] != "POST /tasks/t1/stage-verdict?instance=qa-task-t1-verify-7" {
		t.Fatalf("calls = %v, want one POST to the stage-verdict route", sink.calls)
	}
	if sink.ctype != "text/markdown" || sink.key != "run-key" || sink.body != verdict {
		t.Errorf("upload = %q / key %q / %q", sink.ctype, sink.key, sink.body)
	}
	if _, err := os.Stat(filepath.Join(out, "verdict.posted")); err != nil {
		t.Errorf("no verdict.posted marker: the daemon would post it again: %v", err)
	}
}

func TestPostLockedTestsUploadsOnceWithTheRunKey(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	sink := &verdictSink{status: http.StatusCreated}
	srv := httptest.NewServer(sink)
	defer srv.Close()

	out := t.TempDir()
	const list = "internal/x/contract_test.go\n"
	if err := os.WriteFile(filepath.Join(out, "locked-tests.txt"), []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func() {
		wrapper := "source " + filepath.Join(snippetsDir(t), "i18n.sh") +
			"; source " + filepath.Join(snippetsDir(t), "post-results.sh") + "; _thulu_post_locked_tests"
		cmd := exec.Command(bashOrSkip(t), "-c", wrapper)
		cmd.Env = append(os.Environ(), "OUTPUT_DIR="+out, "TASK_ID=t1", "OCTOPUS_API="+srv.URL,
			"INSTANCE_ID=tester-task-t1-test_design-2", "OCTOPUS_API_KEY=run-key")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("_thulu_post_locked_tests: %v\n%s", err, b)
		}
	}
	run()
	run()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.calls) != 1 || sink.calls[0] != "POST /tasks/t1/locked-tests?instance=tester-task-t1-test_design-2" {
		t.Fatalf("calls = %v, want one POST to the locked-tests route", sink.calls)
	}
	if sink.ctype != "text/plain" || sink.key != "run-key" || sink.body != list {
		t.Errorf("upload = %q / key %q / %q", sink.ctype, sink.key, sink.body)
	}
}

func TestPostStageVerdictWarnsWhenRefused(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	srv := httptest.NewServer(&verdictSink{status: http.StatusForbidden})
	defer srv.Close()

	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "verdict.md"), []byte("---\nverdict: block\n---\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runPostStageVerdict(t, srv.URL, out); !strings.Contains(got, "could not upload verdict.md") {
		t.Errorf("output = %q, want the upload warning", got)
	}
	if _, err := os.Stat(filepath.Join(out, "verdict.posted")); err == nil {
		t.Error("a refused upload left the marker: the daemon would skip its own copy")
	}

	// No verdict.md, no request.
	empty := t.TempDir()
	sink := &verdictSink{status: http.StatusCreated}
	srv2 := httptest.NewServer(sink)
	defer srv2.Close()
	runPostStageVerdict(t, srv2.URL, empty)
	if len(sink.calls) != 0 {
		t.Errorf("calls without verdict.md = %v", sink.calls)
	}
}
