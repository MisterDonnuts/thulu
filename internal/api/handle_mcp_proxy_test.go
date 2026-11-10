package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/mcp"
)

type recordingMCP struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (u *recordingMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.reqs = append(u.reqs, r.Clone(context.Background()))
	u.body = append(u.body, string(b))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(string(b), `"tools/list"`) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"search"},{"name":"delete_issue"}]}}`)
		return
	}
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
}

func (u *recordingMCP) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.reqs)
}

func (u *recordingMCP) last() *http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs[len(u.reqs)-1]
}

type proxyRig struct {
	*credFixture
	fwd *mcp.Forwarder
	reg *mcp.Registry
	up  *recordingMCP
	url string
}

func newProxyRig(t *testing.T, email string) *proxyRig {
	t.Helper()
	f := newCredFixture(t, email, strings.Split(email, "@")[0], fullCaps())
	fwd := mcp.NewForwarder()
	f.srv.mcp.fwd = fwd
	f.srv.taskDispatcher.WithMCPForwarder(fwd)
	reg, err := mcp.NewRegistry(f.srv.taskStore.DB())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	f.srv.SetMCPRegistry(reg)
	up := &recordingMCP{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	return &proxyRig{credFixture: f, fwd: fwd, reg: reg, up: up, url: srv.URL}
}

func (p *proxyRig) register(t *testing.T, r mcp.Registration) {
	t.Helper()
	got, err := p.reg.Register(context.Background(), r)
	if err != nil {
		t.Fatalf("register %s: %v", r.Name, err)
	}
	p.srv.forwardRegistration(&got)
}

func (p *proxyRig) call(t *testing.T, name, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp-proxy/"+name, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if key != "" {
		r.Header.Set("X-API-Key", key)
	}
	w := httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	return w
}

// Unknown routes fall through to the SPA with 200 HTML, so a test could pass without reaching the
// proxy.
func assertHandlerAnswered(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("the SPA answered, not the proxy: %d %s", w.Code, w.Body.String())
	}
}

func (p *proxyRig) createHeaderCredential(t *testing.T, name string, grants []map[string]string) {
	t.Helper()
	w := postJSONAuth(t, p.h, "/credentials", p.token, map[string]any{
		"name": name, "provider": "http_header", "kind": "basic",
		"payload": map[string]string{"username": "me@example.com", "token": "atlassian-token"},
		"grants":  grants,
	})
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create credential = %d: %s", w.Code, w.Body.String())
	}
}

const initializeCall = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

func TestIntegration_MCPProxy_RunKeyOnly(t *testing.T) {
	t.Parallel()
	p := newProxyRig(t, "proxyauth@test.com")
	p.register(t, mcp.Registration{Name: "thulu-cloud", URL: p.url + "/mcp", Transport: mcp.TransportHTTP})

	w := p.call(t, "thulu-cloud", "", initializeCall)
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Errorf("no key = %d, want 401/403", w.Code)
	}

	r := httptest.NewRequest(http.MethodPost, "/mcp-proxy/thulu-cloud", strings.NewReader(initializeCall))
	r.Header.Set("Authorization", "Bearer "+p.token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusForbidden {
		t.Errorf("session = %d, want 403: %s", w.Code, w.Body.String())
	}
	if p.up.count() != 0 {
		t.Error("a caller that is not a run reached the upstream")
	}
}

func TestIntegration_MCPProxy_AgentDefinitionGrants(t *testing.T) {
	t.Parallel()
	p := newProxyRig(t, "proxygrant@test.com")
	p.register(t, mcp.Registration{Name: "thulu-cloud", URL: p.url + "/mcp", Transport: mcp.TransportHTTP})
	p.register(t, mcp.Registration{Name: "jira", URL: p.url + "/jira", Transport: mcp.TransportHTTP})

	w := p.call(t, "thulu-cloud", p.taskKey, initializeCall)
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusOK {
		t.Fatalf("granted server = %d: %s", w.Code, w.Body.String())
	}
	got := p.up.last()
	if got.Header.Get("X-API-Key") != "" {
		t.Error("the run key reached a server not marked to receive it")
	}
	if got.URL.Query().Get("task") != p.taskID {
		t.Errorf("upstream query = %q, want task=%s", got.URL.RawQuery, p.taskID)
	}

	w = p.call(t, "jira", p.taskKey, initializeCall)
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusForbidden {
		t.Errorf("ungranted server = %d, want 403", w.Code)
	}
	if w := p.call(t, "nobody", p.taskKey, initializeCall); w.Code != http.StatusForbidden {
		t.Errorf("unknown server = %d, want 403", w.Code)
	}
}

func TestIntegration_MCPProxy_RecordedGrantAndAllowlist(t *testing.T) {
	t.Parallel()
	p := newProxyRig(t, "proxyallow@test.com")
	p.register(t, mcp.Registration{Name: "jira", URL: p.url + "/jira", Transport: mcp.TransportHTTP})
	if err := p.reg.RecordRunGrant(context.Background(), p.taskID, "developer", "in_progress",
		mcp.RunGrant{Tools: "jira@search"}); err != nil {
		t.Fatal(err)
	}

	before := p.up.count()
	w := p.call(t, "jira", p.taskKey, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"delete_issue"}}`)
	assertHandlerAnswered(t, w)
	if p.up.count() != before {
		t.Fatal("a tool outside the allowlist reached the upstream")
	}
	if !strings.Contains(w.Body.String(), `"error"`) || !strings.Contains(w.Body.String(), "delete_issue") {
		t.Errorf("refusal = %d %s", w.Code, w.Body.String())
	}

	w = p.call(t, "jira", p.taskKey, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if strings.Contains(w.Body.String(), "delete_issue") || !strings.Contains(w.Body.String(), `"search"`) {
		t.Errorf("tools/list = %s", w.Body.String())
	}

	p.register(t, mcp.Registration{Name: "thulu-cloud", URL: p.url + "/mcp", Transport: mcp.TransportHTTP})
	if w := p.call(t, "thulu-cloud", p.taskKey, initializeCall); w.Code != http.StatusForbidden {
		t.Errorf("server outside the recorded grant = %d, want 403", w.Code)
	}
}

func TestIntegration_MCPProxy_CredentialHeaders(t *testing.T) {
	t.Parallel()
	p := newProxyRig(t, "proxycred@test.com")
	p.createHeaderCredential(t, "jira-token", []map[string]string{{"subject_type": "project", "subject_id": p.projID}})
	p.createHeaderCredential(t, "elsewhere", []map[string]string{{"subject_type": "project", "subject_id": "proj-not-this-task"}})
	p.register(t, mcp.Registration{Name: "jira", URL: p.url + "/jira", Transport: mcp.TransportHTTP,
		Auth: mcp.AuthCredential, CredentialRef: "jira-token"})
	p.register(t, mcp.Registration{Name: "slack", URL: p.url + "/slack", Transport: mcp.TransportHTTP,
		Auth: mcp.AuthCredential, CredentialRef: "elsewhere"})
	if err := p.reg.RecordRunGrant(context.Background(), p.taskID, "developer", "in_progress",
		mcp.RunGrant{Tools: "jira,slack"}); err != nil {
		t.Fatal(err)
	}

	w := p.call(t, "jira", p.taskKey, initializeCall)
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusOK {
		t.Fatalf("jira = %d: %s", w.Code, w.Body.String())
	}
	got := p.up.last()
	if got.Header.Get("Authorization") != "Basic bWVAZXhhbXBsZS5jb206YXRsYXNzaWFuLXRva2Vu" {
		t.Errorf("upstream Authorization = %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("X-API-Key") != "" {
		t.Error("the run key reached a third-party server")
	}
	if got.URL.RawQuery != "" {
		t.Errorf("a third-party server got the run's query: %q", got.URL.RawQuery)
	}

	before := p.up.count()
	w = p.call(t, "slack", p.taskKey, initializeCall)
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusForbidden {
		t.Errorf("credential granted elsewhere = %d, want 403: %s", w.Code, w.Body.String())
	}
	if p.up.count() != before {
		t.Error("a refused credential still reached the upstream")
	}
}

func TestIntegration_MCPProxy_SSEForwardsTheRunKey(t *testing.T) {
	t.Parallel()
	p := newProxyRig(t, "proxysse@test.com")
	var mu sync.Mutex
	var seen *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Clone(context.Background())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: endpoint\ndata: /messages?sessionId=upstream-1\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	p.register(t, mcp.Registration{Name: "thulu-cloud", URL: upstream.URL + "/sse", ForwardsCredential: true})

	srv := httptest.NewServer(p.h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/mcp-proxy/thulu-cloud", nil)
	req.Header.Set("X-API-Key", p.taskKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream = %d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	sc := bufio.NewScanner(resp.Body)
	var endpoint string
	for sc.Scan() {
		if after, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			endpoint = after
			break
		}
	}
	if !strings.HasPrefix(endpoint, "/mcp-proxy/thulu-cloud/messages?session=") {
		t.Errorf("endpoint = %q", endpoint)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen == nil {
		t.Fatal("the upstream was never called")
	}
	if seen.Header.Get("X-API-Key") != p.taskKey {
		t.Error("the run key did not reach the server that acts for the agent")
	}
	if seen.URL.Query().Get("task") != p.taskID {
		t.Errorf("upstream query = %q", seen.URL.RawQuery)
	}
}

func TestIntegration_ProbeMCP(t *testing.T) {
	rig := newOperatorRig(t)
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(oauth.Close)
	sse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: endpoint\ndata: /messages\n\n")
	}))
	t.Cleanup(sse.Close)

	for _, tc := range []struct {
		url       string
		transport string
		oauth     bool
	}{
		{oauth.URL + "/mcp", "", true},
		{sse.URL + "/sse", "sse", false},
	} {
		w := callAs(t, rig.h, http.MethodPost, "/mcps/probe", rig.opToken, "", map[string]string{"url": tc.url})
		assertHandlerAnswered(t, w)
		if w.Code != http.StatusOK {
			t.Fatalf("probe %s = %d: %s", tc.url, w.Code, w.Body.String())
		}
		var got mcp.Detection
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Transport != tc.transport || got.OAuth != tc.oauth {
			t.Errorf("probe %s = %+v", tc.url, got)
		}
	}
	if w := callAs(t, rig.h, http.MethodPost, "/mcps/probe", rig.opToken, "", map[string]string{"url": "ftp://x"}); w.Code != http.StatusBadRequest {
		t.Errorf("ftp URL = %d, want 400", w.Code)
	}
}

func TestIntegration_MCPProxy_RunnerRunOpensTheHostLoopback(t *testing.T) {
	t.Parallel()
	p := newProxyRig(t, "proxyhost@test.com")
	queries := make(chan url.Values, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries <- r.URL.Query()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: endpoint\ndata: /messages?sessionId=upstream-1\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(upstream.Close)
	p.register(t, mcp.Registration{Name: browserrun.ServerName, URL: upstream.URL + "/sse", ForwardsCredential: true})
	p.register(t, mcp.Registration{Name: "thulu-cloud", URL: upstream.URL + "/sse"})
	srv := httptest.NewServer(p.h)
	t.Cleanup(srv.Close)

	open := func(name string) url.Values {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/mcp-proxy/"+name, nil)
		req.Header.Set("X-API-Key", p.taskKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /mcp-proxy/%s = %d", name, resp.StatusCode)
		}
		select {
		case q := <-queries:
			return q
		case <-time.After(5 * time.Second):
			t.Fatalf("%s was never called", name)
			return nil
		}
	}
	grant := func(onRunner bool) {
		t.Helper()
		if err := p.reg.RecordRunGrant(context.Background(), p.taskID, "developer", "in_progress",
			mcp.RunGrant{Tools: "thulu-browser,thulu-cloud", OnRunner: onRunner}); err != nil {
			t.Fatal(err)
		}
	}

	grant(false)
	if q := open(browserrun.ServerName); q.Has(browserrun.HostLoopbackQuery) {
		t.Errorf("a run in a container got the host loopback: %v", q)
	}
	grant(true)
	if q := open(browserrun.ServerName); q.Get(browserrun.HostLoopbackQuery) != "1" || q.Get("task") != p.taskID {
		t.Errorf("a runner run's browser query = %v, want %s=1 and the task", q, browserrun.HostLoopbackQuery)
	}
	if q := open("thulu-cloud"); q.Has(browserrun.HostLoopbackQuery) {
		t.Errorf("another server of a runner run got the host loopback: %v", q)
	}
}
