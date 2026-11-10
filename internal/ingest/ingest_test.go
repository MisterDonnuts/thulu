package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func mint(t *testing.T, s *Store, envID string, cidrs []string) string {
	t.Helper()
	raw, _, err := s.CreateToken(context.Background(), envID, "test", "tester", cidrs)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return raw
}

func TestCreateToken_SecretIsNotRecoverable(t *testing.T) {
	s := newStore(t)
	raw, tok, err := s.CreateToken(context.Background(), "env-1", "prod collector", "admin", nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) {
		t.Errorf("raw token %q lacks the %q prefix", raw, TokenPrefix)
	}
	if tok.Prefix == raw {
		t.Fatal("stored prefix is the whole secret")
	}
	if !strings.HasPrefix(raw, tok.Prefix) {
		t.Errorf("prefix %q is not a prefix of the secret", tok.Prefix)
	}

	listed, err := s.ListTokens(context.Background(), "env-1")
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d tokens, want 1", len(listed))
	}
	if strings.Contains(listed[0].Prefix, strings.TrimPrefix(raw, TokenPrefix)[8:]) {
		t.Error("listing leaks the secret body")
	}
}

func TestAuthenticate_EnvironmentComesFromTheTokenOnly(t *testing.T) {
	s := newStore(t)
	prodRaw := mint(t, s, "env-prod", nil)
	stagingRaw := mint(t, s, "env-staging", nil)

	if got := s.Authenticate(context.Background(), prodRaw, "1.2.3.4"); !got.OK || got.EnvironmentID != "env-prod" {
		t.Fatalf("prod token resolved to %+v", got)
	}
	if got := s.Authenticate(context.Background(), stagingRaw, "1.2.3.4"); got.EnvironmentID != "env-staging" {
		t.Fatalf("staging token resolved to %q, want env-staging", got.EnvironmentID)
	}
}

func TestAuthenticate_RevokedTokenStopsWorking(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-1", nil)
	toks, _ := s.ListTokens(context.Background(), "env-1")

	if err := s.RevokeToken(context.Background(), "env-1", toks[0].ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	got := s.Authenticate(context.Background(), raw, "1.2.3.4")
	if got.OK {
		t.Fatal("revoked token still authenticates")
	}
	if got.Reason != RejectRevoked {
		t.Errorf("reason = %q, want %q", got.Reason, RejectRevoked)
	}
	other := mint(t, s, "env-1", nil)
	if !s.Authenticate(context.Background(), other, "1.2.3.4").OK {
		t.Error("revoking one token disabled a sibling")
	}
}

func TestIPAllowed(t *testing.T) {
	cases := []struct {
		name  string
		ip    string
		cidrs []string
		want  bool
	}{
		{"empty list allows anything", "8.8.8.8", nil, true},
		{"inside range", "10.0.4.7", []string{"10.0.0.0/16"}, true},
		{"outside range", "10.1.4.7", []string{"10.0.0.0/16"}, false},
		{"bare address matches", "34.201.9.1", []string{"34.201.9.1"}, true},
		{"one of several", "34.201.9.1", []string{"10.0.0.0/8", "34.201.9.1"}, true},
		{"ipv6 in range", "2001:db8::5", []string{"2001:db8::/32"}, true},
		{"unparseable address with a list configured", "not-an-ip", []string{"10.0.0.0/8"}, false},
		{"unparseable address with no list", "not-an-ip", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IPAllowed(c.ip, c.cidrs); got != c.want {
				t.Errorf("IPAllowed(%q, %v) = %v, want %v", c.ip, c.cidrs, got, c.want)
			}
		})
	}
}

func TestValidateCIDRs_RejectsTypos(t *testing.T) {
	if err := ValidateCIDRs([]string{"10.0.0.0/16", "10.0.0.0/33"}); err == nil {
		t.Error("accepted an impossible prefix length")
	}
	if err := ValidateCIDRs([]string{"10.0.0.0/16", "192.168.1.1", ""}); err != nil {
		t.Errorf("rejected a valid list: %v", err)
	}
}

func TestAuthenticate_IPAllowlist(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-1", []string{"34.201.0.0/16"})

	if got := s.Authenticate(context.Background(), raw, "34.201.9.1"); !got.OK {
		t.Fatalf("allowed address rejected: %+v", got)
	}
	got := s.Authenticate(context.Background(), raw, "8.8.8.8")
	if got.OK {
		t.Fatal("address outside the allowlist was accepted")
	}
	if got.Reason != RejectIPBlocked {
		t.Errorf("reason = %q, want %q", got.Reason, RejectIPBlocked)
	}
}

func TestSourceAudit_RecordsAcceptedAndRejected(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-1", []string{"34.201.0.0/16"})
	ctx := context.Background()

	s.Authenticate(ctx, raw, "34.201.9.1")
	s.Authenticate(ctx, raw, "34.201.9.1")
	s.Authenticate(ctx, raw, "8.8.8.8")

	sources, err := s.ListSources(ctx, "env-1")
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	byIP := map[string]Source{}
	for _, src := range sources {
		byIP[src.IP] = src
	}
	if got := byIP["34.201.9.1"].Accepted; got != 2 {
		t.Errorf("accepted count for the allowed address = %d, want 2", got)
	}
	blocked, ok := byIP["8.8.8.8"]
	if !ok {
		t.Fatal("rejected address is missing from the audit trail")
	}
	if blocked.Rejected != 1 {
		t.Errorf("rejected count = %d, want 1", blocked.Rejected)
	}
	if blocked.LastReject != string(RejectIPBlocked) {
		t.Errorf("last_reject = %q, want %q", blocked.LastReject, RejectIPBlocked)
	}
	if blocked.Accepted != 0 {
		t.Errorf("blocked address recorded %d accepted", blocked.Accepted)
	}
}

func TestSourceAudit_UnknownTokenIsNotAttributedToAnyone(t *testing.T) {
	s := newStore(t)
	mint(t, s, "env-1", nil)

	got := s.Authenticate(context.Background(), TokenPrefix+"deadbeef", "8.8.8.8")
	if got.OK || got.Reason != RejectBadToken {
		t.Fatalf("forged token: %+v", got)
	}
	sources, err := s.ListSources(context.Background(), "env-1")
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("forged token produced %d audit rows, want 0", len(sources))
	}
}

type capturingDecoder struct {
	envID, kind string
	body        []byte
	calls       int
}

func (d *capturingDecoder) Decode(envID, kind, _ string, body []byte) error {
	d.envID, d.kind, d.body = envID, kind, body
	d.calls++
	return nil
}

func post(t *testing.T, srv *httptest.Server, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("X-Octopus-Token", token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func newServer(t *testing.T, s *Store, d Decoder) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(s, d).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHandler_AcceptsAndRoutesToTheRightEnvironment(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-prod", nil)
	dec := &capturingDecoder{}
	srv := newServer(t, s, dec)

	resp := post(t, srv, "/ingest/v1/traces", raw, "payload")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if dec.envID != "env-prod" {
		t.Errorf("decoder got env %q, want env-prod", dec.envID)
	}
	if dec.kind != "traces" {
		t.Errorf("decoder got kind %q, want traces", dec.kind)
	}
	if string(dec.body) != "payload" {
		t.Errorf("decoder got body %q", dec.body)
	}
}

func TestHandler_AllThreeSignalPathsExist(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-1", nil)
	dec := &capturingDecoder{}
	srv := newServer(t, s, dec)

	for _, kind := range []string{"traces", "metrics", "logs"} {
		resp := post(t, srv, "/ingest/v1/"+kind, raw, "x")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("/ingest/v1/%s: status = %d, want 200", kind, resp.StatusCode)
		}
	}
}

func TestHandler_RejectionsAreIndistinguishable(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-1", []string{"10.0.0.0/8"})
	srv := newServer(t, s, &capturingDecoder{})

	valid := post(t, srv, "/ingest/v1/traces", raw, "x")
	forged := post(t, srv, "/ingest/v1/traces", TokenPrefix+"0000", "x")
	absent := post(t, srv, "/ingest/v1/traces", "", "x")

	for name, resp := range map[string]*http.Response{
		"valid token, wrong network": valid,
		"forged token":               forged,
		"no token":                   absent,
	} {
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", name, resp.StatusCode)
		}
		if resp.ContentLength > 0 {
			t.Errorf("%s: response has a body, which distinguishes the cases", name)
		}
	}
	if valid.StatusCode != forged.StatusCode || forged.StatusCode != absent.StatusCode {
		t.Error("rejection responses differ between causes")
	}
}

func TestHandler_DecoderNotCalledForRejectedRequests(t *testing.T) {
	s := newStore(t)
	dec := &capturingDecoder{}
	srv := newServer(t, s, dec)

	post(t, srv, "/ingest/v1/traces", TokenPrefix+"nope", "x")
	if dec.calls != 0 {
		t.Errorf("decoder ran %d times for an unauthenticated request", dec.calls)
	}
}

func TestHandler_BodyCapIsEnforced(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-1", nil)
	dec := &capturingDecoder{}
	srv := newServer(t, s, dec)

	resp := post(t, srv, "/ingest/v1/metrics", raw, strings.Repeat("a", maxBodyBytes+1))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if dec.calls != 0 {
		t.Error("decoder was handed an oversized payload")
	}
}

func TestHandler_RateLimitAnswers429NotASilentDrop(t *testing.T) {
	s := newStore(t)
	raw := mint(t, s, "env-1", nil)
	srv := newServer(t, s, &capturingDecoder{})

	var limited *http.Response
	for i := 0; i < requestsPerMinute+5; i++ {
		resp := post(t, srv, "/ingest/v1/traces", raw, "x")
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = resp
			break
		}
	}
	if limited == nil {
		t.Fatal("never rate limited")
	}
	if limited.Header.Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
}

func TestBearerToken_AcceptsBothHeaderForms(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("Authorization", "Bearer octel_abc")
	if got := bearerToken(r); got != "octel_abc" {
		t.Errorf("Authorization bearer: got %q", got)
	}
	r.Header.Set("X-Octopus-Token", "octel_xyz")
	if got := bearerToken(r); got != "octel_xyz" {
		t.Errorf("dedicated header should win: got %q", got)
	}
}
