package sandbox

import (
	"bufio"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type headerAuthorizer struct{}

func (headerAuthorizer) Authorize(r *http.Request, taskID string) (string, int, error) {
	if r.Header.Get("X-Allow-Task") != taskID {
		return "", http.StatusForbidden, errors.New("not your task")
	}
	return r.Header.Get("X-Caller"), http.StatusOK, nil
}

func (headerAuthorizer) Principal(r *http.Request) string { return r.Header.Get("X-Caller") }

func newAuthzServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := NewMCPHandler(nil, nil).WithAuthorizer(headerAuthorizer{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sandbox/sse", h.HandleSSE)
	mux.HandleFunc("POST /sandbox/messages", h.HandleMessages)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestMCPSSE_AuthorizerRefusal(t *testing.T) {
	srv := newAuthzServer(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/sandbox/sse?task_id=victim", nil)
	req.Header.Set("X-Allow-Task", "mine")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestMCPMessages_OnlyTheOpeningCallerMayPost(t *testing.T) {
	srv := newAuthzServer(t)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/sandbox/sse?task_id=mine", nil)
	req.Header.Set("X-Allow-Task", "mine")
	req.Header.Set("X-Caller", "alice")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open = %d", resp.StatusCode)
	}
	endpoint := ""
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			endpoint = data
			break
		}
	}
	if endpoint == "" {
		t.Fatal("no endpoint event")
	}

	post := func(caller string) int {
		r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+endpoint,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		r.Header.Set("X-Caller", caller)
		pr, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = pr.Body.Close()
		return pr.StatusCode
	}
	if got := post("mallory"); got != http.StatusNotFound {
		t.Errorf("foreign caller = %d, want 404", got)
	}
	if got := post("alice"); got != http.StatusAccepted {
		t.Errorf("opening caller = %d, want 202", got)
	}
}
