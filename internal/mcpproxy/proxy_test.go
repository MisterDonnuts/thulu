package mcpproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
)

const toolsList = `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"pr_view"},{"name":"pr_merge"},{"name":"pr_diff"}]}}`

type streamableUpstream struct {
	mu      sync.Mutex
	calls   []string
	headers []http.Header
}

func (u *streamableUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.calls = append(u.calls, string(body))
	u.headers = append(u.headers, r.Header.Clone())
	u.mu.Unlock()
	if r.Header.Get("Authorization") == "Bearer wrong" {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://x/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Mcp-Session-Id", "up-session")
	if strings.Contains(string(body), `"tools/list"`) {
		if r.URL.Query().Get("sse") == "1" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message\ndata: "+toolsList+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, toolsList)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (u *streamableUpstream) last() (string, http.Header) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.calls) == 0 {
		return "", nil
	}
	return u.calls[len(u.calls)-1], u.headers[len(u.headers)-1]
}

func toolNames(t *testing.T, raw string) []string {
	t.Helper()
	var msg struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("not a tools/list result: %v\n%s", err, raw)
	}
	var out []string
	for _, tool := range msg.Result.Tools {
		out = append(out, tool.Name)
	}
	return out
}

func post(t *testing.T, h http.HandlerFunc, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/mcp-proxy/gh", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestStreamable(t *testing.T) {
	up := &streamableUpstream{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := New(srv.Client())
	target := Target{
		Name: "gh", URL: srv.URL + "/mcp", Transport: mcp.TransportHTTP,
		Header: http.Header{"Authorization": {"Bearer upstream-secret"}},
		Entry:  mcp.ParseToolEntry("gh@pr_view+pr_diff"),
	}
	h := func(w http.ResponseWriter, r *http.Request) { p.Streamable(w, r, target) }

	t.Run("injects the upstream credential and drops the run's", func(t *testing.T) {
		w := post(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pr_view"}}`,
			map[string]string{"X-API-Key": "oct_run", "Cookie": "s=1", "Mcp-Session-Id": "up-session"})
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		_, hdr := up.last()
		if hdr.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("upstream Authorization = %q", hdr.Get("Authorization"))
		}
		if hdr.Get("X-API-Key") != "" || hdr.Get("Cookie") != "" {
			t.Errorf("the run's own credentials reached the upstream: %v", hdr)
		}
		if hdr.Get("Mcp-Session-Id") != "up-session" {
			t.Error("Mcp-Session-Id was not forwarded")
		}
		if w.Header().Get("Mcp-Session-Id") != "up-session" {
			t.Error("Mcp-Session-Id did not come back")
		}
	})

	t.Run("refuses a call outside the allowlist without forwarding it", func(t *testing.T) {
		before := len(up.calls)
		w := post(t, h, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"pr_merge"}}`, nil)
		if len(up.calls) != before {
			t.Fatal("the refused call reached the upstream")
		}
		var resp struct {
			ID    int `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%v: %s", err, w.Body)
		}
		if resp.ID != 7 || resp.Error.Code != errNotAllowed || !strings.Contains(resp.Error.Message, "pr_merge") {
			t.Errorf("refusal = %+v", resp)
		}
	})

	t.Run("refuses a whole batch with one refused call", func(t *testing.T) {
		before := len(up.calls)
		w := post(t, h, `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pr_view"}},`+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pr_merge"}}]`, nil)
		if len(up.calls) != before {
			t.Fatal("the batch reached the upstream")
		}
		if !strings.HasPrefix(w.Body.String(), "[") || strings.Count(w.Body.String(), `"error"`) != 2 {
			t.Errorf("batch refusal = %s", w.Body)
		}
	})

	t.Run("refuses a call with no readable name", func(t *testing.T) {
		before := len(up.calls)
		post(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{}}`, nil)
		if len(up.calls) != before {
			t.Fatal("a nameless call reached the upstream")
		}
	})

	// The upstream's JSON.parse reads each of these as a call to pr_merge (or the check cannot read
	// it); none may reach the upstream.
	for _, tc := range []struct{ name, body string }{
		{"a case-variant method key", `{"jsonrpc":"2.0","id":1,"method":"tools/call","METHOD":"x","params":{"name":"pr_merge"}}`},
		{"a case-variant name key", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pr_merge","Name":"pr_view"}}`},
		{"a duplicate method key", `{"jsonrpc":"2.0","id":1,"method":"x","method":"tools/call","params":{"name":"pr_merge"}}`},
		{"a duplicate name key", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pr_view","name":"pr_merge"}}`},
		{"a case-variant params key", `{"jsonrpc":"2.0","id":1,"method":"tools/call","Params":{"name":"pr_view"},"params":{"name":"pr_merge"}}`},
		{"a byte-order mark", "\xEF\xBB\xBF" + `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pr_merge"}}`},
		{"a numeric jsonrpc", `{"jsonrpc":2,"id":1,"method":"tools/call","params":{"name":"pr_merge"}}`},
		{"invalid UTF-8", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"pr_merge\xff\"}}"},
		{"trailing data", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pr_merge"}}`},
		{"a batch with a case-variant element", `[{"jsonrpc":"2.0","id":1,"method":"tools/call","METHOD":"x","params":{"name":"pr_merge"}}]`},
		{"an empty batch", `[]`},
		{"not JSON", `method=tools/call&name=pr_merge`},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			before := len(up.calls)
			w := post(t, h, tc.body, nil)
			if len(up.calls) != before {
				t.Fatalf("the body reached the upstream (status %d)", w.Code)
			}
			if w.Code != http.StatusBadRequest && !strings.Contains(w.Body.String(), `"error"`) {
				t.Errorf("status %d: %s", w.Code, w.Body)
			}
		})
	}

	t.Run("sends the body as plain UTF-8 JSON whatever charset the client named", func(t *testing.T) {
		w := post(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"pr_view"}}`,
			map[string]string{"Content-Type": "application/json; charset=utf-7"})
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if _, hdr := up.last(); hdr.Get("Content-Type") != "application/json" {
			t.Errorf("upstream Content-Type = %q", hdr.Get("Content-Type"))
		}
	})

	t.Run("filters a JSON tools/list", func(t *testing.T) {
		w := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
		if got := toolNames(t, w.Body.String()); strings.Join(got, ",") != "pr_view,pr_diff" {
			t.Errorf("tools = %v", got)
		}
	})

	t.Run("filters an event-stream tools/list", func(t *testing.T) {
		sse := target
		sse.URL += "?sse=1"
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/mcp-proxy/gh", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		p.Streamable(w, r, sse)
		data := strings.TrimPrefix(strings.Split(w.Body.String(), "\n")[1], "data: ")
		if got := toolNames(t, data); strings.Join(got, ",") != "pr_view,pr_diff" {
			t.Errorf("tools = %v (%s)", got, w.Body)
		}
	})

	t.Run("an unrestricted entry passes everything", func(t *testing.T) {
		open := target
		open.Entry = mcp.ParseToolEntry("gh@write")
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/mcp-proxy/gh", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		p.Streamable(w, r, open)
		if got := toolNames(t, w.Body.String()); len(got) != 3 {
			t.Errorf("tools = %v", got)
		}
	})

	t.Run("an upstream 401 is the daemon's problem, not an OAuth prompt", func(t *testing.T) {
		bad := target
		bad.Header = http.Header{"Authorization": {"Bearer wrong"}}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/mcp-proxy/gh", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		p.Streamable(w, r, bad)
		if w.Code != http.StatusBadGateway {
			t.Errorf("status = %d, want 502", w.Code)
		}
		if w.Header().Get("WWW-Authenticate") != "" {
			t.Error("the upstream's OAuth challenge reached the agent")
		}
	})
}

type sseUpstream struct {
	endpoint string
	out      chan string
	mu       sync.Mutex
	posts    []string
	headers  []http.Header
}

func (u *sseUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/sse":
		u.mu.Lock()
		u.headers = append(u.headers, r.Header.Clone())
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: endpoint\ndata: "+u.endpoint+"\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case msg := <-u.out:
				_, _ = io.WriteString(w, "event: message\ndata: "+msg+"\n\n")
				w.(http.Flusher).Flush()
			}
		}
	case r.Method == http.MethodPost && r.URL.Path == "/messages":
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.posts = append(u.posts, string(body))
		u.headers = append(u.headers, r.Header.Clone())
		u.mu.Unlock()
		if strings.Contains(string(body), "tools/list") {
			u.out <- toolsList
		} else {
			u.out <- string(body)
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		http.NotFound(w, r)
	}
}

type sseClient struct {
	t      *testing.T
	events chan [2]string
	cancel context.CancelFunc
}

func (c *sseClient) next() (string, string) {
	c.t.Helper()
	select {
	case ev, ok := <-c.events:
		if !ok {
			c.t.Fatal("stream closed")
		}
		return ev[0], ev[1]
	case <-time.After(5 * time.Second):
		c.t.Fatal("no event within 5s")
	}
	return "", ""
}

func sseRig(t *testing.T, endpoint string) (*sseUpstream, string) {
	t.Helper()
	up := &sseUpstream{endpoint: endpoint, out: make(chan string, 4)}
	upSrv := httptest.NewServer(up)
	t.Cleanup(upSrv.Close)
	p := New(upSrv.Client())
	target := Target{
		Name: "gh", URL: upSrv.URL + "/sse?task=t1", Transport: mcp.TransportSSE,
		Header: http.Header{"X-Api-Key": {"oct_run_for_upstream"}},
		Entry:  mcp.ParseToolEntry("gh@pr_view+pr_diff"),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mcp-proxy/gh", func(w http.ResponseWriter, r *http.Request) {
		p.OpenSSE(w, r, target, r.Header.Get("X-Owner"), func(id string) string {
			return "/mcp-proxy/gh/messages?session=" + id
		})
	})
	mux.HandleFunc("POST /mcp-proxy/gh/messages", func(w http.ResponseWriter, r *http.Request) {
		p.PostSSE(w, r, target, r.Header.Get("X-Owner"), r.URL.Query().Get("session"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return up, srv.URL
}

func postAs(t *testing.T, url, owner, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Owner", owner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestSSE(t *testing.T) {
	up, base := sseRig(t, "/messages?sessionId=upstream-secret-session")

	c := openSSEAs(t, base+"/mcp-proxy/gh", "key-1")

	name, endpoint := c.next()
	if name != "endpoint" || !strings.HasPrefix(endpoint, "/mcp-proxy/gh/messages?session=") {
		t.Fatalf("endpoint event = %q %q", name, endpoint)
	}
	if strings.Contains(endpoint, "upstream-secret-session") {
		t.Error("the upstream session URL reached the client")
	}
	up.mu.Lock()
	if got := up.headers[0].Get("X-Api-Key"); got != "oct_run_for_upstream" {
		t.Errorf("stream request X-API-Key = %q", got)
	}
	up.mu.Unlock()

	if resp := postAs(t, base+endpoint, "key-1", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("post status %d", resp.StatusCode)
	}
	if _, data := c.next(); strings.Join(toolNames(t, data), ",") != "pr_view,pr_diff" {
		t.Errorf("tools/list on the stream = %s", data)
	}
	up.mu.Lock()
	if got := up.headers[len(up.headers)-1].Get("X-Api-Key"); got != "oct_run_for_upstream" {
		t.Errorf("message request X-API-Key = %q", got)
	}
	up.mu.Unlock()

	posts := len(up.posts)
	if resp := postAs(t, base+endpoint, "key-1", `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"pr_merge"}}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("refused post status %d", resp.StatusCode)
	}
	if _, data := c.next(); !strings.Contains(data, `"id":9`) || !strings.Contains(data, `"error"`) {
		t.Errorf("refusal on the stream = %s", data)
	}
	if len(up.posts) != posts {
		t.Error("the refused call reached the upstream")
	}

	posts = len(up.posts)
	if resp := postAs(t, base+endpoint, "key-1", `{"jsonrpc":"2.0","id":10,"method":"tools/call","METHOD":"x","params":{"name":"pr_merge"}}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("case-variant post status %d, want 400", resp.StatusCode)
	}
	if len(up.posts) != posts {
		t.Error("the case-variant call reached the upstream")
	}

	if resp := postAs(t, base+endpoint, "key-2", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another key's post = %d, want 404", resp.StatusCode)
	}

	c.cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if resp := postAs(t, base+endpoint, "key-1", `{}`); resp.StatusCode == http.StatusNotFound {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the session outlived its stream")
}

func openSSEAs(t *testing.T, url, owner string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("X-Owner", owner)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	c := &sseClient{t: t, events: make(chan [2]string, 16), cancel: cancel}
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var name, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				c.events <- [2]string{name, data}
				name, data = "", ""
			}
		}
		close(c.events)
	}()
	t.Cleanup(cancel)
	return c
}

func TestSSEEndpointOnAnotherOrigin(t *testing.T) {
	_, base := sseRig(t, "https://attacker.example/messages")
	c := openSSEAs(t, base+"/mcp-proxy/gh", "key-1")
	select {
	case ev, ok := <-c.events:
		if ok {
			t.Errorf("the client got %v from a stream that should have been dropped", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stayed open")
	}
}

type fakeAuthorizer struct {
	d   credentials.Decision
	got credentials.ServerRequest
}

func (f *fakeAuthorizer) AuthorizeServer(_ context.Context, req credentials.ServerRequest) (credentials.Decision, error) {
	f.got = req
	return f.d, nil
}

type fakeStore struct {
	cred    credentials.Credential
	payload map[string]string
}

func (f *fakeStore) Get(context.Context, string, string) (*credentials.Credential, error) {
	c := f.cred
	return &c, nil
}

func (f *fakeStore) Payload(context.Context, string, string) (map[string]string, error) {
	return f.payload, nil
}

func TestStaticHeaderAuth(t *testing.T) {
	reg := mcp.Registration{Name: "jira", Auth: mcp.AuthCredential, CredentialRef: "jira-token"}
	run := Run{TaskID: "t1", Agent: "dev", Stage: "build", Reach: credentials.Reach{WorkspaceID: "ws1"}}
	store := &fakeStore{
		cred:    credentials.Credential{ID: "c1", Provider: credentials.HeaderProviderID, Kind: credentials.HeaderKindBasic},
		payload: map[string]string{"username": "me@x.com", "token": "atl"},
	}

	authz := &fakeAuthorizer{d: credentials.Decision{Allowed: true, CredentialID: "c1"}}
	a := &StaticHeaderAuth{Authorizer: authz, Store: store}
	h, err := a.UpstreamHeaders(context.Background(), reg, run)
	if err != nil {
		t.Fatal(err)
	}
	if h.Get("Authorization") != "Basic bWVAeC5jb206YXRs" {
		t.Errorf("Authorization = %q", h.Get("Authorization"))
	}
	if authz.got.CredentialRef != "jira-token" || authz.got.Server != "jira" || authz.got.Reach.WorkspaceID != "ws1" {
		t.Errorf("asked the authorizer %+v", authz.got)
	}

	var refused []credentials.Decision
	deny := &StaticHeaderAuth{
		Authorizer: &fakeAuthorizer{d: credentials.Decision{Reason: credentials.ReasonNoCredential}},
		Store:      store,
		OnRefusal: func(_ context.Context, _ mcp.Registration, _ Run, d credentials.Decision) {
			refused = append(refused, d)
		},
	}
	_, err = deny.UpstreamHeaders(context.Background(), reg, run)
	var r *Refusal
	if !asRefusal(err, &r) || r.Decision.Reason != credentials.ReasonNoCredential {
		t.Errorf("err = %v, want a not-granted refusal", err)
	}
	if len(refused) != 1 {
		t.Errorf("OnRefusal called %d times", len(refused))
	}

	mismatch := &StaticHeaderAuth{Authorizer: authz, Store: &fakeStore{
		cred:    credentials.Credential{ID: "c2", Provider: "github", Kind: "pat"},
		payload: map[string]string{"token": "ghp"},
	}}
	_, err = mismatch.UpstreamHeaders(context.Background(), reg, run)
	if !asRefusal(err, &r) || r.Decision.Reason != credentials.ReasonHeaderMismatch {
		t.Errorf("err = %v, want a mismatch refusal", err)
	}
}

func asRefusal(err error, out **Refusal) bool { return errors.As(err, out) }

type fakeTokens struct {
	err      error
	renewed  string
	rejected string
}

func (f *fakeTokens) AccessToken(context.Context, string, string, string) (string, error) {
	return "tok-1", f.err
}

func (f *fakeTokens) Renew(_ context.Context, _, _, _, rejected string) (string, error) {
	f.rejected = rejected
	return f.renewed, f.err
}

func TestStaticHeaderAuthOAuth(t *testing.T) {
	reg := mcp.Registration{Name: "slack", URL: "https://mcp.slack.com/mcp", Auth: mcp.AuthCredential, CredentialRef: "slack"}
	run := Run{TaskID: "t1", Reach: credentials.Reach{WorkspaceID: "ws1"}}
	authz := &fakeAuthorizer{d: credentials.Decision{Allowed: true, CredentialID: "c1"}}
	store := &fakeStore{cred: credentials.Credential{ID: "c1", Name: "slack", Provider: credentials.OAuthProviderID, Kind: credentials.OAuthKindMCP}}
	tokens := &fakeTokens{renewed: "tok-2"}
	a := &StaticHeaderAuth{Authorizer: authz, Store: store, Tokens: tokens}

	h, err := a.UpstreamHeaders(context.Background(), reg, run)
	if err != nil || h.Get("Authorization") != "Bearer tok-1" {
		t.Fatalf("headers = %v, %v", h, err)
	}
	h, err = a.RenewHeaders(context.Background(), reg, run, h)
	if err != nil || h.Get("Authorization") != "Bearer tok-2" || tokens.rejected != "tok-1" {
		t.Fatalf("renewed = %v, %v (rejected %q)", h, err, tokens.rejected)
	}

	var r *Refusal
	tokens.err = mcpoauth.ErrReconnect
	if _, err := a.UpstreamHeaders(context.Background(), reg, run); !asRefusal(err, &r) || r.Decision.Reason != credentials.ReasonInvalid {
		t.Errorf("reconnect required = %v", err)
	}
	tokens.err = mcpoauth.ErrWrongResource
	if _, err := a.UpstreamHeaders(context.Background(), reg, run); !asRefusal(err, &r) || r.Decision.Reason != credentials.ReasonHeaderMismatch {
		t.Errorf("wrong resource = %v", err)
	}

	static := &StaticHeaderAuth{Authorizer: authz, Store: &fakeStore{
		cred:    credentials.Credential{ID: "c2", Provider: credentials.HeaderProviderID, Kind: credentials.HeaderKindBearer},
		payload: map[string]string{"token": "x"},
	}}
	if _, err := static.RenewHeaders(context.Background(), reg, run, http.Header{}); !errors.Is(err, ErrNotRenewable) {
		t.Errorf("static renew = %v", err)
	}
}

func TestStreamableRenewsOnce(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer up.Close()
	p := New(up.Client())
	target := func(renew func(context.Context, http.Header) (http.Header, error)) Target {
		return Target{Name: "slack", URL: up.URL, Transport: mcp.TransportHTTP,
			Header: http.Header{"Authorization": {"Bearer old"}}, Renew: renew}
	}

	w := post(t, func(w http.ResponseWriter, r *http.Request) {
		p.Streamable(w, r, target(func(_ context.Context, rejected http.Header) (http.Header, error) {
			if rejected.Get("Authorization") != "Bearer old" {
				t.Errorf("rejected = %v", rejected)
			}
			return http.Header{"Authorization": {"Bearer new"}}, nil
		}))
	}, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if w.Code != http.StatusOK || strings.Join(seen, ",") != "Bearer old,Bearer new" {
		t.Fatalf("renewed call = %d, upstream saw %v", w.Code, seen)
	}

	seen = nil
	w = post(t, func(w http.ResponseWriter, r *http.Request) {
		p.Streamable(w, r, target(func(context.Context, http.Header) (http.Header, error) {
			return nil, &Refusal{Decision: credentials.Decision{Reason: credentials.ReasonInvalid}}
		}))
	}, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if w.Code != http.StatusForbidden || len(seen) != 1 {
		t.Fatalf("refused renewal = %d, upstream saw %v", w.Code, seen)
	}

	seen = nil
	w = post(t, func(w http.ResponseWriter, r *http.Request) {
		p.Streamable(w, r, target(func(context.Context, http.Header) (http.Header, error) { return nil, ErrNotRenewable }))
	}, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if w.Code != http.StatusBadGateway || len(seen) != 1 {
		t.Fatalf("not renewable = %d, upstream saw %v", w.Code, seen)
	}
}
