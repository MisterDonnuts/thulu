package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestAPIKey_CannotReachAnotherWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "key-a@test.com", "A")
	tokenB, _ := mintWorkspaceUser(t, srv, "key-b@test.com", "B")

	taskA := taskIDFor(t, h, tokenA, "A's task")
	taskB := taskIDFor(t, h, tokenB, "B's task")

	keyA := mintKey(t, srv, wsA, auth.RoleAdmin, auth.PermAll)

	if w := getWithKey(t, h, "/tasks/"+taskA, keyA); w.Code != http.StatusOK {
		t.Fatalf("A's key on A's task = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := getWithKey(t, h, "/tasks/"+taskB, keyA); w.Code != http.StatusNotFound {
		t.Errorf("A's key on B's task = %d, want 404: %s", w.Code, w.Body.String())
	}
	w := getWithKey(t, h, "/tasks", keyA)
	if w.Code != http.StatusOK {
		t.Fatalf("list with A's key = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), taskB) {
		t.Error("A's key listed B's task")
	}
}

func TestAPIKey_PermissionListIsEnforced(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "key-perms@test.com", "P")
	taskID := taskIDFor(t, h, token, "readable")

	readOnly := mintKey(t, srv, ws, auth.RoleAdmin, auth.PermTasksRead)

	if w := getWithKey(t, h, "/tasks/"+taskID, readOnly); w.Code != http.StatusOK {
		t.Fatalf("tasks:read key reading a task = %d, want 200: %s", w.Code, w.Body.String())
	}
	w := patchWithKey(t, h, "/tasks/"+taskID, readOnly, map[string]any{"title": "renamed"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("tasks:read key writing a task = %d, want 403: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), string(auth.PermTasksWrite)) {
		t.Errorf("refusal should name tasks:write, got %s", w.Body.String())
	}
	if w := getWithKey(t, h, "/credentials", readOnly); w.Code != http.StatusForbidden {
		t.Errorf("tasks:read key reading credentials = %d, want 403", w.Code)
	}
}

func TestAPIKey_MintCannotExceedTheCallersRole(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "key-mint@test.com", "M", "SecurePassword1")

	adminToken := reissueAs(t, token, auth.RoleAdmin)

	w := postJSONAuth(t, h, "/auth/api-keys", adminToken, map[string]any{
		"name": "escalation", "role": "owner", "permissions": []string{"full"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("admin minting an owner key = %d, want 400: %s", w.Code, w.Body.String())
	}
	ok := postJSONAuth(t, h, "/auth/api-keys", adminToken, map[string]any{
		"name": "fine", "role": "developer", "permissions": []string{"ci"},
	})
	if ok.Code != http.StatusCreated {
		t.Fatalf("admin minting a developer key = %d, want 201: %s", ok.Code, ok.Body.String())
	}
	var created map[string]any
	json.NewDecoder(ok.Body).Decode(&created)
	if created["workspace_id"] == "" || created["workspace_id"] == nil {
		t.Error("mint response must echo the workspace the key belongs to")
	}
}

func TestAPIKey_CannotMintWithoutKeysWrite(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	_, ws := mintWorkspaceUser(t, srv, "key-nomint@test.com", "NM")
	narrow := mintKey(t, srv, ws, auth.RoleOwner, auth.PermTasksRead, auth.PermTasksWrite)

	body, _ := json.Marshal(map[string]any{"name": "second", "permissions": []string{"full"}})
	req := httptest.NewRequest(http.MethodPost, "/auth/api-keys", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", narrow)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("minting with a key that lacks keys:write = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func mintKey(t *testing.T, srv *Server, ws string, role auth.Role, perms ...auth.Permission) string {
	t.Helper()
	raw, _, err := srv.authStore.CreateAPIKey(t.Context(), "test key", "", auth.KeyGrant{
		WorkspaceID: ws, Role: role, Permissions: perms,
	})
	if err != nil {
		t.Fatalf("mint key for %s: %v", ws, err)
	}
	return raw
}

func taskIDFor(t *testing.T, h http.Handler, token, title string) string {
	t.Helper()
	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": title})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task %q = %d: %s", title, w.Code, w.Body.String())
	}
	var created map[string]any
	json.NewDecoder(w.Body).Decode(&created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create task %q returned no id", title)
	}
	return id
}

func getWithKey(t *testing.T, h http.Handler, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func patchWithKey(t *testing.T, h http.Handler, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func reissueAs(t *testing.T, token string, role auth.Role) string {
	t.Helper()
	claims, err := auth.ValidateJWT(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	claims.Role = role
	out, err := auth.IssueJWT(*claims)
	if err != nil {
		t.Fatalf("reissue token: %v", err)
	}
	return out
}

func TestAPIKey_ListAndRevokeStayInsideTheWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "key-list-a@test.com", "LA")
	tokenB, wsB := mintWorkspaceUser(t, srv, "key-list-b@test.com", "LB")

	_, metaB, err := srv.authStore.CreateAPIKey(t.Context(), "B's integration", "", auth.KeyGrant{
		WorkspaceID: wsB, Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermTasksRead},
	})
	if err != nil {
		t.Fatalf("mint B's key: %v", err)
	}
	if _, _, err := srv.authStore.CreateAPIKey(t.Context(), "A's integration", "", auth.KeyGrant{
		WorkspaceID: wsA, Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermTasksRead},
	}); err != nil {
		t.Fatalf("mint A's key: %v", err)
	}

	list := getJSON(t, h, "/auth/api-keys", tokenA)
	if list.Code != http.StatusOK {
		t.Fatalf("A listing keys = %d: %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), "B's integration") {
		t.Error("A's key list included B's key")
	}
	if !strings.Contains(list.Body.String(), "A's integration") {
		t.Error("A's key list is missing A's own key")
	}

	if w := deleteRequest(t, h, "/auth/api-keys/"+metaB.ID, tokenA); w.Code != http.StatusNotFound {
		t.Errorf("A revoking B's key = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := deleteRequest(t, h, "/auth/api-keys/"+metaB.ID, tokenB); w.Code != http.StatusOK {
		t.Errorf("B revoking B's key = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// Key-management routes check the permission themselves, outside RequireRole; the path they check
// must map to a resource group or every key, wildcard included, gets 403.
func TestAPIKey_WildcardKeyCanManageKeys(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	_, ws := mintWorkspaceUser(t, srv, "key-mgmt@test.com", "KM")
	full := mintKey(t, srv, ws, auth.RoleOwner, auth.PermAll)

	_, victim, err := srv.authStore.CreateAPIKey(t.Context(), "to be revoked", "", auth.KeyGrant{
		WorkspaceID: ws, Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermTasksRead},
	})
	if err != nil {
		t.Fatalf("mint victim: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"name": "minted by a key", "permissions": []string{"ci"}})
	mint := httptest.NewRequest(http.MethodPost, "/auth/api-keys", strings.NewReader(string(body)))
	mint.Header.Set("Content-Type", "application/json")
	mint.Header.Set("X-API-Key", full)
	mw := httptest.NewRecorder()
	h.ServeHTTP(mw, mint)
	if mw.Code != http.StatusCreated {
		t.Fatalf("wildcard key minting = %d, want 201: %s", mw.Code, mw.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/auth/api-keys/"+victim.ID, nil)
	del.Header.Set("X-API-Key", full)
	dw := httptest.NewRecorder()
	h.ServeHTTP(dw, del)
	if dw.Code != http.StatusOK {
		t.Fatalf("wildcard key revoking = %d, want 200: %s", dw.Code, dw.Body.String())
	}
}
