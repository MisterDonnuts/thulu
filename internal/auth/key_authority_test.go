package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type keyAuthorityFixture struct {
	h         *Handler
	mux       *http.ServeMux
	store     *UserStore
	wsA, wsB  string
	bystander *User
}

func newKeyAuthorityFixture(t *testing.T) *keyAuthorityFixture {
	t.Helper()
	store := newSSOTestStore(t)
	ctx := t.Context()
	ownerA, err := store.CreateUser(ctx, "owner-a@corp.com", "Owner A")
	if err != nil {
		t.Fatal(err)
	}
	ownerB, err := store.CreateUser(ctx, "owner-b@corp.com", "Owner B")
	if err != nil {
		t.Fatal(err)
	}
	wsA, err := store.CreateWorkspace(ctx, ownerA.ID, "A", "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	wsB, err := store.CreateWorkspace(ctx, ownerB.ID, "B", "ws-b")
	if err != nil {
		t.Fatal(err)
	}
	by, err := store.CreateUser(ctx, "bystander@corp.com", "Bystander")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &keyAuthorityFixture{h: h, mux: mux, store: store, wsA: wsA.ID, wsB: wsB.ID, bystander: by}
}

func (f *keyAuthorityFixture) asKey(t *testing.T, key APIKeyInfo, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if key.ID == "" {
		key.ID = "k-test"
	}
	r = r.WithContext(withKeyAuth(r.Context(), &key))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func TestMemberManagement_KeyMustBelongHaveRoleAndPermission(t *testing.T) {
	f := newKeyAuthorityFixture(t)
	add := map[string]string{"email": f.bystander.Email, "role": "admin"}
	pathA := "/auth/workspaces/" + f.wsA + "/members"

	attacks := []struct {
		name string
		key  APIKeyInfo
	}{
		{"key from another workspace", APIKeyInfo{WorkspaceID: f.wsB, Role: RoleOwner, Permissions: []Permission{PermAll}}},
		{"key without a workspace", APIKeyInfo{Role: RoleOwner, Permissions: []Permission{PermAll}}},
		{"viewer key with members:write", APIKeyInfo{WorkspaceID: f.wsA, Role: RoleViewer, Permissions: []Permission{PermMembersWrite}}},
		{"admin key without members:write", APIKeyInfo{WorkspaceID: f.wsA, Role: RoleAdmin, Permissions: []Permission{PermTasksWrite, PermMembersRead}}},
		{"chat run key", APIKeyInfo{WorkspaceID: f.wsA, Role: RoleOwner, Permissions: []Permission{PermAll},
			Scope: RunScope{ChatID: "c1", Agent: "helper"}}},
		{"task run key", APIKeyInfo{WorkspaceID: f.wsA, Role: RoleOwner, Permissions: []Permission{PermAll},
			Scope: RunScope{TaskID: "t1", Agent: "coder", Stage: "dev"}}},
	}
	for _, a := range attacks {
		if w := f.asKey(t, a.key, http.MethodPost, pathA, add); w.Code != http.StatusForbidden {
			t.Errorf("%s: add member = %d, want 403 (%s)", a.name, w.Code, w.Body.String())
		}
	}
	if role, _ := f.store.GetMemberRole(t.Context(), f.wsA, f.bystander.ID); role != "" {
		t.Fatalf("an attack succeeded: bystander is %q in A", role)
	}

	legit := APIKeyInfo{WorkspaceID: f.wsA, Role: RoleAdmin, Permissions: []Permission{PermMembersWrite}}
	if w := f.asKey(t, legit, http.MethodPost, pathA, map[string]string{"email": f.bystander.Email, "role": "developer"}); w.Code != http.StatusCreated {
		t.Fatalf("admin key with members:write: %d %s", w.Code, w.Body.String())
	}

	memberPath := pathA + "/" + f.bystander.ID
	viewerWrite := APIKeyInfo{WorkspaceID: f.wsA, Role: RoleViewer, Permissions: []Permission{PermMembersWrite}}
	if w := f.asKey(t, viewerWrite, http.MethodPatch, memberPath, map[string]string{"role": "admin"}); w.Code != http.StatusForbidden {
		t.Errorf("viewer key changed a role: %d", w.Code)
	}
	otherWS := APIKeyInfo{WorkspaceID: f.wsB, Role: RoleAdmin, Permissions: []Permission{PermMembersWrite}}
	if w := f.asKey(t, otherWS, http.MethodDelete, memberPath, nil); w.Code != http.StatusForbidden {
		t.Errorf("key from B removed a member of A: %d", w.Code)
	}
	if w := f.asKey(t, legit, http.MethodDelete, memberPath, nil); w.Code != http.StatusNoContent {
		t.Errorf("legit removal: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberList_NeedsMembersRead(t *testing.T) {
	f := newKeyAuthorityFixture(t)
	path := "/auth/workspaces/" + f.wsA + "/members"

	if w := f.asKey(t, APIKeyInfo{WorkspaceID: f.wsA, Role: RoleViewer, Permissions: []Permission{PermTasksRead}}, http.MethodGet, path, nil); w.Code != http.StatusForbidden {
		t.Errorf("key without members:read listed members: %d", w.Code)
	}
	if w := f.asKey(t, APIKeyInfo{WorkspaceID: f.wsB, Role: RoleViewer, Permissions: []Permission{PermMembersRead}}, http.MethodGet, path, nil); w.Code != http.StatusForbidden {
		t.Errorf("key from B listed A's members: %d", w.Code)
	}
	if w := f.asKey(t, APIKeyInfo{WorkspaceID: f.wsA, Role: RoleViewer, Permissions: []Permission{PermMembersRead}}, http.MethodGet, path, nil); w.Code != http.StatusOK {
		t.Errorf("viewer key with members:read: %d %s", w.Code, w.Body.String())
	}
}

func TestKeyMint_PermissionsMustBeSubsetOfCaller(t *testing.T) {
	f := newKeyAuthorityFixture(t)
	minter := APIKeyInfo{WorkspaceID: f.wsA, Role: RoleAdmin, Permissions: []Permission{PermKeysWrite, PermTasksRead}}

	for _, perms := range [][]string{{"tasks:write"}, {"credentials:read"}, {"*"}, {"full"}} {
		w := f.asKey(t, minter, http.MethodPost, "/auth/api-keys", map[string]any{"name": "child", "role": "developer", "permissions": perms})
		if w.Code != http.StatusBadRequest {
			t.Errorf("minted a key with %v from a key holding keys:write,tasks:read: %d %s", perms, w.Code, w.Body.String())
		}
	}
	w := f.asKey(t, minter, http.MethodPost, "/auth/api-keys", map[string]any{"name": "child", "role": "viewer", "permissions": []string{"tasks:read"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("subset mint refused: %d %s", w.Code, w.Body.String())
	}

	noKeys := APIKeyInfo{WorkspaceID: f.wsA, Role: RoleAdmin, Permissions: []Permission{PermTasksWrite}}
	if w := f.asKey(t, noKeys, http.MethodPost, "/auth/api-keys", map[string]any{"permissions": []string{"tasks:read"}}); w.Code != http.StatusForbidden {
		t.Errorf("key without keys:write minted: %d", w.Code)
	}
	run := APIKeyInfo{WorkspaceID: f.wsA, Role: RoleOwner, Permissions: []Permission{PermAll}, Scope: RunScope{TaskID: "t1", Agent: "coder"}}
	if w := f.asKey(t, run, http.MethodPost, "/auth/api-keys", map[string]any{"permissions": []string{"tasks:read"}}); w.Code != http.StatusForbidden {
		t.Errorf("run key minted: %d", w.Code)
	}
	if w := f.asKey(t, run, http.MethodGet, "/auth/api-keys", nil); w.Code != http.StatusForbidden {
		t.Errorf("run key listed the workspace's keys: %d", w.Code)
	}
}

func TestKeyMint_ChildNeverOutlivesParent(t *testing.T) {
	f := newKeyAuthorityFixture(t)
	parentExp := time.Now().Add(10 * 24 * time.Hour)
	parent := APIKeyInfo{WorkspaceID: f.wsA, Role: RoleAdmin, Permissions: []Permission{PermAll}, ExpiresAt: &parentExp}

	w := f.asKey(t, parent, http.MethodPost, "/auth/api-keys", map[string]any{"permissions": []string{"tasks:read"}, "expires_in_days": 30})
	if w.Code != http.StatusBadRequest {
		t.Errorf("child asked to outlive its parent: %d %s", w.Code, w.Body.String())
	}

	w = f.asKey(t, parent, http.MethodPost, "/auth/api-keys", map[string]any{"permissions": []string{"tasks:read"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("child without explicit expiry: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got.ExpiresAt == nil || got.ExpiresAt.After(parentExp.Add(time.Minute)) {
		t.Errorf("child expires %v, parent %v", got.ExpiresAt, parentExp)
	}

	expired := time.Now().Add(-time.Hour)
	dead := APIKeyInfo{WorkspaceID: f.wsA, Role: RoleAdmin, Permissions: []Permission{PermAll}, ExpiresAt: &expired}
	if w := f.asKey(t, dead, http.MethodPost, "/auth/api-keys", map[string]any{"permissions": []string{"tasks:read"}}); w.Code != http.StatusBadRequest {
		t.Errorf("expired parent minted: %d", w.Code)
	}
}

func TestCreateAPIKey_MaxTTL(t *testing.T) {
	store := newSSOTestStore(t)
	ctx := t.Context()
	u, err := store.CreateUser(ctx, "cli@corp.com", "CLI")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.CreateWorkspace(ctx, u.ID, "CLI", "cli")
	if err != nil {
		t.Fatal(err)
	}
	grant := func(d time.Duration) KeyGrant {
		return KeyGrant{WorkspaceID: ws.ID, Role: RoleAdmin, Permissions: []Permission{PermAll}, ExpiresIn: d}
	}

	t.Setenv("OCTOPUS_API_KEY_MAX_TTL", "")
	_, meta, err := store.CreateAPIKey(ctx, "default", u.ID, grant(0))
	if err != nil {
		t.Fatal(err)
	}
	if meta.ExpiresAt == nil {
		t.Fatal("key without a requested TTL never expires; want the 90d default")
	}
	if d := time.Until(*meta.ExpiresAt); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Errorf("default expiry in %s, want ~90d", d)
	}
	if _, _, err := store.CreateAPIKey(ctx, "too long", u.ID, grant(200*24*time.Hour)); !errors.Is(err, ErrKeyTTLTooLong) {
		t.Errorf("200d key: err=%v, want ErrKeyTTLTooLong", err)
	}

	t.Setenv("OCTOPUS_API_KEY_MAX_TTL", "30d")
	_, _, err = store.CreateAPIKey(ctx, "31d", u.ID, grant(31*24*time.Hour))
	if !errors.Is(err, ErrKeyTTLTooLong) {
		t.Errorf("31d under a 30d cap: err=%v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "the maximum is 30 days") {
		t.Errorf("31d refusal does not name the cap in days: %v", err)
	}

	t.Setenv("OCTOPUS_API_KEY_MAX_TTL", "0")
	if _, meta, err := store.CreateAPIKey(ctx, "forever", u.ID, grant(0)); err != nil || meta.ExpiresAt != nil {
		t.Errorf("MAX_TTL=0: meta=%+v err=%v, want no expiry", meta, err)
	}
}

func TestCreateWorkspace_GatedWhenPersonalWorkspacesOff(t *testing.T) {
	f := newKeyAuthorityFixture(t)
	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "0")
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", f.wsA)

	n := 0
	post := func(c *Claims) int {
		n++
		r := httptest.NewRequest(http.MethodPost, "/auth/workspaces",
			bytes.NewBufferString(`{"name":"Tenant `+string(rune('a'+n))+`"}`))
		r = r.WithContext(WithClaims(r.Context(), c))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w.Code
	}

	if code := post(&Claims{UserID: f.bystander.ID, Email: f.bystander.Email}); code != http.StatusForbidden {
		t.Errorf("non-operator created a workspace: %d", code)
	}
	if err := f.store.AddMember(t.Context(), f.wsA, f.bystander.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if code := post(&Claims{UserID: f.bystander.ID, Email: f.bystander.Email}); code != http.StatusCreated {
		t.Errorf("operator admin could not create a workspace: %d", code)
	}

	t.Setenv("OCTOPUS_ALLOW_PERSONAL_WORKSPACES", "1")
	u2, err := f.store.CreateUser(t.Context(), "selfserve@corp.com", "Self Serve")
	if err != nil {
		t.Fatal(err)
	}
	if code := post(&Claims{UserID: u2.ID, Email: u2.Email}); code != http.StatusCreated {
		t.Errorf("self-serve install refused a workspace: %d", code)
	}
}

func TestAPIKeyMaxTTL_Parse(t *testing.T) {
	cases := map[string]time.Duration{
		"":        90 * 24 * time.Hour,
		"0":       0,
		"30d":     30 * 24 * time.Hour,
		"720h":    720 * time.Hour,
		"garbage": 90 * 24 * time.Hour,
		"-5d":     90 * 24 * time.Hour,
	}
	for raw, want := range cases {
		t.Setenv("OCTOPUS_API_KEY_MAX_TTL", raw)
		if got := APIKeyMaxTTL(); got != want {
			t.Errorf("%q: got %s, want %s", raw, got, want)
		}
	}
}
