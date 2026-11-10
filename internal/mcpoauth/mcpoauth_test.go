package mcpoauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

type fakeAS struct {
	srv *httptest.Server

	registration bool
	cimd         bool
	expiresIn    int
	refreshDelay time.Duration
	refreshFail  string

	mu         sync.Mutex
	challenges map[string]string
	codeReqs   map[string]url.Values
	registered []map[string]any
	lastToken  *http.Request
	lastForm   url.Values
	refreshes  atomic.Int32
	revoked    []string
	issued     int
}

func newFakeAS(t *testing.T) *fakeAS {
	as := &fakeAS{registration: true, expiresIn: 3600, challenges: map[string]string{}, codeReqs: map[string]url.Values{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		m := map[string]any{
			"issuer":                           as.srv.URL,
			"authorization_endpoint":           as.srv.URL + "/authorize",
			"token_endpoint":                   as.srv.URL + "/token",
			"revocation_endpoint":              as.srv.URL + "/revoke",
			"code_challenge_methods_supported": []string{"S256"},
		}
		if as.registration {
			m["registration_endpoint"] = as.srv.URL + "/register"
		}
		if as.cimd {
			m["client_id_metadata_document_supported"] = true
		}
		_ = json.NewEncoder(w).Encode(m)
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		as.mu.Lock()
		as.registered = append(as.registered, body)
		as.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dyn-client", "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("POST /token", as.token)
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		as.mu.Lock()
		as.revoked = append(as.revoked, r.PostForm.Get("token"))
		as.mu.Unlock()
	})
	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)
	return as
}

func (as *fakeAS) authorize(t *testing.T, raw string) (code, state string, q url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(raw, as.srv.URL+"/authorize?") {
		t.Fatalf("authorize URL %q", raw)
	}
	q = u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("no PKCE in %q", raw)
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	as.issued++
	code = fmt.Sprintf("code-%d", as.issued)
	as.challenges[code] = q.Get("code_challenge")
	as.codeReqs[code] = q
	return code, q.Get("state"), q
}

func (as *fakeAS) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f := r.PostForm
	as.mu.Lock()
	as.lastToken, as.lastForm = r, f
	as.mu.Unlock()
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "nope"})
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		as.mu.Lock()
		challenge, req := as.challenges[f.Get("code")], as.codeReqs[f.Get("code")]
		delete(as.challenges, f.Get("code"))
		as.mu.Unlock()
		if challenge == "" || challengeS256(f.Get("code_verifier")) != challenge {
			fail(http.StatusBadRequest, "invalid_grant")
			return
		}
		if f.Get("redirect_uri") != req.Get("redirect_uri") || f.Get("resource") != req.Get("resource") {
			fail(http.StatusBadRequest, "invalid_request")
			return
		}
	case "refresh_token":
		n := as.refreshes.Add(1)
		time.Sleep(as.refreshDelay)
		switch as.refreshFail {
		case "":
		case "503":
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		default:
			fail(http.StatusBadRequest, as.refreshFail)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("access-r%d", n), "refresh_token": fmt.Sprintf("refresh-r%d", n),
			"token_type": "Bearer", "expires_in": as.expiresIn,
		})
		return
	default:
		fail(http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-1", "refresh_token": "refresh-1", "token_type": "Bearer",
		"expires_in": as.expiresIn, "scope": "read write",
	})
}

type fakeMCP struct {
	srv          *httptest.Server
	as           *fakeAS
	hintMetadata bool
	resource     string
}

func newFakeMCP(t *testing.T, as *fakeAS) *fakeMCP {
	m := &fakeMCP{as: as, hintMetadata: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		ch := `Bearer scope="read write"`
		if m.hintMetadata {
			ch = `Bearer resource_metadata="` + m.srv.URL + `/.well-known/oauth-protected-resource/mcp", scope="read write"`
		}
		w.Header().Set("WWW-Authenticate", ch)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		res := m.srv.URL + "/mcp"
		if m.resource != "" {
			res = m.resource
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": res, "authorization_servers": []string{as.srv.URL}})
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeMCP) url() string { return m.srv.URL + "/mcp" }

type harness struct {
	svc   *Service
	creds *credentials.Store
	as    *fakeAS
	mcp   *fakeMCP
	now   time.Time
}

const testWS = "ws-1"

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testpg.New(t)
	creds, err := credentials.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(db, creds)
	if err != nil {
		t.Fatal(err)
	}
	as := newFakeAS(t)
	h := &harness{svc: svc, creds: creds, as: as, mcp: newFakeMCP(t, as), now: time.Now()}
	svc.WithHTTPClient(http.DefaultClient).WithClock(func() time.Time { return h.now })
	return h
}

func (h *harness) start(t *testing.T, mod func(*StartRequest)) Started {
	t.Helper()
	req := StartRequest{
		WorkspaceID: testWS, Author: "ana@example.com", Name: "slack", Server: "slack",
		ServerURL: h.mcp.url(), Transport: "http", Return: "credentials",
		Client: ClientRequest{RedirectURI: "https://thulu.example.com/credentials/oauth/callback"},
	}
	if mod != nil {
		mod(&req)
	}
	st, err := h.svc.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return st
}

func (h *harness) connect(t *testing.T, mod func(*StartRequest)) string {
	t.Helper()
	st := h.start(t, mod)
	code, state, _ := h.as.authorize(t, st.AuthorizeURL)
	fin, err := h.svc.Finish(context.Background(), state, st.Binder, code, "", "")
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return fin.CredentialID
}

func (h *harness) conn(t *testing.T, id string) (credentials.OAuthConnection, *credentials.Credential) {
	t.Helper()
	p, err := h.creds.Payload(context.Background(), testWS, id)
	if err != nil {
		t.Fatal(err)
	}
	c, err := h.creds.Get(context.Background(), testWS, id)
	if err != nil {
		t.Fatal(err)
	}
	return credentials.ParseOAuthConnection(p), c
}

func TestDiscoverFollowsResourceMetadata(t *testing.T) {
	h := newHarness(t)
	d, err := h.svc.Discover(context.Background(), h.mcp.url(), "http")
	if err != nil {
		t.Fatal(err)
	}
	if d.Resource != h.mcp.url() || d.Server.Issuer != h.as.srv.URL || d.Scope != "read write" {
		t.Fatalf("discovery = %+v", d)
	}
	if !strings.HasSuffix(d.ResourceMetadata, "/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("resource metadata from %q", d.ResourceMetadata)
	}
}

func TestDiscoverFallsBackToWellKnown(t *testing.T) {
	h := newHarness(t)
	h.mcp.hintMetadata = false
	d, err := h.svc.Discover(context.Background(), h.mcp.url(), "http")
	if err != nil {
		t.Fatal(err)
	}
	if d.Server.TokenEndpoint != h.as.srv.URL+"/token" {
		t.Fatalf("discovery = %+v", d)
	}
}

func TestDiscoverRefusesMetadataForAnotherResource(t *testing.T) {
	h := newHarness(t)
	h.mcp.resource = "https://elsewhere.example.com/mcp"
	if _, err := h.svc.Discover(context.Background(), h.mcp.url(), "http"); err == nil {
		t.Fatal("metadata for another origin was accepted")
	}
}

func TestDiscoverRefusesClearTextAuthorizationServer(t *testing.T) {
	h := newHarness(t)
	h.svc.endpointOK = func(string) bool { return false }
	if _, err := h.svc.Discover(context.Background(), h.mcp.url(), "http"); err == nil {
		t.Fatal("an authorization server failing the endpoint rule was used")
	}
}

func TestConnectWithDynamicRegistration(t *testing.T) {
	h := newHarness(t)
	st := h.start(t, func(r *StartRequest) {
		r.Grants = []credentials.Grant{{SubjectType: credentials.SubjectWorkspace}}
	})
	if st.Registration != RegistrationDCR {
		t.Fatalf("registration = %q", st.Registration)
	}
	code, state, q := h.as.authorize(t, st.AuthorizeURL)
	if q.Get("client_id") != "dyn-client" || q.Get("resource") != h.mcp.url() || q.Get("scope") != "read write" {
		t.Fatalf("authorize query = %v", q)
	}
	if got := h.as.registered[0]["redirect_uris"].([]any)[0]; got != "https://thulu.example.com/credentials/oauth/callback" {
		t.Fatalf("registered redirect = %v", got)
	}
	fin, err := h.svc.Finish(context.Background(), state, st.Binder, code, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !fin.Created || fin.Return != "credentials" || fin.Name != "slack" {
		t.Fatalf("finished = %+v", fin)
	}
	conn, cred := h.conn(t, fin.CredentialID)
	if conn.AccessToken != "access-1" || conn.RefreshToken != "refresh-1" || conn.Resource != h.mcp.url() {
		t.Fatalf("connection = %+v", conn)
	}
	if cred.Provider != credentials.OAuthProviderID || cred.State != credentials.StateValid || len(cred.Grants) != 1 {
		t.Fatalf("credential = %+v", cred)
	}
	if h.as.lastForm.Get("client_id") != "dyn-client" || h.as.lastForm.Get("resource") != h.mcp.url() {
		t.Fatalf("token request = %v", h.as.lastForm)
	}
}

func TestConnectNeedsAClientWithoutRegistration(t *testing.T) {
	h := newHarness(t)
	h.as.registration = false
	_, err := h.svc.Start(context.Background(), StartRequest{
		WorkspaceID: testWS, Name: "slack", ServerURL: h.mcp.url(), Transport: "http",
		Client: ClientRequest{RedirectURI: "https://thulu.example.com/cb"},
	})
	if err == nil || !strings.Contains(err.Error(), "client ID") {
		t.Fatalf("Start without a client = %v", err)
	}
}

func TestConnectWithManualClient(t *testing.T) {
	h := newHarness(t)
	h.as.registration = false
	id := h.connect(t, func(r *StartRequest) {
		r.Client.ID, r.Client.Secret = "slack-app", "slack-secret"
	})
	user, pass, ok := h.as.lastToken.BasicAuth()
	if !ok || user != "slack-app" || pass != "slack-secret" || h.as.lastForm.Get("client_secret") != "" {
		t.Fatalf("token request auth = %v %q %q, form %v", ok, user, pass, h.as.lastForm)
	}
	conn, _ := h.conn(t, id)
	if conn.Registration != RegistrationManual || conn.ClientSecret != "slack-secret" {
		t.Fatalf("connection = %+v", conn)
	}

	st := h.start(t, func(r *StartRequest) { r.CredentialID = id })
	if q, _ := url.Parse(st.AuthorizeURL); q.Query().Get("client_id") != "slack-app" {
		t.Fatalf("reconnect client = %q", q.Query().Get("client_id"))
	}
}

func TestConnectWithClientMetadataDocument(t *testing.T) {
	h := newHarness(t)
	h.as.registration, h.as.cimd = false, true
	doc := "https://thulu.example.com/credentials/oauth/client"
	st := h.start(t, func(r *StartRequest) { r.Client.MetadataURL = doc })
	if st.Registration != RegistrationCIMD {
		t.Fatalf("registration = %q", st.Registration)
	}
	_, _, q := h.as.authorize(t, st.AuthorizeURL)
	if q.Get("client_id") != doc {
		t.Fatalf("client_id = %q", q.Get("client_id"))
	}
}

func TestFinishChecksStateAndBinder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	st := h.start(t, nil)
	code, state, _ := h.as.authorize(t, st.AuthorizeURL)

	if _, err := h.svc.Finish(ctx, state, "another-browser", code, "", ""); !errors.Is(err, ErrState) {
		t.Fatalf("wrong binder = %v", err)
	}
	if _, err := h.svc.Finish(ctx, "forged", st.Binder, code, "", ""); !errors.Is(err, ErrState) {
		t.Fatalf("wrong state = %v", err)
	}
	if _, err := h.svc.Finish(ctx, state, st.Binder, code, "", ""); err != nil {
		t.Fatalf("real browser = %v", err)
	}
	if _, err := h.svc.Finish(ctx, state, st.Binder, code, "", ""); !errors.Is(err, ErrState) {
		t.Fatalf("replay = %v", err)
	}
}

func TestFinishRefusesAnExpiredFlow(t *testing.T) {
	h := newHarness(t)
	st := h.start(t, nil)
	code, state, _ := h.as.authorize(t, st.AuthorizeURL)
	h.now = h.now.Add(FlowTTL + time.Second)
	if _, err := h.svc.Finish(context.Background(), state, st.Binder, code, "", ""); !errors.Is(err, ErrState) {
		t.Fatalf("expired flow = %v", err)
	}
}

func TestFinishReportsADeniedConsent(t *testing.T) {
	h := newHarness(t)
	st := h.start(t, nil)
	_, state, _ := h.as.authorize(t, st.AuthorizeURL)
	fin, err := h.svc.Finish(context.Background(), state, st.Binder, "", "access_denied", "user said no")
	if err == nil || fin.Return != "credentials" || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("denied = %+v, %v", fin, err)
	}
}

func TestFinishRefusesAWrongVerifier(t *testing.T) {
	h := newHarness(t)
	st := h.start(t, nil)
	code, state, _ := h.as.authorize(t, st.AuthorizeURL)
	h.as.challenges[code] = challengeS256("someone-elses-verifier")
	if _, err := h.svc.Finish(context.Background(), state, st.Binder, code, "", ""); err == nil {
		t.Fatal("a code redeemed with another verifier was accepted")
	}
}

func TestAccessTokenRefreshesBeforeExpiry(t *testing.T) {
	h := newHarness(t)
	h.as.expiresIn = 600
	id := h.connect(t, nil)
	ctx := context.Background()

	tok, err := h.svc.AccessToken(ctx, testWS, id, h.mcp.url())
	if err != nil || tok != "access-1" || h.as.refreshes.Load() != 0 {
		t.Fatalf("fresh token = %q, %v (refreshes %d)", tok, err, h.as.refreshes.Load())
	}
	h.now = h.now.Add(9 * time.Minute)
	tok, err = h.svc.AccessToken(ctx, testWS, id, h.mcp.url())
	if err != nil || tok != "access-r1" {
		t.Fatalf("refreshed token = %q, %v", tok, err)
	}
	if h.as.lastForm.Get("refresh_token") != "refresh-1" || h.as.lastForm.Get("resource") != h.mcp.url() {
		t.Fatalf("refresh request = %v", h.as.lastForm)
	}
	conn, _ := h.conn(t, id)
	if conn.RefreshToken != "refresh-r1" {
		t.Fatalf("rotated refresh token not stored: %+v", conn)
	}
}

func TestRenewIsOneRefreshPerCredential(t *testing.T) {
	h := newHarness(t)
	id := h.connect(t, nil)
	h.as.refreshDelay = 150 * time.Millisecond

	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := h.svc.Renew(context.Background(), testWS, id, h.mcp.url(), "access-1")
			if err != nil {
				t.Error(err)
			}
			got[i] = tok
		}(i)
	}
	wg.Wait()
	if n := h.as.refreshes.Load(); n != 1 {
		t.Fatalf("%d refreshes for one rejected token", n)
	}
	for _, tok := range got {
		if tok != "access-r1" {
			t.Fatalf("tokens = %v", got)
		}
	}
	if tok, err := h.svc.Renew(context.Background(), testWS, id, h.mcp.url(), "access-1"); err != nil || tok != "access-r1" || h.as.refreshes.Load() != 1 {
		t.Fatalf("late renew = %q, %v (refreshes %d)", tok, err, h.as.refreshes.Load())
	}
}

func TestRefusedRefreshMarksTheConnection(t *testing.T) {
	h := newHarness(t)
	id := h.connect(t, nil)
	h.as.refreshFail = "invalid_grant"
	ctx := context.Background()

	if _, err := h.svc.Renew(ctx, testWS, id, h.mcp.url(), "access-1"); !errors.Is(err, ErrReconnect) {
		t.Fatalf("refused refresh = %v", err)
	}
	conn, cred := h.conn(t, id)
	if cred.State != credentials.StateInvalid || cred.IntrospectError != credentials.OAuthReconnectRequired ||
		conn.Status != credentials.OAuthFailed || conn.AccessToken != "" || conn.RefreshToken != "" {
		t.Fatalf("after a refused refresh: %+v / %+v", cred, conn)
	}
	before := h.as.refreshes.Load()
	if _, err := h.svc.AccessToken(ctx, testWS, id, h.mcp.url()); !errors.Is(err, ErrReconnect) {
		t.Fatalf("token of a failed connection = %v", err)
	}
	if h.as.refreshes.Load() != before {
		t.Fatal("a failed connection kept calling the token endpoint")
	}
	payload, _ := h.creds.Payload(ctx, testWS, id)
	in, _ := credentials.NewRegistry().Introspect(ctx, credentials.OAuthProviderID, credentials.OAuthKindMCP, payload, nil)
	if in.State != credentials.StateInvalid {
		t.Fatalf("introspection = %+v", in)
	}
}

func TestUnreachableRefreshKeepsTheConnection(t *testing.T) {
	h := newHarness(t)
	id := h.connect(t, nil)
	h.as.refreshFail = "503"
	_, err := h.svc.Renew(context.Background(), testWS, id, h.mcp.url(), "access-1")
	if err == nil || errors.Is(err, ErrReconnect) {
		t.Fatalf("transient failure = %v", err)
	}
	conn, cred := h.conn(t, id)
	if cred.State != credentials.StateValid || conn.RefreshToken != "refresh-1" {
		t.Fatalf("a transient failure changed the connection: %+v / %+v", cred, conn)
	}
}

func TestTokensOnlyGoToTheirResource(t *testing.T) {
	h := newHarness(t)
	id := h.connect(t, nil)
	if _, err := h.svc.AccessToken(context.Background(), testWS, id, "https://other.example.com/mcp"); !errors.Is(err, ErrWrongResource) {
		t.Fatalf("token for another server = %v", err)
	}
}

func TestDisconnectRevokesAndForgets(t *testing.T) {
	h := newHarness(t)
	id := h.connect(t, nil)
	ctx := context.Background()
	if err := h.svc.Disconnect(ctx, testWS, id); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.as.revoked, ",") != "refresh-1,access-1" {
		t.Fatalf("revoked = %v", h.as.revoked)
	}
	conn, cred := h.conn(t, id)
	if cred.IntrospectError != credentials.OAuthNotConnected || conn.AccessToken != "" || conn.ClientID != "dyn-client" {
		t.Fatalf("after disconnect: %+v / %+v", cred, conn)
	}
	if _, err := h.svc.AccessToken(ctx, testWS, id, h.mcp.url()); !errors.Is(err, ErrReconnect) {
		t.Fatalf("token after disconnect = %v", err)
	}
	if again := h.connect(t, nil); again != id {
		t.Fatalf("reconnect made %s, want %s", again, id)
	}
}

func TestStartRefusesANameTakenByAnotherKind(t *testing.T) {
	h := newHarness(t)
	_, err := h.creds.Create(context.Background(), testWS, credentials.Input{
		Name: "slack", Provider: credentials.HeaderProviderID, Kind: credentials.HeaderKindBearer,
		Payload: map[string]string{"token": "x"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Start(context.Background(), StartRequest{
		WorkspaceID: testWS, Name: "slack", ServerURL: h.mcp.url(), Transport: "http",
		Client: ClientRequest{RedirectURI: "https://thulu.example.com/cb"},
	})
	if !errors.Is(err, credentials.ErrExists) {
		t.Fatalf("name of a header credential = %v", err)
	}
}

func TestBearerParams(t *testing.T) {
	got := bearerParams(`Bearer error="invalid_token", resource_metadata="https://x/.well-known/oauth-protected-resource", scope="a b"`)
	if got["resource_metadata"] != "https://x/.well-known/oauth-protected-resource" || got["scope"] != "a b" || got["error"] != "invalid_token" {
		t.Fatalf("params = %v", got)
	}
	if got := bearerParams(`Basic realm="x"`); len(got) != 0 {
		t.Fatalf("basic challenge read as bearer: %v", got)
	}
}

func TestCovers(t *testing.T) {
	for _, c := range []struct {
		resource, server string
		want             bool
	}{
		{"https://mcp.example.com", "https://mcp.example.com/mcp", true},
		{"https://mcp.example.com/mcp", "https://MCP.example.com/mcp/", true},
		{"https://mcp.example.com/mcp", "https://mcp.example.com/mcpx", false},
		{"https://mcp.example.com/mcp", "http://mcp.example.com/mcp", false},
		{"https://mcp.example.com/mcp", "https://evil.example.com/mcp", false},
	} {
		if got := Covers(c.resource, c.server); got != c.want {
			t.Errorf("Covers(%q, %q) = %v", c.resource, c.server, got)
		}
	}
}

type selfHosted struct {
	srv      *httptest.Server
	hint     string
	issuer   string
	redirect string
}

func newSelfHosted(t *testing.T) *selfHosted {
	sh := &selfHosted{}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		ch := `Bearer scope="read"`
		if sh.hint != "" {
			ch = `Bearer resource_metadata="` + sh.hint + `"`
		}
		w.Header().Set("WWW-Authenticate", ch)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		if sh.redirect != "" {
			http.Redirect(w, r, sh.redirect, http.StatusFound)
			return
		}
		issuer := sh.issuer
		if issuer == "" {
			issuer = sh.srv.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": sh.srv.URL + "/mcp", "authorization_servers": []string{issuer}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": sh.srv.URL, "authorization_endpoint": sh.srv.URL + "/authorize",
			"token_endpoint": sh.srv.URL + "/token", "code_challenge_methods_supported": []string{"S256"},
		})
	})
	sh.srv = httptest.NewServer(mux)
	t.Cleanup(sh.srv.Close)
	return sh
}

func TestDiscoverKeepsLearnedURLsOffPrivateNetworks(t *testing.T) {
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")
	h := newHarness(t)
	svc, err := New(h.svc.db, h.creds)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"authorization_servers": []string{"https://as.example.com"}})
	}))
	t.Cleanup(internal.Close)

	sh := newSelfHosted(t)
	d, err := svc.Discover(context.Background(), sh.srv.URL+"/mcp", "http")
	if err != nil || d.Server.TokenEndpoint != sh.srv.URL+"/token" {
		t.Fatalf("a server that is its own authorization server: %+v, %v", d, err)
	}

	for name, set := range map[string]func(){
		"a resource_metadata hint":           func() { sh.hint = internal.URL + "/admin" },
		"an authorization server":            func() { sh.issuer = internal.URL },
		"a redirect off the server's origin": func() { sh.redirect = internal.URL + "/admin" },
	} {
		sh.hint, sh.issuer, sh.redirect = "", "", ""
		set()
		if _, err := svc.Discover(context.Background(), sh.srv.URL+"/mcp", "http"); err == nil {
			t.Errorf("%s on a private address was accepted", name)
		}
		if n := hits.Load(); n != 0 {
			t.Fatalf("%s on a private address was fetched (%d requests)", name, n)
		}
	}
}
