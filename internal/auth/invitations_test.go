package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

type invitationFixture struct {
	h       *Handler
	store   *UserStore
	wrapped http.Handler
	owner   *User
	wsID    string
	token   string
}

func newInvitationFixture(t *testing.T) invitationFixture {
	t.Helper()
	t.Setenv("OCTOPUS_SIGNUP_MODE", "open")
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	t.Setenv("OCTOPUS_SSO_DEFAULT_WORKSPACE", "")
	t.Setenv("OCTOPUS_SSO_ALLOWED_EMAILS", "")
	store := newSSOTestStore(t)
	h := NewHandler(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f := invitationFixture{h: h, store: store, wrapped: JWTMiddlewareWithStore(nil, mux)}
	f.owner = ssoUser(t, store, "owner@acme.io", "Owner")
	ws, err := store.CreateWorkspace(t.Context(), f.owner.ID, "Acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	f.wsID = ws.ID
	tokens, err := h.issueTokenPair(t.Context(), f.owner, ws.ID, RoleOwner, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}
	f.token = tokens.AccessToken
	return f
}

func (f invitationFixture) invite(t *testing.T, email string, role Role) *http.Response {
	t.Helper()
	w := signupDo(t, f.wrapped, http.MethodPost, "/auth/workspaces/"+f.wsID+"/members", f.token,
		map[string]string{"email": email, "role": string(role)})
	return w.Result()
}

func (f invitationFixture) list(t *testing.T) []Invitation {
	t.Helper()
	w := signupDo(t, f.wrapped, http.MethodGet, "/auth/workspaces/"+f.wsID+"/invitations", f.token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list invitations: %d %s", w.Code, w.Body)
	}
	var out []Invitation
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInvitation_FirstSignInLandsInInvitingWorkspace(t *testing.T) {
	f := newInvitationFixture(t)

	res := f.invite(t, "Ana@Example.com", RoleViewer)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("invite: status %d", res.StatusCode)
	}
	var body struct {
		Status     string     `json:"status"`
		Invitation Invitation `json:"invitation"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "invited" || body.Invitation.Email != "ana@example.com" || body.Invitation.AwaitingAccess {
		t.Fatalf("invite body = %+v", body)
	}

	if res := f.invite(t, "ana@example.com", RoleDeveloper); res.StatusCode != http.StatusCreated {
		t.Fatalf("re-invite: status %d", res.StatusCode)
	}
	invs := f.list(t)
	if len(invs) != 1 || invs[0].Role != RoleDeveloper {
		t.Fatalf("invitations after re-invite = %+v", invs)
	}

	user, ws, role, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "ana@example.com", Sub: "ana-sub", EmailVerified: true})
	if err != nil {
		t.Fatalf("sign-in: %v", err)
	}
	if ws != f.wsID || role != RoleDeveloper {
		t.Errorf("landed in %q as %q, want %q as developer", ws, role, f.wsID)
	}
	mine, err := f.store.ListMemberships(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 {
		t.Errorf("memberships = %+v, want only the inviting workspace", mine)
	}
	if left := f.list(t); len(left) != 0 {
		t.Errorf("invitation still pending after sign-in: %+v", left)
	}
}

func TestInvitation_ExistingAccountAcceptsAtNextSignIn(t *testing.T) {
	f := newInvitationFixture(t)
	if _, _, _, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "bo@example.com", Sub: "bo-sub", EmailVerified: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpsertInvitation(t.Context(), f.wsID, "bo@example.com", RoleViewer, "owner"); err != nil {
		t.Fatal(err)
	}
	user, ws, _, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "bo@example.com", Sub: "bo-sub", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	if ws == f.wsID {
		t.Error("existing account moved into the inviting workspace; it should keep its own")
	}
	role, err := f.store.GetMemberRole(t.Context(), f.wsID, user.ID)
	if err != nil || role != RoleViewer {
		t.Errorf("role in inviting workspace = %q, %v; want viewer", role, err)
	}
}

func TestInvitation_AllowlistStillGates(t *testing.T) {
	f := newInvitationFixture(t)
	t.Setenv("OCTOPUS_SIGNUP_MODE", "allowlist")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", f.wsID)

	res := f.invite(t, "cy@example.com", RoleDeveloper)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("invite: status %d", res.StatusCode)
	}
	if invs := f.list(t); len(invs) != 1 || !invs[0].AwaitingAccess {
		t.Fatalf("invitation should await access: %+v", invs)
	}

	_, _, _, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "cy@example.com", Sub: "cy-sub", EmailVerified: true})
	if !errors.Is(err, ErrSSONotAllowed) {
		t.Fatalf("invited but not listed: err = %v, want ErrSSONotAllowed", err)
	}

	w := signupDo(t, f.wrapped, http.MethodGet, "/auth/signup-allowlist", f.token, nil)
	var list signupAllowlistResponse
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Waiting) != 1 || list.Waiting[0].Email != "cy@example.com" ||
		len(list.Waiting[0].Workspaces) != 1 || list.Waiting[0].Workspaces[0] != "Acme" {
		t.Fatalf("waiting = %+v", list.Waiting)
	}

	if _, err := f.store.AddSignupAllowlist(t.Context(), "cy@example.com", "", "op"); err != nil {
		t.Fatal(err)
	}
	_, ws, role, err := f.h.provisionSSOUser(ssoRequest(), "oidc",
		ssoIdentity{Email: "cy@example.com", Sub: "cy-sub", EmailVerified: true})
	if err != nil || ws != f.wsID || role != RoleDeveloper {
		t.Fatalf("after listing: ws=%q role=%q err=%v", ws, role, err)
	}
}

func TestInvitation_SeatCapCountsPending(t *testing.T) {
	f := newInvitationFixture(t)
	f.h.WithSeatLimit(func(string) int { return 2 })

	if res := f.invite(t, "one@example.com", RoleDeveloper); res.StatusCode != http.StatusCreated {
		t.Fatalf("first invite: status %d", res.StatusCode)
	}
	if res := f.invite(t, "two@example.com", RoleDeveloper); res.StatusCode != http.StatusPaymentRequired {
		t.Errorf("second invite past the cap: status %d, want 402", res.StatusCode)
	}
	if res := f.invite(t, "one@example.com", RoleViewer); res.StatusCode != http.StatusCreated {
		t.Errorf("re-invite within the cap: status %d, want 201", res.StatusCode)
	}
	ssoUser(t, f.store, "three@example.com", "Three")
	if res := f.invite(t, "three@example.com", RoleDeveloper); res.StatusCode != http.StatusPaymentRequired {
		t.Errorf("existing account past the cap: status %d, want 402", res.StatusCode)
	}
}

func TestInvitation_ChangeAndRevoke(t *testing.T) {
	f := newInvitationFixture(t)
	if res := f.invite(t, "dee@example.com", RoleDeveloper); res.StatusCode != http.StatusCreated {
		t.Fatal(res.StatusCode)
	}
	inv := f.list(t)[0]
	path := "/auth/workspaces/" + f.wsID + "/invitations/" + inv.ID

	if w := signupDo(t, f.wrapped, http.MethodPatch, path, f.token, map[string]string{"role": "owner"}); w.Code != http.StatusForbidden {
		t.Errorf("invite as owner: status %d, want 403", w.Code)
	}
	if w := signupDo(t, f.wrapped, http.MethodPatch, path, f.token, map[string]string{"role": "admin"}); w.Code != http.StatusOK {
		t.Errorf("change role: status %d %s", w.Code, w.Body)
	}
	if got := f.list(t)[0].Role; got != RoleAdmin {
		t.Errorf("role = %q, want admin", got)
	}

	other := ssoUser(t, f.store, "other@example.com", "Other")
	otherWS, err := f.store.CreateWorkspace(t.Context(), other.ID, "Other", "other")
	if err != nil {
		t.Fatal(err)
	}
	otherTok, err := f.h.issueTokenPair(t.Context(), other, otherWS.ID, RoleOwner, deviceInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if w := signupDo(t, f.wrapped, http.MethodDelete, path, otherTok.AccessToken, nil); w.Code != http.StatusForbidden {
		t.Errorf("other workspace's owner deleting: status %d, want 403", w.Code)
	}
	if w := signupDo(t, f.wrapped, http.MethodDelete,
		"/auth/workspaces/"+otherWS.ID+"/invitations/"+inv.ID, otherTok.AccessToken, nil); w.Code != http.StatusNotFound {
		t.Errorf("deleting through own workspace: status %d, want 404", w.Code)
	}

	if w := signupDo(t, f.wrapped, http.MethodDelete, path, f.token, nil); w.Code != http.StatusNoContent {
		t.Errorf("revoke: status %d", w.Code)
	}
	if left := f.list(t); len(left) != 0 {
		t.Errorf("still pending: %+v", left)
	}
}

func TestMe_CanCreateWorkspace(t *testing.T) {
	f := newInvitationFixture(t)
	t.Setenv("OCTOPUS_MAX_OWNED_WORKSPACES", "2")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "")

	canCreate := func() bool {
		t.Helper()
		w := signupDo(t, f.wrapped, http.MethodGet, "/auth/me", f.token, nil)
		var me struct {
			CanCreate bool `json:"can_create_workspace"`
		}
		if err := json.NewDecoder(w.Body).Decode(&me); err != nil {
			t.Fatal(err)
		}
		return me.CanCreate
	}
	if !canCreate() {
		t.Error("owner of one workspace under a cap of two: can_create_workspace = false")
	}
	if _, err := f.store.CreateWorkspace(t.Context(), f.owner.ID, "Second", "second"); err != nil {
		t.Fatal(err)
	}
	if canCreate() {
		t.Error("owner at the cap: can_create_workspace = true")
	}
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", f.wsID)
	if !canCreate() {
		t.Error("operator admin at the cap: can_create_workspace = false")
	}
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "0")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "")
	if canCreate() {
		t.Error("personal workspaces off: can_create_workspace = true")
	}
}
