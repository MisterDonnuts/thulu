package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func TestSlugify_FoldsDiacritics(t *testing.T) {
	for in, want := range map[string]string{
		"Workspace de Lucía": "workspace-de-lucia",
		"Ñandú Çelik":        "nandu-celik",
		"Ærø":                "ærø",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPersonalWorkspaceName_FollowsLocale(t *testing.T) {
	es := i18n.WithLocale(context.Background(), "es")
	if got := PersonalWorkspaceName(es, "Lucía Ejemplo"); got != "Workspace de Lucía" {
		t.Errorf("es: %q", got)
	}
	en := i18n.WithLocale(context.Background(), "en")
	if got := PersonalWorkspaceName(en, "Lucía Ejemplo"); got != "Lucía's workspace" {
		t.Errorf("en: %q", got)
	}
}

func TestProvisionSSOWorkspace_NameInRequestLanguage(t *testing.T) {
	t.Setenv("OCTOPUS_SIGNUP_MODE", "open")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	store := newSSOTestStore(t)
	h := NewHandler(store)
	r := ssoRequest()
	r.Header.Set("Accept-Language", "es-AR,es;q=0.9")
	_, wsID, _, err := h.provisionSSOUser(r, "oidc",
		ssoIdentity{Email: "lucia@example.com", Name: "Lucía Ejemplo", Sub: "l-1", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.GetWorkspace(t.Context(), wsID)
	if err != nil || ws == nil {
		t.Fatalf("workspace: %v %v", ws, err)
	}
	if ws.Name != "Workspace de Lucía" || ws.Slug != "lucia-ejemplo" {
		t.Errorf("name %q slug %q", ws.Name, ws.Slug)
	}
}

func TestRenameWorkspace(t *testing.T) {
	f := newInvitationFixture(t)
	path := "/auth/workspaces/" + f.wsID
	if w := signupDo(t, f.wrapped, http.MethodPatch, path, f.token, map[string]string{"name": "  Acme Web  "}); w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	ws, _ := f.store.GetWorkspace(t.Context(), f.wsID)
	if ws.Name != "Acme Web" || ws.Slug != "acme" {
		t.Errorf("after rename: name %q slug %q (the slug must not move)", ws.Name, ws.Slug)
	}
	if w := signupDo(t, f.wrapped, http.MethodPatch, path, f.token, map[string]string{"name": " "}); w.Code != http.StatusBadRequest {
		t.Errorf("empty name: %d", w.Code)
	}
	if w := signupDo(t, f.wrapped, http.MethodPatch, path, f.token, map[string]string{"name": strings.Repeat("x", 81)}); w.Code != http.StatusBadRequest {
		t.Errorf("long name: %d", w.Code)
	}

	viewer := ssoUser(t, f.store, "v@acme.io", "V")
	if err := f.store.AddMember(t.Context(), f.wsID, viewer.ID, RoleViewer); err != nil {
		t.Fatal(err)
	}
	vt, err := f.h.issueTokenPair(t.Context(), viewer, f.wsID, RoleViewer, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if w := signupDo(t, f.wrapped, http.MethodPatch, path, vt.AccessToken, map[string]string{"name": "Mine"}); w.Code != http.StatusForbidden {
		t.Errorf("viewer rename: %d, want 403", w.Code)
	}
}

func TestWorkspaceSeats_IgnoresInvitations(t *testing.T) {
	f := newInvitationFixture(t)
	f.h.WithSeatLimit(func(string) int { return 5 })
	if res := f.invite(t, "new@acme.io", RoleDeveloper); res.StatusCode != http.StatusCreated {
		t.Fatalf("invite: %d", res.StatusCode)
	}
	w := signupDo(t, f.wrapped, http.MethodGet, "/auth/workspaces/"+f.wsID+"/seats", f.token, nil)
	var s Seats
	if err := json.NewDecoder(w.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Used != 1 || s.Limit != 5 {
		t.Errorf("seats = %+v, want 1 of 5 (the owner; a pending invitation takes no place)", s)
	}
}

func TestRemoveMember_RevokeKeysOnRequest(t *testing.T) {
	f := newInvitationFixture(t)
	ctx := t.Context()
	dev := ssoUser(t, f.store, "dev@acme.io", "Dev")
	if err := f.store.AddMember(ctx, f.wsID, dev.ID, RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	grant := KeyGrant{WorkspaceID: f.wsID, Role: RoleDeveloper, Permissions: []Permission{PermAll}}
	if _, _, err := f.store.CreateAPIKey(ctx, "ci", dev.ID, grant); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.CreateAPIKey(ctx, "boot", "thulu init", grant); err != nil {
		t.Fatal(err)
	}

	w := signupDo(t, f.wrapped, http.MethodGet, "/auth/api-keys", f.token, nil)
	var keys []apiKeyView
	if err := json.NewDecoder(w.Body).Decode(&keys); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	byName := map[string]apiKeyView{}
	for _, k := range keys {
		byName[k.Name] = k
	}
	if byName["ci"].CreatedByEmail != "dev@acme.io" || byName["boot"].CreatedByEmail != "" {
		t.Errorf("creator emails: ci=%q boot=%q", byName["ci"].CreatedByEmail, byName["boot"].CreatedByEmail)
	}

	path := "/auth/workspaces/" + f.wsID + "/members/" + dev.ID + "?revoke_keys=1"
	if w := signupDo(t, f.wrapped, http.MethodDelete, path, f.token, nil); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	live, err := f.store.ListAPIKeys(ctx, f.wsID)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range live {
		switch {
		case k.Name == "ci" && k.RevokedAt == nil:
			t.Error("the removed member's key is still live")
		case k.Name == "boot" && k.RevokedAt != nil:
			t.Error("somebody else's key was revoked")
		}
	}
}

func TestAccessRequest_Flow(t *testing.T) {
	f := newSignupFixture(t)
	mux := http.NewServeMux()
	f.h.RegisterRoutes(mux)
	wrapped := JWTMiddlewareWithStore(nil, mux)
	op, err := f.h.issueTokenPair(t.Context(), f.opUser, f.opWS, RoleOwner, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}

	if w := signupDo(t, wrapped, http.MethodPost, "/auth/access-request", "", map[string]string{"token": "forged"}); w.Code != http.StatusUnauthorized {
		t.Errorf("forged token: %d", w.Code)
	}
	state, _, err := issueOIDCState("oidc", "http://x", "/", "bind")
	if err != nil {
		t.Fatal(err)
	}
	if w := signupDo(t, wrapped, http.MethodPost, "/auth/access-request", "", map[string]string{"token": state}); w.Code != http.StatusUnauthorized {
		t.Errorf("state as access token: %d", w.Code)
	}

	token, err := issueAccessRequestToken("Ana@Example.com", "Ana")
	if err != nil {
		t.Fatal(err)
	}
	if w := signupDo(t, wrapped, http.MethodPost, "/auth/access-request", "", map[string]string{"token": token}); w.Code != http.StatusOK {
		t.Fatalf("request: %d %s", w.Code, w.Body)
	}
	waiting := func() []InvitationWaiting {
		w := signupDo(t, wrapped, http.MethodGet, "/auth/signup-allowlist", op.AccessToken, nil)
		var list signupAllowlistResponse
		if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
		return list.Waiting
	}
	if got := waiting(); len(got) != 1 || got[0].Email != "ana@example.com" || !got[0].Requested || got[0].Name != "Ana" {
		t.Fatalf("waiting = %+v", got)
	}
	if w := signupDo(t, wrapped, http.MethodPost, "/auth/signup-allowlist", op.AccessToken, map[string]string{"email": "ana@example.com"}); w.Code/100 != 2 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	if got := waiting(); len(got) != 0 {
		t.Errorf("granted request still waiting: %+v", got)
	}

	if err := f.store.AddSignupRequest(t.Context(), "bo@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if w := signupDo(t, wrapped, http.MethodDelete, "/auth/signup-requests/bo@example.com", op.AccessToken, nil); w.Code != http.StatusNoContent {
		t.Errorf("dismiss: %d", w.Code)
	}
	if ok, _ := f.store.SignupAllowlisted(t.Context(), "bo@example.com"); ok {
		t.Error("dismissing granted access")
	}
}

func TestRedirectRefused_Reasons(t *testing.T) {
	f := newSignupFixture(t)
	t.Setenv("OCTOPUS_FRONTEND_ORIGIN", "")
	ws, err := f.store.CreateWorkspace(t.Context(), f.opUser.ID, "Acme Web", "acme-web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertInvitation(t.Context(), ws.ID, "cy@example.com", RoleDeveloper, "op"); err != nil {
		t.Fatal(err)
	}
	id := ssoIdentity{Email: "cy@example.com", Name: "Cy", Sub: "cy", EmailVerified: true}
	_, _, _, admitErr := f.h.provisionSSOUser(ssoRequest(), "oidc", id)

	w := httptest.NewRecorder()
	f.h.redirectRefused(w, ssoRequest(), admitErr, "Example ID", id)
	q := redirectQuery(t, w)
	if q.Get("reason") != loginReasonNotInvited || q.Get("email") != "cy@example.com" ||
		q.Get("invited") != "Acme Web" || q.Get("request") == "" {
		t.Fatalf("not invited: %v", q)
	}
	if p, err := openAccessRequestToken(q.Get("request")); err != nil || p.Email != "cy@example.com" {
		t.Errorf("request token: %+v %v", p, err)
	}

	w = httptest.NewRecorder()
	f.h.redirectRefused(w, ssoRequest(), ErrSSOEmailUnverified, "Example ID", id)
	if q := redirectQuery(t, w); q.Get("reason") != loginReasonEmailUnverified || q.Get("provider") != "Example ID" {
		t.Errorf("unverified: %v", q)
	}

	if got := roundTripReason(errSSOStateExpired); got != loginReasonStateExpired {
		t.Errorf("expired state reason %q", got)
	}
}

func redirectQuery(t *testing.T, w *httptest.ResponseRecorder) url.Values {
	t.Helper()
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query()
}

func TestInvitationPreview(t *testing.T) {
	f := newInvitationFixture(t)
	res := f.invite(t, "ana@example.com", RoleDeveloper)
	var body struct {
		Invitation Invitation `json:"invitation"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	w := signupDo(t, f.wrapped, http.MethodGet, "/auth/invitation?id="+body.Invitation.ID, "", nil)
	var p InvitationPreview
	if err := json.NewDecoder(w.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Workspace != "Acme" || p.Email != "ana@example.com" {
		t.Errorf("preview = %+v", p)
	}
	if w := signupDo(t, f.wrapped, http.MethodGet, "/auth/invitation?id=nope", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown id: %d", w.Code)
	}
	if !isPreSessionAuthPath("/auth/invitation") || !isPreSessionAuthPath("/auth/access-request") {
		t.Error("the login page's endpoints must pass the env-key gate")
	}
}

func TestMe_WorkspaceNameAndCreateReason(t *testing.T) {
	f := newInvitationFixture(t)
	t.Setenv("OCTOPUS_MAX_OWNED_WORKSPACES", "1")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "")
	w := signupDo(t, f.wrapped, http.MethodGet, "/auth/me", f.token, nil)
	var me struct {
		Name   string `json:"workspace_name"`
		Can    bool   `json:"can_create_workspace"`
		Reason string `json:"create_workspace_reason"`
		Max    int    `json:"create_workspace_max"`
	}
	if err := json.NewDecoder(w.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if me.Name != "Acme" || me.Can || me.Reason != createBlockCap || me.Max != 1 {
		t.Errorf("me = %+v", me)
	}
}

func TestLoginConfig_OperatorOnly(t *testing.T) {
	f := newInvitationFixture(t)
	t.Setenv("OCTOPUS_SSO_ALLOWED_DOMAINS", "acme.io, example.com")
	t.Setenv("OCTOPUS_OIDC_ISSUER", "https://id.example")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "")
	if w := signupDo(t, f.wrapped, http.MethodGet, "/auth/login-config", f.token, nil); w.Code != http.StatusForbidden {
		t.Errorf("non-operator: %d", w.Code)
	}
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", f.wsID)
	w := signupDo(t, f.wrapped, http.MethodGet, "/auth/login-config", f.token, nil)
	var c loginConfig
	if err := json.NewDecoder(w.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.AllowedDomains) != 2 || c.OIDC == nil || c.OIDC.Issuer != "https://id.example" || c.SignupMode != SignupOpen {
		t.Errorf("config = %+v", c)
	}
}

func TestOwnerHandoff_FromPlaceholder(t *testing.T) {
	f := newInvitationFixture(t)
	ctx := t.Context()
	placeholder, err := f.store.CreateUser(ctx, "bootstrap@octopus.local", "Bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := f.store.CreateWorkspace(ctx, placeholder.ID, "Corp", "corp")
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, _, err := f.store.CreateAPIKey(ctx, "init", "thulu init",
		KeyGrant{WorkspaceID: ws.ID, Role: RoleOwner, Permissions: []Permission{PermAll}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.h.RegisterRoutes(mux)
	chain := JWTMiddlewareWithStore(f.store, MiddlewareWithStoreAndPublic(f.store, nil, mux))
	add := func(tok, email string) *httptest.ResponseRecorder {
		return signupDo(t, chain, http.MethodPost, "/auth/workspaces/"+ws.ID+"/members", tok,
			map[string]string{"email": email, "role": "owner"})
	}

	if w := add(ownerKey, "ana@corp.io"); w.Code != http.StatusCreated {
		t.Fatalf("owner invitation by the init key: %d %s", w.Code, w.Body)
	}
	user, landed, role, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "ana@corp.io", Name: "Ana", Sub: "ana", EmailVerified: true})
	if err != nil || landed != ws.ID || role != RoleOwner {
		t.Fatalf("first sign-in: ws=%q role=%q err=%v", landed, role, err)
	}
	got, _ := f.store.GetWorkspace(ctx, ws.ID)
	if got.OwnerID != user.ID {
		t.Errorf("owner_id = %q, want the person who signed in", got.OwnerID)
	}
	if r, _ := f.store.GetMemberRole(ctx, ws.ID, placeholder.ID); r != RoleAdmin {
		t.Errorf("placeholder role = %q, want admin", r)
	}

	if w := add(ownerKey, "mallory@corp.io"); w.Code != http.StatusForbidden {
		t.Errorf("hand-over from a real owner: %d", w.Code)
	}
	anaTok, err := f.h.issueTokenPair(ctx, user, ws.ID, RoleOwner, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if w := add(anaTok.AccessToken, "bo@corp.io"); w.Code != http.StatusForbidden {
		t.Errorf("owner session adding an owner: %d", w.Code)
	}
}

func TestRedirectPendingAccess_First(t *testing.T) {
	store := newSSOTestStore(t)
	h := NewHandler(store)
	t.Setenv("OCTOPUS_FRONTEND_ORIGIN", "")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "")
	boot, err := store.CreateUser(t.Context(), "bootstrap@octopus.local", "Bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.CreateWorkspace(t.Context(), boot.ID, "Corp", "corp")
	if err != nil {
		t.Fatal(err)
	}
	id := ssoIdentity{Email: "ana@corp.io"}
	w := httptest.NewRecorder()
	h.redirectPendingAccess(w, ssoRequest(), ErrPendingAccess, id)
	if q := redirectQuery(t, w); q.Get("reason") != loginReasonPendingAccessFirst || q.Get("email") != "ana@corp.io" || q.Get("workspace") != ws.ID {
		t.Errorf("first, sole workspace: %v", q)
	}

	if _, err := store.CreateWorkspace(t.Context(), boot.ID, "Other", "other"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.redirectPendingAccess(w, ssoRequest(), ErrPendingAccess, id)
	if q := redirectQuery(t, w); q.Get("reason") != loginReasonPendingAccessFirst || q.Has("workspace") {
		t.Errorf("first, no workspace to name: %v", q)
	}
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", ws.ID)
	w = httptest.NewRecorder()
	h.redirectPendingAccess(w, ssoRequest(), ErrPendingAccess, id)
	if q := redirectQuery(t, w); q.Get("workspace") != ws.ID {
		t.Errorf("first, operator workspace: %v", q)
	}

	op := ssoUser(t, store, "op@corp.io", "Op")
	if err := store.LinkOIDCIdentity(t.Context(), op.ID, "oidc", "op", op.Email); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(t.Context(), ws.ID, op.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.redirectPendingAccess(w, ssoRequest(), ErrPendingAccess, id)
	if q := redirectQuery(t, w); q.Get("reason") != loginReasonPendingAccess {
		t.Errorf("later: %v", q)
	}
}

func TestWorkspaceSeats_AgreeWithTheCap(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 2, 3} {
		f := newInvitationFixture(t)
		f.h.WithSeatLimit(func(string) int { return limit })
		ssoUser(t, f.store, "known@acme.io", "Known")
		for i, email := range []string{"new@acme.io", "known@acme.io", "late@acme.io"} {
			w := signupDo(t, f.wrapped, http.MethodGet, "/auth/workspaces/"+f.wsID+"/seats", f.token, nil)
			var s Seats
			if err := json.NewDecoder(w.Body).Decode(&s); err != nil {
				t.Fatal(err)
			}
			if s.Limit < 0 || (limit > 0 && s.Limit != limit) || (limit <= 0 && s.Limit != 0) {
				t.Fatalf("limit %d: GET /seats limit = %d", limit, s.Limit)
			}
			want := http.StatusCreated
			if s.Full() {
				want = http.StatusPaymentRequired
			}
			res := f.invite(t, email, RoleDeveloper)
			if res.StatusCode != want {
				t.Errorf("limit %d, add #%d (%s): seats %+v say %d, the add answered %d",
					limit, i+1, email, s, want, res.StatusCode)
			}
		}
	}
}

func TestSeats_Full(t *testing.T) {
	for _, c := range []struct {
		s    Seats
		full bool
	}{
		{Seats{Used: 9, Limit: 0}, false},
		{Seats{Used: 2, Limit: 3}, false},
		{Seats{Used: 3, Limit: 3}, true},
		{Seats{Used: 4, Limit: 3}, true},
	} {
		if got := c.s.Full(); got != c.full {
			t.Errorf("%+v.Full() = %v, want %v", c.s, got, c.full)
		}
	}
}
