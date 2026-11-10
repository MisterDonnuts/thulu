package llmgateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newGateway(t *testing.T, resolve Resolver) *httptest.Server {
	t.Helper()
	g, err := New(resolve)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	g.logf = func(string, ...any) {}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return srv
}

func TestInjectsRealKeyHidesRunToken(t *testing.T) {
	var gotAPIKey, gotAuth, gotToken, gotPath, gotOrg string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		gotToken = r.Header.Get(RunKeyHeader)
		gotOrg = r.Header.Get("OpenAI-Organization")
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	var gotProvider string
	srv := newGateway(t, func(_ context.Context, token, provider string) (Target, error) {
		gotProvider = provider
		if token != "run-abc" {
			return Target{}, ErrUnauthorized
		}
		return Target{Upstream: mustURL(t, upstream.URL), Credential: "sk-ant-REAL-TENANT-KEY", AuthStyle: AuthXAPIKey, AllowPrivate: true}, nil
	})

	req, _ := http.NewRequest("POST", srv.URL+"/anthropic/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", "run-abc")
	req.Header.Set("Authorization", "Bearer attacker")
	req.Header.Set("OpenAI-Organization", "org-attacker")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotProvider != ProviderAnthropic || gotPath != "/v1/messages" {
		t.Errorf("provider=%q path=%q, want anthropic + /v1/messages", gotProvider, gotPath)
	}
	if gotAPIKey != "sk-ant-REAL-TENANT-KEY" {
		t.Errorf("upstream x-api-key = %q, want the injected tenant key", gotAPIKey)
	}
	if gotAuth != "" || gotToken != "" || gotOrg != "" {
		t.Errorf("agent-controlled headers leaked: auth=%q token=%q org=%q", gotAuth, gotToken, gotOrg)
	}
}

func TestRejectsMissingUnknownAndForbidden(t *testing.T) {
	srv := newGateway(t, func(_ context.Context, token, _ string) (Target, error) {
		if token == "forbidden" {
			return Target{}, fmt.Errorf("%w: no workspace credential", ErrForbidden)
		}
		return Target{}, ErrUnauthorized
	})
	cases := []struct {
		name, path, token string
		want              int
	}{
		{"no token", "/anthropic/v1/messages", "", http.StatusUnauthorized},
		{"unknown token", "/anthropic/v1/messages", "bogus", http.StatusUnauthorized},
		{"forbidden run", "/anthropic/v1/messages", "forbidden", http.StatusForbidden},
		{"unknown provider", "/gemini/v1/x", "bogus", http.StatusNotFound},
		{"openai-compatible is routed", "/openai-compatible/chat/completions", "bogus", http.StatusUnauthorized},
		{"bare root", "/v1/messages", "bogus", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", srv.URL+c.path, nil)
			if c.token != "" {
				req.Header.Set(RunKeyHeader, c.token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.want)
			}
		})
	}
}

func TestBearerUpstreamWithBasePath(t *testing.T) {
	var gotAuth, gotPath, gotOrg string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotOrg = r.Header.Get("OpenAI-Organization")
		gotPath = r.URL.Path
	}))
	defer upstream.Close()

	srv := newGateway(t, func(context.Context, string, string) (Target, error) {
		return Target{
			Upstream: mustURL(t, upstream.URL+"/v1"), Credential: "sk-openai", AuthStyle: AuthBearer,
			Headers: map[string]string{"OpenAI-Organization": "org-1"}, AllowPrivate: true,
		}, nil
	})
	req, _ := http.NewRequest("POST", srv.URL+"/openai/responses", nil)
	req.Header.Set("Authorization", "Bearer run-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotAuth != "Bearer sk-openai" || gotPath != "/v1/responses" || gotOrg != "org-1" {
		t.Errorf("auth=%q path=%q org=%q", gotAuth, gotPath, gotOrg)
	}
}

func TestStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: two\n\n")
	}))
	defer upstream.Close()
	defer close(release)

	srv := newGateway(t, func(context.Context, string, string) (Target, error) {
		return Target{Upstream: mustURL(t, upstream.URL), Credential: "k", AllowPrivate: true}, nil
	})
	req, _ := http.NewRequest("POST", srv.URL+"/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "run")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, "event: one") {
			t.Errorf("first chunk = %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first SSE event was buffered")
	}
}

func TestRefusesInternalUpstreamUnlessAllowed(t *testing.T) {
	var hit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Store(true) }))
	defer upstream.Close()

	var allow atomic.Bool
	srv := newGateway(t, func(context.Context, string, string) (Target, error) {
		return Target{Upstream: mustURL(t, upstream.URL), Credential: "k", AllowPrivate: allow.Load()}, nil
	})
	do := func() int {
		req, _ := http.NewRequest("POST", srv.URL+"/anthropic/v1/messages", nil)
		req.Header.Set("x-api-key", "run")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := do(); code != http.StatusBadGateway || hit.Load() {
		t.Fatalf("internal upstream: status %d, reached=%v; want 502 and never reached", code, hit.Load())
	}
	allow.Store(true)
	if code := do(); code != http.StatusOK || !hit.Load() {
		t.Fatalf("vouched upstream: status %d, reached=%v", code, hit.Load())
	}
}

func TestInternalIPAndProxyHosts(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.8", "172.20.1.1", "192.168.1.1", "169.254.169.254", "100.64.3.4", "::1", "fd00::1", "fe80::1", "0.0.0.0"} {
		if !internalIP(net.ParseIP(ip)) {
			t.Errorf("%s should be internal", ip)
		}
	}
	for _, ip := range []string{"160.79.104.10", "8.8.8.8", "2607:6bc0::10"} {
		if internalIP(net.ParseIP(ip)) {
			t.Errorf("%s should be public", ip)
		}
	}
	got := proxyHosts(func(k string) string {
		return map[string]string{"HTTPS_PROXY": "http://Proxy.corp:3128", "http_proxy": "10.1.2.3:8080"}[k]
	})
	if !got["proxy.corp"] || !got["10.1.2.3"] || len(got) != 2 {
		t.Errorf("proxyHosts = %v", got)
	}
}
