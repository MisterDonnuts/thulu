package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeMCP struct {
	mu       sync.Mutex
	sessions map[string]chan string

	tools        []ToolInfo
	handshake    atomic.Int32
	lastScope    atomic.Value
	omitEndpoint bool
	errorOnList  bool
	requireKey   bool
}

func newFakeMCP(tools ...ToolInfo) *fakeMCP {
	return &fakeMCP{sessions: make(map[string]chan string), tools: tools}
}

func (f *fakeMCP) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", f.serveSSE)
	mux.HandleFunc("/messages", f.serveMessages)
	return mux
}

func (f *fakeMCP) serveSSE(w http.ResponseWriter, r *http.Request) {
	f.handshake.Add(1)
	f.lastScope.Store(r.URL.Query().Get("scope"))
	if f.requireKey && r.Header.Get("X-API-Key") == "" && r.URL.Query().Get("catalog") != "1" {
		http.Error(w, "run key required", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	id := fmt.Sprintf("s%d", f.handshake.Load())
	out := make(chan string, 8)
	f.mu.Lock()
	f.sessions[id] = out
	f.mu.Unlock()

	if !f.omitEndpoint {
		fmt.Fprintf(w, ": keep-alive\n\n")
		fmt.Fprintf(w, "event: endpoint\ndata: /messages?sessionId=%s\n\n", id)
		flusher.Flush()
	}

	for {
		select {
		case msg := <-out:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fakeMCP) serveMessages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     *int   `json:"id"`
		Method string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	f.mu.Lock()
	out := f.sessions[r.URL.Query().Get("sessionId")]
	f.mu.Unlock()
	if out == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusAccepted)

	if req.ID == nil {
		return
	}
	var resp string
	switch req.Method {
	case "initialize":
		resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":%q}}`, *req.ID, ProtocolVersion)
	case "tools/list":
		if f.errorOnList {
			resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32001,"message":"nope"}}`, *req.ID)
			break
		}
		b, _ := json.Marshal(map[string]any{"tools": f.tools})
		resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, *req.ID, b)
	default:
		resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, *req.ID)
	}
	out <- resp
}

func startFake(t *testing.T, f *fakeMCP) string {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return srv.URL + "/sse"
}

func TestProberListsTools(t *testing.T) {
	f := newFakeMCP(
		ToolInfo{Name: "run_jobs", Description: "jobs of a run"},
		ToolInfo{Name: "run_logs", Description: "logs"},
	)
	url := startFake(t, f)

	got, err := NewProber().Tools(context.Background(), url)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(got) != 2 || got[0].Name != "run_jobs" || got[1].Name != "run_logs" {
		t.Fatalf("tools = %+v, want run_jobs + run_logs", got)
	}
	if got[0].Description != "jobs of a run" {
		t.Errorf("description = %q, want it preserved", got[0].Description)
	}
}

func TestProberRequestsWriteScope(t *testing.T) {
	f := newFakeMCP(ToolInfo{Name: "pr_create"})
	url := startFake(t, f)

	if _, err := NewProber().Tools(context.Background(), url); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if scope, _ := f.lastScope.Load().(string); scope != "write" {
		t.Errorf("scope = %q, want write", scope)
	}
}

func TestProberOverridesExistingScope(t *testing.T) {
	f := newFakeMCP(ToolInfo{Name: "pr_view"})
	url := startFake(t, f) + "?scope=read"

	if _, err := NewProber().Tools(context.Background(), url); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if scope, _ := f.lastScope.Load().(string); scope != "write" {
		t.Errorf("scope = %q, want write", scope)
	}
}

func TestProberCachesSuccess(t *testing.T) {
	f := newFakeMCP(ToolInfo{Name: "a"})
	url := startFake(t, f)
	p := NewProber()

	for i := 0; i < 3; i++ {
		if _, err := p.Tools(context.Background(), url); err != nil {
			t.Fatalf("Tools #%d: %v", i, err)
		}
	}
	if n := f.handshake.Load(); n != 1 {
		t.Errorf("handshakes = %d, want 1 (cached)", n)
	}
}

func TestProberCachesFailureBriefly(t *testing.T) {
	f := newFakeMCP()
	f.errorOnList = true
	url := startFake(t, f)

	p := NewProber()
	now := time.Now()
	p.now = func() time.Time { return now }

	if _, err := p.Tools(context.Background(), url); err == nil {
		t.Fatal("want error from tools/list")
	}
	if _, err := p.Tools(context.Background(), url); err == nil {
		t.Fatal("want cached error")
	}
	if n := f.handshake.Load(); n != 1 {
		t.Fatalf("handshakes = %d, want 1 while the failure is cached", n)
	}

	now = now.Add(probeFailureTTL + time.Second)
	f.errorOnList = false
	f.tools = []ToolInfo{{Name: "recovered"}}
	got, err := p.Tools(context.Background(), url)
	if err != nil {
		t.Fatalf("Tools after failure TTL: %v", err)
	}
	if len(got) != 1 || got[0].Name != "recovered" {
		t.Errorf("tools = %+v, want the recovered list", got)
	}
}

func TestProberCollapsesConcurrentProbes(t *testing.T) {
	f := newFakeMCP(ToolInfo{Name: "a"})
	url := startFake(t, f)
	p := NewProber()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Tools(context.Background(), url); err != nil {
				t.Errorf("Tools: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := f.handshake.Load(); n != 1 {
		t.Errorf("handshakes = %d, want 1 (collapsed)", n)
	}
}

func TestProberErrors(t *testing.T) {
	p := NewProber()

	if _, err := p.Tools(context.Background(), "  "); err == nil {
		t.Error("empty url: want error")
	}

	f := newFakeMCP()
	f.omitEndpoint = true
	url := startFake(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout+2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.Tools(ctx, url)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("no endpoint event: want error")
		}
	case <-ctx.Done():
		t.Error("probe hung past its timeout")
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer plain.Close()
	if _, err := NewProber().Tools(context.Background(), plain.URL); err == nil {
		t.Error("HTTP 404: want error")
	}
}

func TestResolveEndpoint(t *testing.T) {
	for _, tc := range []struct{ base, endpoint, want string }{
		{"http://h:3000/sse", "/messages?sessionId=x", "http://h:3000/messages?sessionId=x"},
		{"http://h:3000/a/sse", "messages?sessionId=x", "http://h:3000/a/messages?sessionId=x"},
		{"http://h:3000/sse", "http://other/msg", "http://other/msg"},
		{"http://h:3000/sse", "  /m?s=1  ", "http://h:3000/m?s=1"},
	} {
		got, err := resolveEndpoint(tc.base, tc.endpoint)
		if err != nil {
			t.Fatalf("resolveEndpoint(%q, %q): %v", tc.base, tc.endpoint, err)
		}
		if got != tc.want {
			t.Errorf("resolveEndpoint(%q, %q) = %q, want %q", tc.base, tc.endpoint, got, tc.want)
		}
	}
}

func TestSSEReaderMultilineData(t *testing.T) {
	raw := ": hi\n\nevent: endpoint\ndata: /messages?sessionId=z\n\ndata: {\"id\":1,\ndata: \"result\":{}}\n\n"
	r := newSSEReader(strings.NewReader(raw))

	ep, err := r.awaitEndpoint()
	if err != nil {
		t.Fatalf("awaitEndpoint: %v", err)
	}
	if ep != "/messages?sessionId=z" {
		t.Fatalf("endpoint = %q", ep)
	}
	if _, err := r.awaitResponse(1); err != nil {
		t.Fatalf("awaitResponse: %v", err)
	}
}

func TestProbeURLAsksForTheFullCatalog(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"no query", "http://mcp:3000/sse"},
		{"existing query is preserved", "http://mcp:3000/sse?task=t1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(probeURL(tc.in))
			if err != nil {
				t.Fatalf("probeURL produced an unparseable URL: %v", err)
			}
			if got := u.Query().Get("scope"); got != "write" {
				t.Errorf("scope = %q, want write", got)
			}
			if got := u.Query().Get("catalog"); got != "1" {
				t.Errorf("catalog = %q, want 1", got)
			}
		})
	}
	const bad = "://nope"
	if got := probeURL(bad); got != bad {
		t.Errorf("probeURL(%q) = %q, want it untouched", bad, got)
	}
}

func TestProberPing(t *testing.T) {
	upURL := startFake(t, newFakeMCP())

	gated := newFakeMCP()
	gated.requireKey = true
	gatedURL := startFake(t, gated)

	locked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "credential required", http.StatusUnauthorized)
	}))
	t.Cleanup(locked.Close)

	silent := newFakeMCP()
	silent.omitEndpoint = true
	silentURL := startFake(t, silent)

	notMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(notMCP.Close)

	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"announces its endpoint", upURL, false},
		{"opens the keyless catalog session", gatedURL, false},
		{"refuses every keyless session", locked.URL + "/sse", false},
		{"never announces an endpoint", silentURL, true},
		{"not an MCP server", notMCP.URL, true},
		{"nothing listening", "http://127.0.0.1:1/sse", true},
		{"empty url", "  ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), pingTimeout+2*time.Second)
			defer cancel()
			err := NewProber().Ping(ctx, tc.url)
			if (err != nil) != tc.wantErr {
				t.Errorf("Ping(%q) err = %v, wantErr %v", tc.url, err, tc.wantErr)
			}
			if ctx.Err() != nil {
				t.Error("ping outlived its own timeout")
			}
		})
	}
}

func TestPingURLAsksForTheCatalogSession(t *testing.T) {
	got, err := url.Parse(pingURL("http://thulu-browser:3000/sse?task=t1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Query().Get("catalog") != "1" || got.Query().Get("task") != "t1" {
		t.Fatalf("pingURL = %s, want catalog=1 added and task kept", got)
	}
}

func TestProberPingCaches(t *testing.T) {
	f := newFakeMCP()
	url := startFake(t, f)
	p := NewProber()
	now := time.Now()
	p.now = func() time.Time { return now }

	for range 3 {
		if err := p.Ping(context.Background(), url); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	}
	if n := f.handshake.Load(); n != 1 {
		t.Fatalf("sessions opened = %d, want 1 inside the cache window", n)
	}

	if _, err := p.Tools(context.Background(), url); err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if n := f.handshake.Load(); n != 2 {
		t.Fatalf("sessions opened = %d, want Tools to run its own handshake", n)
	}

	now = now.Add(pingCacheTTL + time.Second)
	if err := p.Ping(context.Background(), url); err != nil {
		t.Fatalf("Ping after TTL: %v", err)
	}
	if n := f.handshake.Load(); n != 3 {
		t.Fatalf("sessions opened = %d, want a fresh ping past the TTL", n)
	}
}
