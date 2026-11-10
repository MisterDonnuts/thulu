package execagent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecRoundTrip(t *testing.T) {
	const token = "sandbox-token"
	srv := httptest.NewServer(mux(NewServer(token)))
	defer srv.Close()
	ctx := context.Background()

	res, err := Exec(ctx, srv.Client(), srv.URL, token, ExecRequest{Command: []string{"sh", "-c", "echo hello"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "hello" || res.ExitCode != 0 {
		t.Errorf("got stdout=%q exit=%d, want hello/0", res.Stdout, res.ExitCode)
	}

	res, err = Exec(ctx, srv.Client(), srv.URL, token, ExecRequest{Command: []string{"sh", "-c", "echo oops >&2; exit 7"}})
	if err != nil {
		t.Fatalf("Exec (failing cmd): %v", err)
	}
	if res.ExitCode != 7 || !strings.Contains(res.Stderr, "oops") {
		t.Errorf("got exit=%d stderr=%q, want 7/oops", res.ExitCode, res.Stderr)
	}
}

func TestExecRejectsBadToken(t *testing.T) {
	srv := httptest.NewServer(mux(NewServer("right")))
	defer srv.Close()
	_, err := Exec(context.Background(), srv.Client(), srv.URL, "wrong", ExecRequest{Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad token: err = %v, want a 401", err)
	}
}

func TestExecTimeout(t *testing.T) {
	const token = "t"
	srv := httptest.NewServer(mux(NewServer(token)))
	defer srv.Close()
	res, err := Exec(context.Background(), srv.Client(), srv.URL, token,
		ExecRequest{Command: []string{"sh", "-c", "sleep 5"}, TimeoutMs: 100})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !res.TimedOut {
		t.Errorf("expected TimedOut, got %+v", res)
	}
}

func TestServerRejectsGET(t *testing.T) {
	s := NewServer("t")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/exec", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

func mux(s *Server) http.Handler {
	m := http.NewServeMux()
	m.Handle("/exec", s)
	return m
}

func TestExecTimeoutKillsChildren(t *testing.T) {
	const token = "t"
	srv := httptest.NewServer(mux(NewServer(token)))
	defer srv.Close()
	marker := filepath.Join(t.TempDir(), "child-survived")
	script := "(sleep 1; touch " + marker + ") & sleep 5"
	res, err := Exec(context.Background(), srv.Client(), srv.URL, token,
		ExecRequest{Command: []string{"sh", "-c", script}, TimeoutMs: 200})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the timed-out command's child kept running and wrote the marker")
	}
}

func TestExecKeepsBackgroundChildrenOnSuccess(t *testing.T) {
	const token = "t"
	srv := httptest.NewServer(mux(NewServer(token)))
	defer srv.Close()
	marker := filepath.Join(t.TempDir(), "child-ran")
	script := "(sleep 0.3; touch " + marker + ") >/dev/null 2>&1 & exit 0"
	res, err := Exec(context.Background(), srv.Client(), srv.URL, token,
		ExecRequest{Command: []string{"sh", "-c", script}})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Exec: err=%v res=%+v", err, res)
	}
	time.Sleep(800 * time.Millisecond)
	if _, err := os.Stat(marker); err != nil {
		t.Error("a background child of a successful command was killed")
	}
}
