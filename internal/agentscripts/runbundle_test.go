package agentscripts

import (
	"errors"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runBundle(t *testing.T, api string) (string, int) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	dir := snippetsDir(t)
	wrapper := "set -euo pipefail; source " + filepath.Join(dir, "i18n.sh") +
		"; source " + filepath.Join(dir, "run-bundle.sh")
	cmd := exec.Command(bashOrSkip(t), "-c", wrapper)
	cmd.Env = append(cmd.Environ(), "OCTOPUS_LOCALE=en", "OCTOPUS_API="+api,
		"OCTOPUS_API_KEY=run-key", "STAGE=development")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run-bundle.sh: %v\n%s", err, out)
	}
	return string(out), exitErr.ExitCode()
}

// Under the entrypoint's set -e a failed curl assignment would exit with curl's code and no
// message; it must reach the FATAL and exit 91.
func TestRunBundle_FailedCurlStillExplainsItself(t *testing.T) {
	out, code := runBundle(t, "http://[bad")
	if code != 91 {
		t.Errorf("exit = %d, want 91\n%s", code, out)
	}
	if !strings.Contains(out, "FATAL") || !strings.Contains(out, "http=000") {
		t.Errorf("output lacks the FATAL with http=000:\n%s", out)
	}
}

// A new pod's first requests are refused until the NetworkPolicy controller programs its IP, so
// refused connections are retried.
func TestRunBundle_RetriesARefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"no bundle yet"}`))
		}),
	}
	started := make(chan struct{})
	go func() {
		defer close(started)
		time.Sleep(500 * time.Millisecond)
		late, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			t.Errorf("listen again on %s: %v", addr, lerr)
			return
		}
		go func() { _ = srv.Serve(late) }()
	}()
	defer func() {
		<-started
		_ = srv.Close()
	}()

	out, code := runBundle(t, "http://"+addr)
	if code != 91 {
		t.Errorf("exit = %d, want 91\n%s", code, out)
	}
	if !strings.Contains(out, "http=409") || !strings.Contains(out, "no bundle yet") {
		t.Errorf("the retry never reached the daemon:\n%s", out)
	}
}
