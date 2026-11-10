package agentscripts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeDaemon struct {
	mu       sync.Mutex
	snapshot []byte
	uploaded []byte
	requests []string
	headers  []http.Header
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	f.headers = append(f.headers, r.Header.Clone())
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/tasks/t1/session-snapshot":
		if f.snapshot == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(f.snapshot)
	case r.Method == http.MethodGet && r.URL.Path == "/tasks/t1/conversation-transcript":
		_, _ = w.Write([]byte("**assistant:** drafted the plan\n"))
	case r.Method == http.MethodPut && r.URL.Path == "/tasks/t1/session-snapshot":
		f.uploaded, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// The race detector cannot see that curl exited before the test reads, so reads and resets go
// through the lock too.

func (f *fakeDaemon) seen() (requests []string, headers []http.Header, uploaded []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests), slices.Clone(f.headers), slices.Clone(f.uploaded)
}

func (f *fakeDaemon) serve(snapshot []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot, f.requests, f.headers = snapshot, nil, nil
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func untarGz(t *testing.T, b []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			names = append(names, strings.TrimPrefix(h.Name, "./"))
		}
	}
	sort.Strings(names)
	return names
}

func runSnapshot(t *testing.T, home, fn string, env ...string) string {
	t.Helper()
	dir := snippetsDir(t)
	wrapper := "set -eo pipefail; source " + filepath.Join(dir, "i18n.sh") +
		"; source " + filepath.Join(dir, "engines", "claude-code.sh") +
		"; source " + filepath.Join(dir, "session-snapshot.sh") + "; " + fn
	cmd := exec.Command(bashOrSkip(t), "-c", wrapper)
	// COPYFILE_DISABLE keeps macOS bsdtar from adding AppleDouble "._" files the image's GNU tar
	// never writes.
	cmd.Env = append(os.Environ(), append([]string{"OCTOPUS_LOCALE=en", "HOME=" + home, "COPYFILE_DISABLE=1",
		"TASK_ID=t1", "AGENT_NAME=developer", "OCTOPUS_API_KEY=run-key"}, env...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", fn, err, out)
	}
	return string(out)
}

func TestSessionSnapshot_FetchRestoresStateAndTranscript(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	daemon := &fakeDaemon{snapshot: tarGz(t, map[string]string{
		"./id": testSID + "\n", "./state/" + testSID + ".jsonl": "{}\n",
	})}
	srv := httptest.NewServer(daemon)
	defer srv.Close()
	tmp := t.TempDir()
	state, conv := filepath.Join(tmp, "engine-session"), filepath.Join(tmp, "conversation.md")

	runSnapshot(t, tmp, "_octopus_session_fetch", "OCTOPUS_API="+srv.URL, "SESSION_ID="+testSID,
		"SESSION_STATE_DIR="+state, "CONVERSATION_FILE="+conv)

	if _, err := os.Stat(filepath.Join(state, "state", testSID+".jsonl")); err != nil {
		t.Errorf("snapshot not unpacked: %v", err)
	}
	if b, _ := os.ReadFile(conv); !strings.Contains(string(b), "drafted the plan") {
		t.Errorf("transcript = %q", b)
	}
	want := []string{
		"GET /tasks/t1/session-snapshot?sid=" + testSID,
		"GET /tasks/t1/conversation-transcript?agent=developer",
	}
	requests, headers, _ := daemon.seen()
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests = %q, want %q", requests, want)
	}
	for _, h := range headers {
		if h.Get("X-API-Key") != "run-key" {
			t.Errorf("request without the run key: %v", h)
		}
	}

	daemon.serve(nil)
	runSnapshot(t, tmp, "_octopus_session_fetch", "OCTOPUS_API="+srv.URL, "SESSION_ID="+testSID,
		"SESSION_STATE_DIR="+state, "CONVERSATION_FILE="+conv)
	if _, err := os.Stat(filepath.Join(state, "state")); err == nil {
		t.Error("a stale state survived a 404")
	}
}

func TestSessionSnapshot_FetchWithoutSessionGetsOnlyTheTranscript(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	daemon := &fakeDaemon{}
	srv := httptest.NewServer(daemon)
	defer srv.Close()
	tmp := t.TempDir()
	state, conv := filepath.Join(tmp, "engine-session"), filepath.Join(tmp, "conversation.md")

	runSnapshot(t, tmp, "_octopus_session_fetch", "OCTOPUS_API="+srv.URL, "SESSION_ID=",
		"RESUME_MESSAGE=use Postgres", "SESSION_STATE_DIR="+state, "CONVERSATION_FILE="+conv)

	if b, _ := os.ReadFile(conv); !strings.Contains(string(b), "drafted the plan") {
		t.Errorf("transcript = %q", b)
	}
	if requests, _, _ := daemon.seen(); strings.Join(requests, "\n") != "GET /tasks/t1/conversation-transcript?agent=developer" {
		t.Errorf("requests = %q, want only the transcript", requests)
	}

	daemon.serve(nil)
	runSnapshot(t, tmp, "_octopus_session_fetch", "OCTOPUS_API="+srv.URL, "SESSION_ID=", "RESUME_MESSAGE=",
		"SESSION_STATE_DIR="+state, "CONVERSATION_FILE="+conv)
	if requests, _, _ := daemon.seen(); len(requests) != 0 {
		t.Errorf("a first run fetched %q", requests)
	}
}

func TestSessionSnapshot_UploadSendsTheAdaptersExport(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	daemon := &fakeDaemon{}
	srv := httptest.NewServer(daemon)
	defer srv.Close()
	tmp := t.TempDir()
	home, out := filepath.Join(tmp, "home"), filepath.Join(tmp, "run")
	writeFile(t, filepath.Join(home, ".claude", "projects", "-workspace", testSID+".jsonl"), "{}\n")
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), "secret")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	runSnapshot(t, home, "_octopus_session_upload", "OCTOPUS_API="+srv.URL, "OUTPUT_DIR="+out)

	if b, _ := os.ReadFile(filepath.Join(out, ".session_id")); strings.TrimSpace(string(b)) != testSID {
		t.Errorf(".session_id = %q", b)
	}
	requests, headers, uploaded := daemon.seen()
	if len(requests) != 1 || requests[0] != "PUT /tasks/t1/session-snapshot?sid="+testSID {
		t.Fatalf("requests = %q", requests)
	}
	if ct := headers[0].Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got, want := untarGz(t, uploaded), []string{"id", "state/" + testSID + ".jsonl"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("snapshot holds %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(out, ".engine-session", "state")); err == nil {
		t.Error("the state stayed in the run dir after the upload")
	}
}

func TestSessionSnapshot_NoSessionWritesEmptyIDAndUploadsNothing(t *testing.T) {
	daemon := &fakeDaemon{}
	srv := httptest.NewServer(daemon)
	defer srv.Close()
	tmp := t.TempDir()
	out := filepath.Join(tmp, "run")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	runSnapshot(t, filepath.Join(tmp, "home"), "_octopus_session_upload", "OCTOPUS_API="+srv.URL, "OUTPUT_DIR="+out)
	if b, err := os.ReadFile(filepath.Join(out, ".session_id")); err != nil || strings.TrimSpace(string(b)) != "" {
		t.Errorf(".session_id = %q, %v; want an empty file", b, err)
	}
	if requests, _, _ := daemon.seen(); len(requests) != 0 {
		t.Errorf("uploaded with no session: %q", requests)
	}
}
