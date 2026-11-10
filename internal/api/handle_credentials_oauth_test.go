package api

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

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/mcpoauth"
)

type oauthWorld struct {
	as, mcp *httptest.Server

	mu          sync.Mutex
	valid       string
	refreshFail bool
	refreshes   atomic.Int32
	seen        []string
}

func newOAuthWorld(t *testing.T) *oauthWorld {
	o := &oauthWorld{valid: "access-1"}
	as := http.NewServeMux()
	as.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": o.as.URL, "authorization_endpoint": o.as.URL + "/authorize",
			"token_endpoint": o.as.URL + "/token", "registration_endpoint": o.as.URL + "/register",
			"code_challenge_methods_supported": []string{"S256"},
		})
	})
	as.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "octopus-client"})
	})
	as.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		back := q.Get("redirect_uri") + "?" + url.Values{"code": {"the-code"}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, back, http.StatusFound)
	})
	as.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			if r.PostForm.Get("code") != "the-code" || r.PostForm.Get("code_verifier") == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600})
		case "refresh_token":
			n := o.refreshes.Add(1)
			o.mu.Lock()
			fail := o.refreshFail
			o.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("access-r%d", n), "refresh_token": fmt.Sprintf("refresh-r%d", n), "expires_in": 3600,
			})
		}
	})
	o.as = httptest.NewServer(as)
	t.Cleanup(o.as.Close)

	m := http.NewServeMux()
	m.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": o.mcp.URL + "/mcp", "authorization_servers": []string{o.as.URL}})
	})
	m.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.seen = append(o.seen, r.Header.Get("Authorization"))
		ok := r.Header.Get("Authorization") == "Bearer "+o.valid
		o.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+o.mcp.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})
	o.mcp = httptest.NewServer(m)
	t.Cleanup(o.mcp.Close)
	return o
}

func (o *oauthWorld) lastSeen() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.seen[len(o.seen)-1]
}

func connectOAuth(t *testing.T, p *proxyRig, o *oauthWorld, withBinder bool) *url.URL {
	t.Helper()
	w := postJSONAuth(t, p.h, "/credentials/oauth/start", p.token, map[string]any{
		"server": "slack", "grants": []map[string]string{{"subject_type": "workspace"}},
	})
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", w.Code, w.Body.String())
	}
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
		Registration string `json:"registration"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &started)
	if started.Registration != mcpoauth.RegistrationDCR {
		t.Errorf("registration = %q", started.Registration)
	}
	var binder *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == mcpOAuthBinder {
			binder = c
		}
	}
	if binder == nil || !binder.HttpOnly || binder.SameSite != http.SameSiteLaxMode || binder.Path != mcpOAuthBinderPath {
		t.Fatalf("binder cookie = %+v", binder)
	}

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	if back.Path != mcpOAuthCallbackPath {
		t.Fatalf("authorization server sent the browser to %s", back)
	}
	r := httptest.NewRequest(http.MethodGet, back.RequestURI(), nil)
	if withBinder {
		r.AddCookie(&http.Cookie{Name: mcpOAuthBinder, Value: binder.Value})
	}
	cw := httptest.NewRecorder()
	p.h.ServeHTTP(cw, r)
	if cw.Code != http.StatusFound {
		t.Fatalf("callback = %d: %s", cw.Code, cw.Body.String())
	}
	loc, _ := url.Parse(cw.Header().Get("Location"))
	return loc
}

func newOAuthRig(t *testing.T, email string) (*proxyRig, *oauthWorld) {
	t.Helper()
	p := newProxyRig(t, email)
	svc, err := mcpoauth.New(p.srv.taskStore.DB(), p.srv.credStore)
	if err != nil {
		t.Fatal(err)
	}
	// The egress policy refuses loopback URLs learned in discovery; the fake AS is on loopback
	// (mcpoauth tests the policy).
	p.srv.SetMCPOAuth(svc.WithHTTPClient(http.DefaultClient))
	o := newOAuthWorld(t)
	p.register(t, mcp.Registration{Name: "slack", URL: o.mcp.URL + "/mcp", Transport: mcp.TransportHTTP,
		Auth: mcp.AuthCredential, CredentialRef: "slack"})
	if err := p.reg.RecordRunGrant(context.Background(), p.taskID, "developer", "in_progress",
		mcp.RunGrant{Tools: "slack"}); err != nil {
		t.Fatal(err)
	}
	return p, o
}

func TestIntegration_MCPOAuth_ConnectAndProxy(t *testing.T) {
	t.Parallel()
	p, o := newOAuthRig(t, "oauthflow@test.com")

	loc := connectOAuth(t, p, o, true)
	if loc.Path != "/credentials/" || loc.Query().Get("mcp_oauth") != "connected" {
		t.Fatalf("callback redirect = %s", loc)
	}
	credID := loc.Query().Get("credential")
	w := getJSON(t, p.h, "/credentials/"+credID, p.token)
	var cred credentials.Credential
	_ = json.Unmarshal(w.Body.Bytes(), &cred)
	if cred.Provider != credentials.OAuthProviderID || cred.State != credentials.StateValid || cred.Name != "slack" {
		t.Fatalf("credential = %+v", cred)
	}
	if strings.Contains(w.Body.String(), "access-1") || strings.Contains(w.Body.String(), "refresh-1") {
		t.Fatal("the credential view carries a token")
	}

	w = p.call(t, "slack", p.taskKey, initializeCall)
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusOK || o.lastSeen() != "Bearer access-1" {
		t.Fatalf("proxied call = %d (%s), upstream saw %q", w.Code, w.Body.String(), o.lastSeen())
	}

	o.mu.Lock()
	o.valid = "access-r1"
	o.mu.Unlock()
	w = p.call(t, "slack", p.taskKey, initializeCall)
	if w.Code != http.StatusOK || o.lastSeen() != "Bearer access-r1" || o.refreshes.Load() != 1 {
		t.Fatalf("after expiry = %d (%s), upstream saw %q, %d refreshes", w.Code, w.Body.String(), o.lastSeen(), o.refreshes.Load())
	}

	o.mu.Lock()
	o.valid, o.refreshFail = "access-r2", true
	o.mu.Unlock()
	w = p.call(t, "slack", p.taskKey, initializeCall)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), string(credentials.ReasonInvalid)) {
		t.Fatalf("refused refresh = %d: %s", w.Code, w.Body.String())
	}
	w = getJSON(t, p.h, "/credentials/"+credID, p.token)
	_ = json.Unmarshal(w.Body.Bytes(), &cred)
	if cred.State != credentials.StateInvalid || cred.IntrospectError != credentials.OAuthReconnectRequired {
		t.Fatalf("after a refused refresh: %+v", cred)
	}
	before := o.refreshes.Load()
	if w := p.call(t, "slack", p.taskKey, initializeCall); w.Code != http.StatusForbidden || o.refreshes.Load() != before {
		t.Fatalf("a dead connection = %d, refreshes %d -> %d", w.Code, before, o.refreshes.Load())
	}

	o.mu.Lock()
	o.valid, o.refreshFail = "access-1", false
	o.mu.Unlock()
	loc = connectOAuth(t, p, o, true)
	if loc.Query().Get("credential") != credID {
		t.Fatalf("reconnect landed on %s, want %s", loc.Query().Get("credential"), credID)
	}
	if w := p.call(t, "slack", p.taskKey, initializeCall); w.Code != http.StatusOK {
		t.Fatalf("after reconnect = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_MCPOAuth_CallbackNeedsTheBinder(t *testing.T) {
	t.Parallel()
	p, o := newOAuthRig(t, "oauthbinder@test.com")
	loc := connectOAuth(t, p, o, false)
	if loc.Query().Get("mcp_oauth") != "error" || loc.Query().Get("reason") != "state" {
		t.Fatalf("callback without binder = %s", loc)
	}
	w := getJSON(t, p.h, "/credentials", p.token)
	if strings.Contains(w.Body.String(), credentials.OAuthProviderID) {
		t.Fatalf("a credential was created: %s", w.Body.String())
	}
}

func TestIntegration_MCPOAuth_TokensStayInTheDaemon(t *testing.T) {
	t.Parallel()
	p, o := newOAuthRig(t, "oauthtokens@test.com")
	w := postJSONAuth(t, p.h, "/credentials", p.token, map[string]any{
		"name": "typed", "provider": credentials.OAuthProviderID, "kind": credentials.OAuthKindMCP,
		"payload": map[string]string{"access_token": "x"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("typed OAuth credential = %d: %s", w.Code, w.Body.String())
	}

	loc := connectOAuth(t, p, o, true)
	id := loc.Query().Get("credential")
	r := httptest.NewRequest(http.MethodPatch, "/credentials/"+id, strings.NewReader(`{"payload":{"access_token":"x"}}`))
	r.Header.Set("Authorization", "Bearer "+p.token)
	r.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	p.h.ServeHTTP(rw, r)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("rotating an OAuth connection = %d: %s", rw.Code, rw.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, "/internal/credentials/resolve?server=slack&tool=any&credential_id="+id, nil)
	r.Header.Set("X-API-Key", p.taskKey)
	rw = httptest.NewRecorder()
	p.h.ServeHTTP(rw, r)
	assertHandlerAnswered(t, rw)
	if strings.Contains(rw.Body.String(), "access-1") || rw.Code == http.StatusOK {
		t.Fatalf("resolve handed out the connection = %d: %s", rw.Code, rw.Body.String())
	}

	w = postJSONAuth(t, p.h, "/credentials/"+id+"/oauth/disconnect", p.token, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), credentials.OAuthNotConnected) {
		t.Fatalf("disconnect = %d: %s", w.Code, w.Body.String())
	}
	if w := p.call(t, "slack", p.taskKey, initializeCall); w.Code != http.StatusForbidden {
		t.Fatalf("call after disconnect = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_MCPOAuth_PublicRoutesAndServerList(t *testing.T) {
	t.Parallel()
	p, _ := newOAuthRig(t, "oauthpublic@test.com")

	r := httptest.NewRequest(http.MethodGet, mcpOAuthCallbackPath+"?state=x&code=y", nil)
	w := httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "reason=state") {
		t.Fatalf("anonymous callback = %d %s", w.Code, w.Header().Get("Location"))
	}

	w = getJSON(t, p.h, mcpOAuthClientPath, "")
	assertHandlerAnswered(t, w)
	if w.Code != http.StatusNotFound {
		t.Fatalf("client document over http = %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "https://thulu.example.com"+mcpOAuthClientPath, nil)
	w = httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	var doc map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &doc)
	if w.Code != http.StatusOK || doc["client_id"] != "https://thulu.example.com"+mcpOAuthClientPath {
		t.Fatalf("client document = %d %s", w.Code, w.Body.String())
	}

	w = getJSON(t, p.h, "/credentials/oauth/servers", p.token)
	assertHandlerAnswered(t, w)
	var list struct {
		Servers     []oauthServerView `json:"servers"`
		RedirectURI string            `json:"redirect_uri"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != http.StatusOK || len(list.Servers) != 1 || list.Servers[0].Name != "slack" ||
		!strings.HasSuffix(list.RedirectURI, mcpOAuthCallbackPath) {
		t.Fatalf("servers = %d %s", w.Code, w.Body.String())
	}
}
