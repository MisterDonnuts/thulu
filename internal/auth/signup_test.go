package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func ssoRequest() *http.Request {
	return httptest.NewRequest(http.MethodGet, "http://octopus.example/auth/oidc/callback", nil)
}

func TestProvisionSSOUser_UnverifiedNewcomerRefused(t *testing.T) {
	t.Setenv("OCTOPUS_SIGNUP_MODE", "open")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	store := newSSOTestStore(t)
	h := NewHandler(store)

	_, _, _, err := h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "squatter@victim.io", Sub: "sub-1", EmailVerified: false})
	if !errors.Is(err, ErrSSOEmailUnverified) {
		t.Fatalf("unverified newcomer: err = %v, want ErrSSOEmailUnverified", err)
	}
	if u, _ := store.GetUserByEmail(t.Context(), "squatter@victim.io"); u != nil {
		t.Fatalf("unverified newcomer got a user row: %+v", u)
	}

	user, _, _, err := h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "ana@example.com", Sub: "sub-2", EmailVerified: true})
	if err != nil || user == nil {
		t.Fatalf("verified newcomer: user=%v err=%v", user, err)
	}
}

func TestRedirectToLoginUnverifiedMessage(t *testing.T) {
	t.Setenv("OCTOPUS_FRONTEND_ORIGIN", "")
	h := &Handler{}
	w := httptest.NewRecorder()
	h.redirectToLogin(w, ssoRequest(), ErrSSOEmailUnverified)
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("error"); got != ErrSSOEmailUnverified.Error() {
		t.Errorf("error = %q, want %q", got, ErrSSOEmailUnverified.Error())
	}
}

func TestSignupMode_Defaults(t *testing.T) {
	for _, c := range []struct{ saas, mode, want string }{
		{"", "", SignupOpen},
		{"1", "", SignupAllowlist},
		{"1", "open", SignupOpen},
		{"", "allowlist", SignupAllowlist},
		{"", "ALLOWLIST", SignupAllowlist},
		{"", "opne", SignupAllowlist},
	} {
		t.Setenv("OCTOPUS_SAAS_MODE", c.saas)
		t.Setenv("OCTOPUS_SIGNUP_MODE", c.mode)
		if got := SignupMode(); got != c.want {
			t.Errorf("SAAS=%q SIGNUP_MODE=%q: SignupMode() = %q, want %q", c.saas, c.mode, got, c.want)
		}
	}
}

type signupFixture struct {
	h       *Handler
	store   *UserStore
	opUser  *User
	opWS    string
	opSub   string
	opEmail string
}

func newSignupFixture(t *testing.T) signupFixture {
	t.Helper()
	t.Setenv("OCTOPUS_SIGNUP_MODE", "allowlist")
	t.Setenv("OCTOPUS_SSO_ALLOWED_EMAILS", "")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	store := newSSOTestStore(t)
	f := signupFixture{h: NewHandler(store), store: store, opSub: "op-sub", opEmail: "op@example.com"}
	f.opUser = ssoUser(t, store, f.opEmail, "Operator")
	if err := store.LinkOIDCIdentity(t.Context(), f.opUser.ID, "oidc", f.opSub, f.opEmail); err != nil {
		t.Fatal(err)
	}
	ws, err := store.CreateWorkspace(t.Context(), f.opUser.ID, "Ops", "ops")
	if err != nil {
		t.Fatal(err)
	}
	f.opWS = ws.ID
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", ws.ID)
	return f
}

func TestSignupAllowlist_Admission(t *testing.T) {
	f := newSignupFixture(t)
	ctx := t.Context()
	if _, err := f.store.AddSignupAllowlist(ctx, "Invited@Example.com", "beta", "op"); err != nil {
		t.Fatal(err)
	}

	login := func(email, sub string, verified bool) error {
		_, _, _, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
			ssoIdentity{Email: email, Sub: sub, EmailVerified: verified})
		return err
	}

	if err := login("stranger@example.com", "s-stranger", true); !errors.Is(err, ErrSSONotAllowed) {
		t.Errorf("stranger: err = %v, want ErrSSONotAllowed", err)
	}
	if u, _ := f.store.GetUserByEmail(ctx, "stranger@example.com"); u != nil {
		t.Error("refused stranger got a user row")
	}

	if err := login("invited@example.com", "s-invited", true); err != nil {
		t.Errorf("invited: err = %v, want nil", err)
	}

	if _, err := f.store.AddSignupAllowlist(ctx, "unverified@example.com", "", "op"); err != nil {
		t.Fatal(err)
	}
	if err := login("unverified@example.com", "s-unverified", false); !errors.Is(err, ErrSSONotAllowed) {
		t.Errorf("unverified listed email: err = %v, want ErrSSONotAllowed", err)
	}

	t.Setenv("OCTOPUS_SSO_ALLOWED_EMAILS", "seed@example.com, other@example.com")
	if err := login("seed@example.com", "s-seed", true); err != nil {
		t.Errorf("seeded: err = %v, want nil", err)
	}

	if err := login(f.opEmail, f.opSub, false); err != nil {
		t.Errorf("operator member: err = %v, want nil", err)
	}

	if err := login(f.opEmail, "impostor-sub", true); !errors.Is(err, ErrSSONotAllowed) {
		t.Errorf("operator email from another subject: err = %v, want ErrSSONotAllowed", err)
	}

	if _, err := f.store.RemoveSignupAllowlist(ctx, "invited@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := login("invited@example.com", "s-invited", true); !errors.Is(err, ErrSSONotAllowed) {
		t.Errorf("removed: err = %v, want ErrSSONotAllowed", err)
	}
}

func TestSignupAllowlist_OpenModeUnaffected(t *testing.T) {
	f := newSignupFixture(t)
	t.Setenv("OCTOPUS_SIGNUP_MODE", "open")
	if _, _, _, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "anyone@example.com", Sub: "s-any", EmailVerified: true}); err != nil {
		t.Errorf("open mode: err = %v, want nil", err)
	}
}

func signupDo(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSignupAllowlist_Routes(t *testing.T) {
	f := newSignupFixture(t)
	ctx := t.Context()
	mux := http.NewServeMux()
	f.h.RegisterRoutes(mux)
	wrapped := JWTMiddlewareWithStore(nil, mux)

	opTokens, err := f.h.issueTokenPair(ctx, f.opUser, f.opWS, RoleOwner, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}
	op := opTokens.AccessToken

	tenantID, tenant, _, err := f.h.ProvisionSessionForTest("tenant@example.com", "Tenant")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/auth/signup-allowlist"},
		{http.MethodPost, "/auth/signup-allowlist"},
		{http.MethodDelete, "/auth/signup-allowlist/x@example.com"},
	} {
		if w := signupDo(t, wrapped, c.method, c.path, tenant, map[string]string{"email": "x@example.com"}); w.Code != http.StatusForbidden {
			t.Errorf("tenant %s %s: status %d, want 403", c.method, c.path, w.Code)
		}
	}

	if w := signupDo(t, wrapped, http.MethodPost, "/auth/signup-allowlist", op,
		map[string]string{"email": "not an email"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad email: status %d, want 400", w.Code)
	}
	if w := signupDo(t, wrapped, http.MethodPost, "/auth/signup-allowlist", op,
		map[string]string{"email": " Tenant@Example.com ", "note": "beta"}); w.Code != http.StatusCreated {
		t.Fatalf("add: status %d body %s", w.Code, w.Body)
	}

	t.Setenv("OCTOPUS_SSO_ALLOWED_EMAILS", "seed@example.com")
	w := signupDo(t, wrapped, http.MethodGet, "/auth/signup-allowlist", op, nil)
	var list signupAllowlistResponse
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Mode != SignupAllowlist || len(list.Entries) != 2 {
		t.Fatalf("list = %+v, want allowlist mode with the seed and one row", list)
	}
	if e := list.Entries[0]; e.Email != "seed@example.com" || e.Source != allowlistSourceEnv {
		t.Errorf("first entry = %+v, want the env seed", e)
	}
	if e := list.Entries[1]; e.Email != "tenant@example.com" || e.Note != "beta" || e.Source != allowlistSourceList || e.AddedBy == "" {
		t.Errorf("second entry = %+v, want the added row", e)
	}

	if w := signupDo(t, wrapped, http.MethodDelete, "/auth/signup-allowlist/seed@example.com", op, nil); w.Code != http.StatusConflict {
		t.Errorf("delete seed: status %d, want 409", w.Code)
	}
	if w := signupDo(t, wrapped, http.MethodDelete, "/auth/signup-allowlist/nobody@example.com", op, nil); w.Code != http.StatusNotFound {
		t.Errorf("delete missing: status %d, want 404", w.Code)
	}

	before, _ := f.store.GetTokenVersion(ctx, tenantID)
	if w := signupDo(t, wrapped, http.MethodDelete, "/auth/signup-allowlist/tenant@example.com", op, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d body %s", w.Code, w.Body)
	}
	after, _ := f.store.GetTokenVersion(ctx, tenantID)
	if after == before {
		t.Error("removal did not bump the token version")
	}
	if fams, _ := f.store.ListSessionFamilies(ctx, tenantID); len(fams) != 0 {
		t.Errorf("removal left %d live session families", len(fams))
	}

	opVersion, _ := f.store.GetTokenVersion(ctx, f.opUser.ID)
	if _, err := f.store.AddSignupAllowlist(ctx, f.opEmail, "", "op"); err != nil {
		t.Fatal(err)
	}
	if w := signupDo(t, wrapped, http.MethodDelete, "/auth/signup-allowlist/"+f.opEmail, op, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete operator entry: status %d", w.Code)
	}
	if v, _ := f.store.GetTokenVersion(ctx, f.opUser.ID); v != opVersion {
		t.Error("removing the operator's own entry evicted the operator")
	}
}
