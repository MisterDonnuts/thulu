package auth_test

import (
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

type membersFixture struct {
	store                *auth.UserStore
	wrapped              http.Handler
	wsID                 string
	aliceID, bobID       string
	malloryID            string
	aliceTok, malloryTok string
	bobRefresh           string
	aliceEmail, bobEmail string
}

func newMembersFixture(t *testing.T) membersFixture {
	t.Helper()
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f := membersFixture{store: store, wrapped: authedMux(mux), aliceEmail: "alice@example.com", bobEmail: "bob@example.com"}

	alice := mustProvision(t, h, f.aliceEmail, "Alice")
	f.aliceID, f.aliceTok = alice["user_id"].(string), alice["access_token"].(string)
	f.wsID = getFirstWorkspaceID(t, f.wrapped, f.aliceTok)

	mallory := mustProvision(t, h, "mallory@example.com", "Mallory")
	f.malloryID, f.malloryTok = mallory["user_id"].(string), mallory["access_token"].(string)
	if err := store.AddMember(t.Context(), f.wsID, f.malloryID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	bob := mustProvision(t, h, f.bobEmail, "Bob")
	f.bobID, f.bobRefresh = bob["user_id"].(string), bob["refresh_token"].(string)
	return f
}

func TestAddMember_CannotRewriteOwner(t *testing.T) {
	f := newMembersFixture(t)
	path := "/auth/workspaces/" + f.wsID + "/members"

	w := authedPost(t, f.wrapped, path, map[string]string{"email": f.aliceEmail, "role": "viewer"}, f.malloryTok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin re-adding the owner as viewer: status %d, want 403 (body %s)", w.Code, w.Body)
	}
	if w := authedPost(t, f.wrapped, path, map[string]string{"email": f.aliceEmail, "role": "admin"}, f.aliceTok); w.Code != http.StatusForbidden {
		t.Errorf("owner re-adding themself as admin: status %d, want 403", w.Code)
	}
	if role, _ := f.store.GetMemberRole(t.Context(), f.wsID, f.aliceID); role != auth.RoleOwner {
		t.Errorf("owner's role = %q, want owner", role)
	}

	if err := f.store.AddMember(t.Context(), f.wsID, f.aliceID, auth.RoleViewer); err != nil {
		t.Fatalf("store re-add of the owner: %v", err)
	}
	if role, _ := f.store.GetMemberRole(t.Context(), f.wsID, f.aliceID); role != auth.RoleOwner {
		t.Errorf("store re-add changed the owner's role to %q", role)
	}
}

func TestAddMember_NeverGrantsOwner(t *testing.T) {
	f := newMembersFixture(t)
	path := "/auth/workspaces/" + f.wsID + "/members"
	for name, tok := range map[string]string{"admin": f.malloryTok, "owner": f.aliceTok} {
		if w := authedPost(t, f.wrapped, path, map[string]string{"email": f.bobEmail, "role": "owner"}, tok); w.Code != http.StatusForbidden {
			t.Errorf("%s adding bob as owner: status %d, want 403", name, w.Code)
		}
	}
	if role, _ := f.store.GetMemberRole(t.Context(), f.wsID, f.bobID); role != "" {
		t.Errorf("bob became %q", role)
	}
	if err := f.store.AddMember(t.Context(), f.wsID, f.bobID, auth.RoleOwner); err == nil {
		t.Error("store accepted owner on add")
	}
}

func TestMemberRole_RejectsUnknownRoles(t *testing.T) {
	f := newMembersFixture(t)
	path := "/auth/workspaces/" + f.wsID + "/members"
	for _, role := range []string{"superuser", "Admin", "owner ", "*"} {
		if w := authedPost(t, f.wrapped, path, map[string]string{"email": f.bobEmail, "role": role}, f.aliceTok); w.Code != http.StatusBadRequest {
			t.Errorf("add with role %q: status %d, want 400", role, w.Code)
		}
	}
	if w := authedPost(t, f.wrapped, path, map[string]string{"email": f.bobEmail, "role": "developer"}, f.aliceTok); w.Code != http.StatusCreated {
		t.Fatalf("add bob: status %d", w.Code)
	}
	if w := authedPatch(t, f.wrapped, path+"/"+f.bobID, map[string]string{"role": "superuser"}, f.aliceTok); w.Code != http.StatusBadRequest {
		t.Errorf("update to role superuser: status %d, want 400", w.Code)
	}
	if role, _ := f.store.GetMemberRole(t.Context(), f.wsID, f.bobID); role != auth.RoleDeveloper {
		t.Errorf("bob's role = %q, want developer", role)
	}
}

func TestAddMember_RoleChangeRevokesSessions(t *testing.T) {
	f := newMembersFixture(t)
	ctx := t.Context()
	path := "/auth/workspaces/" + f.wsID + "/members"
	if w := authedPost(t, f.wrapped, path, map[string]string{"email": f.bobEmail, "role": "admin"}, f.aliceTok); w.Code != http.StatusCreated {
		t.Fatalf("add bob: status %d", w.Code)
	}
	if _, _, err := f.store.ValidateRefreshToken(auth.HashRefreshToken(f.bobRefresh)); err != nil {
		t.Fatalf("first add revoked bob's refresh token: %v", err)
	}
	before, _ := f.store.GetTokenVersion(ctx, f.bobID)

	if w := authedPost(t, f.wrapped, path, map[string]string{"email": f.bobEmail, "role": "viewer"}, f.aliceTok); w.Code != http.StatusCreated {
		t.Fatalf("re-add bob as viewer: status %d", w.Code)
	}
	if _, _, err := f.store.ValidateRefreshToken(auth.HashRefreshToken(f.bobRefresh)); err == nil {
		t.Error("downgrade by re-add left bob's refresh token valid")
	}
	if after, _ := f.store.GetTokenVersion(ctx, f.bobID); after == before {
		t.Error("downgrade by re-add did not bump bob's token version")
	}
}

func TestMemberChange_NonMemberIsNotSignedOut(t *testing.T) {
	f := newMembersFixture(t)
	ctx := t.Context()
	path := "/auth/workspaces/" + f.wsID + "/members/" + f.bobID
	before, _ := f.store.GetTokenVersion(ctx, f.bobID)

	if w := authedPatch(t, f.wrapped, path, map[string]string{"role": "viewer"}, f.malloryTok); w.Code != http.StatusNotFound {
		t.Errorf("role change for a non-member: status %d, want 404 (body %s)", w.Code, w.Body)
	}
	if w := authedDelete(t, f.wrapped, path, f.malloryTok); w.Code != http.StatusNotFound {
		t.Errorf("removal of a non-member: status %d, want 404 (body %s)", w.Code, w.Body)
	}
	if _, _, err := f.store.ValidateRefreshToken(auth.HashRefreshToken(f.bobRefresh)); err != nil {
		t.Errorf("bob's refresh token was revoked by a workspace he is not in: %v", err)
	}
	if after, _ := f.store.GetTokenVersion(ctx, f.bobID); after != before {
		t.Error("bob's token version was bumped by a workspace he is not in")
	}
	if role, _ := f.store.GetMemberRole(ctx, f.wsID, f.bobID); role != "" {
		t.Errorf("bob became %q", role)
	}
}

func TestUpdateMemberRole_NeverTouchesOwner(t *testing.T) {
	f := newMembersFixture(t)
	path := "/auth/workspaces/" + f.wsID + "/members/"
	for name, tok := range map[string]string{"admin": f.malloryTok, "owner": f.aliceTok} {
		if w := authedPatch(t, f.wrapped, path+f.malloryID, map[string]string{"role": "owner"}, tok); w.Code != http.StatusForbidden {
			t.Errorf("%s granting owner by role change: status %d, want 403 (body %s)", name, w.Code, w.Body)
		}
		if w := authedPatch(t, f.wrapped, path+f.aliceID, map[string]string{"role": "admin"}, tok); w.Code != http.StatusForbidden {
			t.Errorf("%s changing the owner's role: status %d, want 403 (body %s)", name, w.Code, w.Body)
		}
	}
	if role, _ := f.store.GetMemberRole(t.Context(), f.wsID, f.aliceID); role != auth.RoleOwner {
		t.Errorf("owner's role = %q, want owner", role)
	}
	if role, _ := f.store.GetMemberRole(t.Context(), f.wsID, f.malloryID); role != auth.RoleAdmin {
		t.Errorf("mallory's role = %q, want admin", role)
	}
	if w := authedPatch(t, f.wrapped, path+f.malloryID, map[string]string{"role": "developer"}, f.aliceTok); w.Code != http.StatusOK {
		t.Errorf("owner demoting an admin: status %d, want 200 (body %s)", w.Code, w.Body)
	}
}
