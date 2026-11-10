package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type accountFixture struct {
	t      *testing.T
	store  *UserStore
	h      *Handler
	srv    http.Handler
	purged []string

	mu     sync.Mutex
	events []AuditEvent
}

func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	f := &accountFixture{t: t, store: newSSOTestStore(t)}
	f.h = NewHandler(f.store).
		WithAudit(func(e AuditEvent) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.events = append(f.events, e)
		}).
		WithWorkspacePurge(func(ctx context.Context, wsID string) error {
			f.purged = append(f.purged, wsID)
			_, err := f.store.DeleteWorkspace(ctx, wsID)
			return err
		})
	mux := http.NewServeMux()
	f.h.RegisterRoutes(mux)
	f.srv = JWTMiddlewareWithStore(f.store, mux)
	return f
}

type person struct {
	user    *User
	ws      *Workspace
	access  string
	refresh string
}

func (f *accountFixture) signIn(email, slug string) *person {
	f.t.Helper()
	ctx := f.t.Context()
	u, err := f.store.CreateUser(ctx, email, email)
	if err != nil {
		f.t.Fatal(err)
	}
	ws, err := f.store.CreateWorkspace(ctx, u.ID, slug, slug)
	if err != nil {
		f.t.Fatal(err)
	}
	tok, err := f.h.issueTokenPair(ctx, u, ws.ID, RoleOwner, deviceInfo{})
	if err != nil {
		f.t.Fatal(err)
	}
	return &person{user: u, ws: ws, access: tok.AccessToken, refresh: tok.RefreshToken}
}

func (f *accountFixture) addMember(ws *Workspace, p *person, role Role) {
	f.t.Helper()
	if err := f.store.AddMember(f.t.Context(), ws.ID, p.user.ID, role); err != nil {
		f.t.Fatal(err)
	}
}

func (f *accountFixture) do(p *person, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if p != nil {
		r.Header.Set("Authorization", "Bearer "+p.access)
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

func (f *accountFixture) asKey(wsID, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(withKeyAuth(r.Context(), &APIKeyInfo{
		ID: "k-owner", WorkspaceID: wsID, Role: RoleOwner, Permissions: AllPermissions(),
	}))
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

func (f *accountFixture) event(kind string) (AuditEvent, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.Kind == kind {
			return e, true
		}
	}
	return AuditEvent{}, false
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
}

func TestDeleteAccount_ErasesOwnedWorkspacesAndTheAccount(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(bob.ws, ana, RoleDeveloper)
	if err := f.store.LinkOIDCIdentity(ctx, ana.user.ID, "example-id", "sub-ana", ana.user.Email); err != nil {
		t.Fatal(err)
	}
	_, minted, err := f.store.CreateAPIKey(ctx, "ana-ci", ana.user.ID,
		KeyGrant{WorkspaceID: bob.ws.ID, Role: RoleDeveloper, Permissions: []Permission{PermTasksRead}})
	if err != nil {
		t.Fatal(err)
	}
	_, delegated, err := f.store.CreateRunScopedAPIKey(ctx,
		RunScope{ChatID: "chat-1", DelegatedBy: ana.user.ID}, bob.ws.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, bobs, err := f.store.CreateAPIKey(ctx, "bob-ci", bob.user.ID,
		KeyGrant{WorkspaceID: bob.ws.ID, Role: RoleDeveloper, Permissions: []Permission{PermTasksRead}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ana.user.ID, bob.user.ID} {
		if err := f.store.SetPreference(ctx, id, "tasks.view", `{"layout":"list"}`); err != nil {
			t.Fatal(err)
		}
	}

	w := f.do(ana, http.MethodDelete, "/auth/me", map[string]string{"confirm": "  ANA@corp.com "})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var resp struct {
		Status            string   `json:"status"`
		WorkspacesDeleted []string `json:"workspaces_deleted"`
		APIKeysRevoked    int      `json:"api_keys_revoked"`
	}
	decodeBody(t, w, &resp)
	if len(resp.WorkspacesDeleted) != 1 || resp.WorkspacesDeleted[0] != ana.ws.ID {
		t.Errorf("workspaces_deleted = %v, want only %s", resp.WorkspacesDeleted, ana.ws.ID)
	}
	if resp.APIKeysRevoked != 2 {
		t.Errorf("api_keys_revoked = %d, want 2", resp.APIKeysRevoked)
	}
	if len(f.purged) != 1 || f.purged[0] != ana.ws.ID {
		t.Errorf("purged = %v, want only Ana's workspace (Bob's is not hers)", f.purged)
	}

	if u, _ := f.store.GetUserByID(ctx, ana.user.ID); u != nil {
		t.Error("user row survived the deletion")
	}
	if ws, _ := f.store.GetWorkspace(ctx, ana.ws.ID); ws != nil {
		t.Error("owned workspace survived the deletion")
	}
	if ws, _ := f.store.GetWorkspace(ctx, bob.ws.ID); ws == nil {
		t.Error("a workspace Ana only belonged to was deleted")
	}
	if role, _ := f.store.GetMemberRole(ctx, bob.ws.ID, ana.user.ID); role != "" {
		t.Errorf("membership in Bob's workspace survived: %q", role)
	}
	if u, _ := f.store.GetUserByOIDCSub(ctx, "example-id", "sub-ana"); u != nil {
		t.Error("IdP link survived: a re-login would land in the deleted account")
	}
	if p, err := f.store.Preferences(ctx, ana.user.ID); err != nil || len(p) != 0 {
		t.Errorf("Ana's preferences survived the deletion: %v %v", p, err)
	}
	if p, _ := f.store.Preferences(ctx, bob.user.ID); len(p) != 1 {
		t.Errorf("Bob's preferences = %v, want his own still there", p)
	}
	if rec, _ := f.store.LookupRefreshToken(ctx, HashRefreshToken(ana.refresh)); rec.State != RefreshUnknown {
		t.Errorf("refresh token state = %v, want gone", rec.State)
	}
	keys, err := f.store.ListAPIKeys(ctx, bob.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		switch k.ID {
		case minted.ID, delegated.ID:
			if k.RevokedAt == nil {
				t.Errorf("key %s (%s) still live after its author was deleted", k.Name, k.ID)
			}
		case bobs.ID:
			if k.RevokedAt != nil {
				t.Error("Bob's own key was revoked")
			}
		}
	}

	if w := f.do(ana, http.MethodGet, "/auth/me", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /auth/me with the deleted account's token = %d, want 401", w.Code)
	}
	cleared := map[string]bool{}
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared[c.Name] = true
		}
	}
	if !cleared[CookieAccess] || !cleared[CookieRefresh] {
		t.Errorf("session cookies not cleared: %v", cleared)
	}

	e, ok := f.event("user.deleted")
	if !ok {
		t.Fatal("no user.deleted audit event")
	}
	if e.WorkspaceID != "" || e.ResourceID != ana.user.ID {
		t.Errorf("user.deleted event = %+v, want install-level about Ana", e)
	}
	if e, ok := f.event("workspace.deleted"); !ok || e.WorkspaceID != ana.ws.ID {
		t.Errorf("workspace.deleted event = %+v (found %v), want one in Ana's workspace", e, ok)
	}
}

func TestDeleteAccount_ConfirmMustBeTheEmail(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	for _, body := range []any{nil, map[string]string{}, map[string]string{"confirm": "bob@corp.com"}, map[string]string{"confirm": "ana-ws"}} {
		if w := f.do(ana, http.MethodDelete, "/auth/me", body); w.Code != http.StatusBadRequest {
			t.Errorf("confirm %v: status = %d, want 400", body, w.Code)
		}
	}
	if u, _ := f.store.GetUserByID(t.Context(), ana.user.ID); u == nil {
		t.Fatal("account deleted without a matching confirmation")
	}
	if len(f.purged) != 0 {
		t.Errorf("purged %v without a confirmation", f.purged)
	}
}

func TestDeleteAccount_EmptyEmailNeverConfirms(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE users SET email = '' WHERE id = $1`, ana.user.ID); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{map[string]string{}, map[string]string{"confirm": ""}, map[string]string{"confirm": "  "}} {
		if w := f.do(ana, http.MethodDelete, "/auth/me", body); w.Code != http.StatusBadRequest {
			t.Errorf("confirm %v: status = %d, want 400", body, w.Code)
		}
	}
	if u, _ := f.store.GetUserByID(t.Context(), ana.user.ID); u == nil {
		t.Fatal("an account without an email was deleted by an empty confirmation")
	}
}

func TestDeleteAccount_RefusesWhileOwningASharedWorkspace(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(ana.ws, bob, RoleDeveloper)
	solo, err := f.store.CreateWorkspace(t.Context(), ana.user.ID, "solo", "solo")
	if err != nil {
		t.Fatal(err)
	}

	w := f.do(ana, http.MethodDelete, "/auth/me", map[string]string{"confirm": "ana@corp.com"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body)
	}
	var resp struct {
		Error      string `json:"error"`
		Code       string `json:"code"`
		Workspaces []struct {
			ID          string `json:"id"`
			Slug        string `json:"slug"`
			MemberCount int    `json:"member_count"`
		} `json:"workspaces"`
	}
	decodeBody(t, w, &resp)
	if resp.Error == "" || resp.Code == "" {
		t.Errorf("409 without the usual error/code: %s", w.Body)
	}
	if len(resp.Workspaces) != 1 || resp.Workspaces[0].ID != ana.ws.ID || resp.Workspaces[0].MemberCount != 2 {
		t.Errorf("workspaces = %+v, want only %s with 2 members", resp.Workspaces, ana.ws.ID)
	}
	if len(f.purged) != 0 {
		t.Errorf("purged %v on a refused deletion", f.purged)
	}
	if ws, _ := f.store.GetWorkspace(t.Context(), solo.ID); ws == nil {
		t.Error("the solo workspace was erased on a refused deletion")
	}
	if u, _ := f.store.GetUserByID(t.Context(), ana.user.ID); u == nil {
		t.Error("account deleted despite the conflict")
	}
}

func TestAccountRoutes_RefuseAPIKeysAndAnonymous(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(ana.ws, bob, RoleAdmin)

	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodDelete, "/auth/me", map[string]string{"confirm": "ana@corp.com"}},
		{http.MethodPost, "/auth/workspaces/" + ana.ws.ID + "/transfer", map[string]string{"user_id": bob.user.ID}},
		{http.MethodPost, "/auth/switch-workspace", map[string]string{"workspace_id": bob.ws.ID}},
	}
	for _, rt := range routes {
		if w := f.asKey(ana.ws.ID, rt.method, rt.path, rt.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with an owner API key = %d, want 403", rt.method, rt.path, w.Code)
		}
		if w := f.do(nil, rt.method, rt.path, rt.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a credential = %d, want 401", rt.method, rt.path, w.Code)
		}
	}

	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	for _, rt := range routes {
		r := httptest.NewRequest(rt.method, rt.path, bytes.NewReader([]byte(`{}`)))
		r = r.WithContext(context.WithValue(r.Context(), devSessionKey{}, true))
		w := httptest.NewRecorder()
		f.srv.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s from the no-login dev session = %d, want 403", rt.method, rt.path, w.Code)
		}
	}
	if u, _ := f.store.GetUserByID(t.Context(), ana.user.ID); u == nil {
		t.Error("a key deleted the account")
	}
	if ws, _ := f.store.GetWorkspace(t.Context(), ana.ws.ID); ws == nil || ws.OwnerID != ana.user.ID {
		t.Error("a key transferred the workspace")
	}
}

func TestTransferOwnership_SwapsOwnerAndRolesTogether(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(ana.ws, bob, RoleDeveloper)

	w := f.do(ana, http.MethodPost, "/auth/workspaces/"+ana.ws.ID+"/transfer", map[string]string{"user_id": bob.user.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	ws, _ := f.store.GetWorkspace(ctx, ana.ws.ID)
	if ws.OwnerID != bob.user.ID {
		t.Errorf("owner_id = %s, want Bob", ws.OwnerID)
	}
	if r, _ := f.store.GetMemberRole(ctx, ana.ws.ID, bob.user.ID); r != RoleOwner {
		t.Errorf("Bob's role = %q, want owner", r)
	}
	if r, _ := f.store.GetMemberRole(ctx, ana.ws.ID, ana.user.ID); r != RoleAdmin {
		t.Errorf("Ana's role = %q, want admin", r)
	}
	if w := f.do(ana, http.MethodGet, "/auth/me", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("Ana's pre-transfer token = %d, want 401 (stale owner claim)", w.Code)
	}
	if _, ok := f.event("workspace.ownership_transferred"); !ok {
		t.Error("no workspace.ownership_transferred audit event")
	}
}

func TestTransferOwnership_DemotesTheOldOwnersOwnerKeys(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(ana.ws, bob, RoleDeveloper)
	mint := func(by string, ws *Workspace, role Role) string {
		raw, _, err := f.store.CreateAPIKey(ctx, "k", by, KeyGrant{WorkspaceID: ws.ID, Role: role, Permissions: []Permission{PermAll}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	anaOwnerKey := mint(ana.user.ID, ana.ws, RoleOwner)
	anaDevKey := mint(ana.user.ID, ana.ws, RoleDeveloper)
	anaElsewhere := mint(ana.user.ID, bob.ws, RoleOwner)

	if w := f.do(ana, http.MethodPost, "/auth/workspaces/"+ana.ws.ID+"/transfer", map[string]string{"user_id": bob.user.ID}); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	for raw, want := range map[string]Role{anaOwnerKey: RoleAdmin, anaDevKey: RoleDeveloper, anaElsewhere: RoleOwner} {
		info := f.store.LookupAPIKeyInfo(ctx, raw)
		if info == nil || info.Role != want {
			t.Errorf("key in %s: %+v, want role %s", info.WorkspaceID, info, want)
		}
	}
}

func TestTransferOwnership_CookieSessionStaysSignedIn(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(ana.ws, bob, RoleDeveloper)

	r := httptest.NewRequest(http.MethodPost, "/auth/workspaces/"+ana.ws.ID+"/transfer",
		bytes.NewReader([]byte(`{"user_id":"`+bob.user.ID+`"}`)))
	r.AddCookie(&http.Cookie{Name: CookieAccess, Value: ana.access})
	r.AddCookie(&http.Cookie{Name: CookieRefresh, Value: ana.refresh})
	r.AddCookie(&http.Cookie{Name: CookieCSRF, Value: "c"})
	r.Header.Set("X-CSRF-Token", "c")
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var access string
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieAccess {
			access = c.Value
		}
	}
	if access == "" {
		t.Fatal("no fresh access cookie after the transfer")
	}
	claims, err := ValidateJWT(access)
	if err != nil || claims.WorkspaceID != ana.ws.ID || claims.Role != RoleAdmin {
		t.Fatalf("fresh access token = %+v (%v), want her workspace as admin", claims, err)
	}
	me := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	me.AddCookie(&http.Cookie{Name: CookieAccess, Value: access})
	mw := httptest.NewRecorder()
	f.srv.ServeHTTP(mw, me)
	if mw.Code != http.StatusOK {
		t.Errorf("GET /auth/me with the fresh cookie = %d, want 200", mw.Code)
	}
}

func TestTransferOwnership_OnlyTheOwnerToAMember(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	eve := f.signIn("eve@corp.com", "eve-ws")
	f.addMember(ana.ws, bob, RoleAdmin)
	path := "/auth/workspaces/" + ana.ws.ID + "/transfer"

	if w := f.do(bob, http.MethodPost, path, map[string]string{"user_id": bob.user.ID}); w.Code != http.StatusForbidden {
		t.Errorf("admin transferring to self = %d, want 403", w.Code)
	}
	if w := f.do(eve, http.MethodPost, path, map[string]string{"user_id": eve.user.ID}); w.Code != http.StatusForbidden {
		t.Errorf("outsider transferring = %d, want 403", w.Code)
	}
	if w := f.do(ana, http.MethodPost, path, map[string]string{"user_id": eve.user.ID}); w.Code != http.StatusBadRequest {
		t.Errorf("transfer to a non-member = %d, want 400", w.Code)
	}
	if w := f.do(ana, http.MethodPost, path, map[string]string{"user_id": "no-such-user"}); w.Code != http.StatusBadRequest {
		t.Errorf("transfer to an unknown user = %d, want 400", w.Code)
	}
	if w := f.do(ana, http.MethodPost, path, map[string]string{"user_id": ana.user.ID}); w.Code != http.StatusBadRequest {
		t.Errorf("transfer to self = %d, want 400", w.Code)
	}
	if w := f.do(ana, http.MethodPost, path, map[string]string{}); w.Code != http.StatusBadRequest {
		t.Errorf("transfer without user_id = %d, want 400", w.Code)
	}
	if w := f.do(ana, http.MethodPost, "/auth/workspaces/nope/transfer", map[string]string{"user_id": bob.user.ID}); w.Code != http.StatusNotFound {
		t.Errorf("transfer of a missing workspace = %d, want 404", w.Code)
	}

	ws, _ := f.store.GetWorkspace(ctx, ana.ws.ID)
	if ws.OwnerID != ana.user.ID {
		t.Errorf("owner changed to %s after only refused transfers", ws.OwnerID)
	}
	if r, _ := f.store.GetMemberRole(ctx, ana.ws.ID, eve.user.ID); r != "" {
		t.Errorf("the refused transfer added Eve as %q", r)
	}
	if r, _ := f.store.GetMemberRole(ctx, ana.ws.ID, ana.user.ID); r != RoleOwner {
		t.Errorf("Ana's role = %q after refused transfers, want owner", r)
	}
}

func TestTransferOwnership_StoreIsAtomic(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	eve := f.signIn("eve@corp.com", "eve-ws")
	if err := f.store.TransferOwnership(ctx, ana.ws.ID, ana.user.ID, eve.user.ID); !errors.Is(err, ErrTransferTargetNotMember) {
		t.Fatalf("err = %v, want ErrTransferTargetNotMember", err)
	}
	if ws, _ := f.store.GetWorkspace(ctx, ana.ws.ID); ws.OwnerID != ana.user.ID {
		t.Errorf("owner_id moved to %s on a rolled-back transfer", ws.OwnerID)
	}
	f.addMember(ana.ws, eve, RoleDeveloper)
	if err := f.store.TransferOwnership(ctx, ana.ws.ID, eve.user.ID, eve.user.ID); !errors.Is(err, ErrNotWorkspaceOwner) {
		t.Errorf("err = %v, want ErrNotWorkspaceOwner", err)
	}
	if r, _ := f.store.GetMemberRole(ctx, ana.ws.ID, eve.user.ID); r != RoleDeveloper {
		t.Errorf("Eve's role = %q after a refused transfer, want developer", r)
	}
}

func TestListWorkspaces_RolesAndCurrent(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(bob.ws, ana, RoleViewer)

	w := f.do(ana, http.MethodGet, "/auth/workspaces", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var list []Membership
	decodeBody(t, w, &list)
	got := map[string]Membership{}
	for _, m := range list {
		got[m.ID] = m
	}
	if len(got) != 2 {
		t.Fatalf("listed %d workspaces, want 2: %s", len(got), w.Body)
	}
	if m := got[ana.ws.ID]; m.Role != RoleOwner || !m.Current || m.MemberCount != 1 {
		t.Errorf("own workspace = %+v, want owner, current, 1 member", m)
	}
	if m := got[bob.ws.ID]; m.Role != RoleViewer || m.Current || m.MemberCount != 2 {
		t.Errorf("Bob's workspace = %+v, want viewer, not current, 2 members", m)
	}
}

func TestSwitchWorkspace_ReissuesTheSessionInTheSameFamily(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(bob.ws, ana, RoleDeveloper)
	before, _ := f.store.LookupRefreshToken(ctx, HashRefreshToken(ana.refresh))

	w := f.do(ana, http.MethodPost, "/auth/switch-workspace",
		map[string]string{"workspace_id": bob.ws.ID, "refresh_token": ana.refresh})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var resp struct {
		WorkspaceID  string `json:"workspace_id"`
		Role         Role   `json:"role"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	decodeBody(t, w, &resp)
	if resp.WorkspaceID != bob.ws.ID || resp.Role != RoleDeveloper {
		t.Errorf("switched to %s as %s, want %s as developer", resp.WorkspaceID, resp.Role, bob.ws.ID)
	}
	claims, err := ValidateJWT(resp.AccessToken)
	if err != nil || claims.WorkspaceID != bob.ws.ID || claims.Role != RoleDeveloper {
		t.Fatalf("new access token = %+v (%v), want Bob's workspace as developer", claims, err)
	}
	after, _ := f.store.LookupRefreshToken(ctx, HashRefreshToken(resp.RefreshToken))
	if after.State != RefreshValid || after.FamilyID != before.FamilyID || !after.FamilyStart.Equal(before.FamilyStart) {
		t.Errorf("new refresh token = %+v, want the same family and start as %+v", after, before)
	}
	if old, _ := f.store.LookupRefreshToken(ctx, HashRefreshToken(ana.refresh)); old.State != RefreshRevoked {
		t.Errorf("old refresh token state = %v, want revoked", old.State)
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil)
	if _, ws, role, err := f.h.landExistingUser(r, ana.user, ""); err != nil || ws != bob.ws.ID || role != RoleDeveloper {
		t.Errorf("sign-in lands in %s/%s (%v), want %s", ws, role, err, bob.ws.ID)
	}
	e, ok := f.event("auth.workspace_switched")
	if !ok {
		t.Fatal("no auth.workspace_switched audit event")
	}
	if e.WorkspaceID != bob.ws.ID || strings.Contains(e.Summary, ana.ws.ID) {
		t.Errorf("audit event = %+v, want it in %s without naming %s", e, bob.ws.ID, ana.ws.ID)
	}
}

func TestSwitchWorkspace_CookieSessionGetsNoTokensInTheBody(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(bob.ws, ana, RoleDeveloper)

	r := httptest.NewRequest(http.MethodPost, "/auth/switch-workspace",
		bytes.NewReader([]byte(`{"workspace_id":"`+bob.ws.ID+`"}`)))
	r.Header.Set("Authorization", "Bearer "+ana.access)
	r.AddCookie(&http.Cookie{Name: CookieRefresh, Value: ana.refresh})
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeBody(t, w, &resp)
	if _, leaked := resp["refresh_token"]; leaked {
		t.Error("refresh token returned in the body to a cookie session")
	}
	set := map[string]bool{}
	for _, c := range w.Result().Cookies() {
		if c.Value != "" {
			set[c.Name] = true
		}
	}
	if !set[CookieAccess] || !set[CookieRefresh] {
		t.Errorf("session cookies not re-issued: %v", set)
	}
}

func TestSwitchWorkspace_RefusesOutsidersAndForeignRefreshTokens(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")

	w := f.do(ana, http.MethodPost, "/auth/switch-workspace",
		map[string]string{"workspace_id": bob.ws.ID, "refresh_token": ana.refresh})
	if w.Code != http.StatusForbidden {
		t.Errorf("switch to a foreign workspace = %d, want 403", w.Code)
	}
	if w := f.do(ana, http.MethodPost, "/auth/switch-workspace",
		map[string]string{"workspace_id": "no-such-ws", "refresh_token": ana.refresh}); w.Code != http.StatusForbidden {
		t.Errorf("switch to a missing workspace = %d, want 403 (same as foreign)", w.Code)
	}
	if rec, _ := f.store.LookupRefreshToken(ctx, HashRefreshToken(ana.refresh)); rec.State != RefreshValid {
		t.Errorf("refused switch spent the refresh token (state %v)", rec.State)
	}

	f.addMember(bob.ws, ana, RoleViewer)
	if w := f.do(ana, http.MethodPost, "/auth/switch-workspace",
		map[string]string{"workspace_id": bob.ws.ID, "refresh_token": bob.refresh}); w.Code != http.StatusUnauthorized {
		t.Errorf("switch with another user's refresh token = %d, want 401", w.Code)
	}
	if w := f.do(ana, http.MethodPost, "/auth/switch-workspace",
		map[string]string{"workspace_id": bob.ws.ID}); w.Code != http.StatusUnauthorized {
		t.Errorf("switch without a refresh token = %d, want 401", w.Code)
	}
	if w := f.do(ana, http.MethodPost, "/auth/switch-workspace", map[string]string{}); w.Code != http.StatusBadRequest {
		t.Errorf("switch without workspace_id = %d, want 400", w.Code)
	}
}

func TestDefaultWorkspace_IgnoresALastWorkspaceTheUserLeft(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	f.addMember(bob.ws, ana, RoleDeveloper)
	if err := f.store.SetLastWorkspace(ctx, ana.user.ID, bob.ws.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveMember(ctx, bob.ws.ID, ana.user.ID); err != nil {
		t.Fatal(err)
	}
	if ws, _, err := f.store.LastWorkspace(ctx, ana.user.ID); err != nil || ws != "" {
		t.Errorf("last workspace = %q (%v), want none after she left it", ws, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil)
	_, ws, role, err := f.h.landExistingUser(r, ana.user, "")
	if err != nil || ws != ana.ws.ID || role != RoleOwner {
		t.Errorf("sign-in lands in %s/%s (%v), want her own %s", ws, role, err, ana.ws.ID)
	}
}

func TestSignIn_GroupRoleNeverFollowsTheLastWorkspace(t *testing.T) {
	f := newAccountFixture(t)
	ctx := t.Context()
	ana := f.signIn("ana@corp.com", "ana-ws")
	bob := f.signIn("bob@corp.com", "bob-ws")
	carl := f.signIn("carl@corp.com", "carl-ws")
	if err := f.store.RemoveMember(ctx, ana.ws.ID, ana.user.ID); err != nil {
		t.Fatal(err)
	}
	f.addMember(carl.ws, ana, RoleViewer)
	time.Sleep(10 * time.Millisecond) // invited_at orders the memberships
	f.addMember(bob.ws, ana, RoleViewer)
	if err := f.store.SetLastWorkspace(ctx, ana.user.ID, bob.ws.ID); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil)
	_, ws, role, err := f.h.landExistingUser(r, ana.user, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if ws != bob.ws.ID || role != RoleViewer {
		t.Errorf("landed in %s as %s, want Bob's workspace as viewer", ws, role)
	}
	if got, _ := f.store.GetMemberRole(ctx, bob.ws.ID, ana.user.ID); got != RoleViewer {
		t.Errorf("Ana's role in Bob's workspace = %q, want viewer: the group role followed her switch", got)
	}
	if got, _ := f.store.GetMemberRole(ctx, carl.ws.ID, ana.user.ID); got != RoleAdmin {
		t.Errorf("Ana's role in her first workspace = %q, want the group's admin", got)
	}
}

func TestMe_ListsIdentityProviders(t *testing.T) {
	f := newAccountFixture(t)
	ana := f.signIn("ana@corp.com", "ana-ws")
	for _, link := range [][2]string{{"example-id", "sub-1"}, {"github", "gh-1"}, {"example-id", "sub-2"}} {
		if err := f.store.LinkOIDCIdentity(t.Context(), ana.user.ID, link[0], link[1], ana.user.Email); err != nil {
			t.Fatal(err)
		}
	}
	w := f.do(ana, http.MethodGet, "/auth/me", nil)
	var resp struct {
		IdentityProviders []string `json:"identity_providers"`
	}
	decodeBody(t, w, &resp)
	if len(resp.IdentityProviders) != 2 || resp.IdentityProviders[0] != "example-id" || resp.IdentityProviders[1] != "github" {
		t.Errorf("identity_providers = %v, want [example-id github]", resp.IdentityProviders)
	}
}

func TestConfig_LegalIdentity(t *testing.T) {
	f := newAccountFixture(t)
	get := func() LegalInfo {
		t.Helper()
		w := f.do(nil, http.MethodGet, "/auth/config", nil)
		var resp struct {
			Legal LegalInfo `json:"legal"`
		}
		decodeBody(t, w, &resp)
		return resp.Legal
	}
	for _, k := range []string{"OCTOPUS_LEGAL_ENTITY", "OCTOPUS_LEGAL_CONTACT", "OCTOPUS_LEGAL_HOSTING", "OCTOPUS_LEGAL_HOSTING_REGION"} {
		t.Setenv(k, "")
	}
	if got := get(); got != (LegalInfo{}) {
		t.Errorf("unset legal identity = %+v, want empty", got)
	}
	t.Setenv("OCTOPUS_LEGAL_ENTITY", " Example SRL ")
	t.Setenv("OCTOPUS_LEGAL_CONTACT", "privacy@example.com")
	t.Setenv("OCTOPUS_LEGAL_HOSTING", "Example Cloud Inc. ")
	t.Setenv("OCTOPUS_LEGAL_HOSTING_REGION", "EU")
	want := LegalInfo{Entity: "Example SRL", Contact: "privacy@example.com", Hosting: "Example Cloud Inc.", HostingRegion: "EU"}
	if got := get(); got != want {
		t.Errorf("legal identity = %+v, want %+v", got, want)
	}
}
