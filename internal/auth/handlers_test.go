package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func newTestHandler(t *testing.T) (*auth.Handler, *auth.UserStore) {
	t.Helper()
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return auth.NewHandler(store), store
}

func postJSON(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func parseJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatalf("decode response JSON: %v (body=%q)", err, w.Body.String())
	}
	return m
}

func mustProvision(t *testing.T, h *auth.Handler, email, name string) map[string]any {
	t.Helper()
	userID, access, refresh, err := h.ProvisionSessionForTest(email, name)
	if err != nil {
		t.Fatalf("provision session for %s: %v", email, err)
	}
	return map[string]any{
		"user_id":       userID,
		"access_token":  access,
		"refresh_token": refresh,
	}
}

func TestHandler_Logout(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	tokens := mustProvision(t, h, "alice@example.com", "Alice")

	w := postJSON(t, mux, "/auth/logout", map[string]string{
		"refresh_token": tokens["refresh_token"].(string),
	})
	if w.Code != http.StatusOK {
		t.Errorf("logout: expected 200, got %d", w.Code)
	}

	w2 := postJSON(t, mux, "/auth/refresh", map[string]string{
		"refresh_token": tokens["refresh_token"].(string),
	})
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("refresh revoked token: expected 401, got %d", w2.Code)
	}
}

func TestHandler_Refresh(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	tokens := mustProvision(t, h, "alice@example.com", "Alice")

	w := postJSON(t, mux, "/auth/refresh", map[string]string{
		"refresh_token": tokens["refresh_token"].(string),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["access_token"] == nil {
		t.Error("refresh: expected new access_token")
	}
	if resp["refresh_token"] == nil {
		t.Error("refresh: expected new refresh_token")
	}
	w2 := postJSON(t, mux, "/auth/refresh", map[string]string{
		"refresh_token": tokens["refresh_token"].(string),
	})
	if w2.Code != http.StatusOK || parseJSON(t, w2)["refresh_token"] != resp["refresh_token"] {
		t.Errorf("old refresh token inside the grace window: got %d, want 200 with the same successor", w2.Code)
	}
	w3 := postJSON(t, mux, "/auth/refresh", map[string]string{"refresh_token": resp["refresh_token"].(string)})
	if w3.Code != http.StatusOK {
		t.Fatalf("refresh the successor: %d", w3.Code)
	}
	w4 := postJSON(t, mux, "/auth/refresh", map[string]string{
		"refresh_token": tokens["refresh_token"].(string),
	})
	if w4.Code != http.StatusUnauthorized {
		t.Errorf("old refresh token two rotations back: expected 401, got %d", w4.Code)
	}
}

func authedMux(mux *http.ServeMux) http.Handler {
	return auth.JWTMiddlewareWithStore(nil, mux)
}

func authedGet(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func authedPost(t *testing.T, h http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func authedPatch(t *testing.T, h http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func authedDelete(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodDelete, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandler_Config_NoUsers(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth/config", nil)
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("config: expected 200, got %d", w.Code)
	}
	resp := parseJSON(t, w)
	if resp["has_users"] != false {
		t.Errorf("has_users: want false on a fresh install, got %v", resp["has_users"])
	}
}

func TestHandler_Config_AfterFirstUser(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mustProvision(t, h, "alice@example.com", "Alice")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth/config", nil)
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("config: expected 200, got %d", w.Code)
	}
	resp := parseJSON(t, w)
	if resp["has_users"] != true {
		t.Errorf("has_users: want true after a user exists, got %v", resp["has_users"])
	}
}

func TestHandler_Config_ReportsDevMode(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("OCTOPUS_JWT_SECRET", "0123456789abcdef0123456789abcdef")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/config", nil))

	resp := parseJSON(t, w)
	if resp["dev_mode"] != true {
		t.Errorf("dev_mode: want true when OCTOPUS_DEV_MODE=1, got %v", resp["dev_mode"])
	}
	if resp["jwt_configured"] != true {
		t.Fatal("this test is not exercising the bug: it needs a JWT secret set, " +
			"which is exactly what made the old check answer \"closed\"")
	}
}

func TestHandler_Config_ReportsTheKeyCap(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	t.Setenv("OCTOPUS_API_KEY_MAX_TTL", "720h")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/config", nil))

	if got := parseJSON(t, w)["api_key_max_ttl_seconds"]; got != float64(30*24*3600) {
		t.Errorf("api_key_max_ttl_seconds = %v, want 30 days", got)
	}
}

func TestHandler_Config_DevModeOffByDefault(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	t.Setenv("OCTOPUS_DEV_MODE", "")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/config", nil))

	if resp := parseJSON(t, w); resp["dev_mode"] != false {
		t.Errorf("dev_mode: want false without OCTOPUS_DEV_MODE, got %v", resp["dev_mode"])
	}
}

func TestHandler_Me(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "alice@example.com", "Alice")
	token := tokens["access_token"].(string)

	w := authedGet(t, wrapped, "/auth/me", token)
	if w.Code != http.StatusOK {
		t.Fatalf("me: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w)
	if resp["user"] == nil {
		t.Error("me: expected user in response")
	}
	if resp["operator"] != false {
		t.Errorf("me: operator = %v with OCTOPUS_OPERATOR_WORKSPACE unset, want false", resp["operator"])
	}
	if got := resp["api_key_max_ttl_seconds"]; got != float64(90*24*60*60) {
		t.Errorf("me: api_key_max_ttl_seconds = %v, want the 90-day default", got)
	}
}

func TestHandler_Me_Operator(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY_MAX_TTL", "30d")
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	token := mustProvision(t, h, "op@example.com", "Op")["access_token"].(string)
	ws, _ := parseJSON(t, authedGet(t, wrapped, "/auth/me", token))["workspace_id"].(string)
	if ws == "" {
		t.Fatal("me: no workspace_id")
	}

	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "some-other-workspace")
	if got := parseJSON(t, authedGet(t, wrapped, "/auth/me", token))["operator"]; got != false {
		t.Errorf("owner of another workspace: operator = %v, want false", got)
	}

	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", ws)
	resp := parseJSON(t, authedGet(t, wrapped, "/auth/me", token))
	if resp["operator"] != true {
		t.Errorf("owner of the operator workspace: operator = %v, want true", resp["operator"])
	}
	if got := resp["api_key_max_ttl_seconds"]; got != float64(30*24*60*60) {
		t.Errorf("api_key_max_ttl_seconds = %v, want 30 days", got)
	}
}

func TestHandler_Me_Unauthenticated(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	w := authedGet(t, mux, "/auth/me", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("me unauthenticated: expected 401, got %d", w.Code)
	}
}

func TestHandler_Workspaces(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "alice@example.com", "Alice")
	token := tokens["access_token"].(string)

	w := authedGet(t, wrapped, "/auth/workspaces", token)
	if w.Code != http.StatusOK {
		t.Fatalf("list workspaces: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	w2 := authedPost(t, wrapped, "/auth/workspaces", map[string]string{"name": "Team WS"}, token)
	if w2.Code != http.StatusCreated {
		t.Fatalf("create workspace: expected 201, got %d body=%s", w2.Code, w2.Body.String())
	}
	ws := parseJSON(t, w2)
	if ws["id"] == nil {
		t.Error("create workspace: expected id in response")
	}
}

func TestHandler_Workspaces_MissingName(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "alice@example.com", "Alice")
	token := tokens["access_token"].(string)

	w := authedPost(t, wrapped, "/auth/workspaces", map[string]string{"name": ""}, token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing name: expected 400, got %d", w.Code)
	}
}

func TestHandler_Members(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	ownerTokens := mustProvision(t, h, "owner@example.com", "Owner")
	ownerToken := ownerTokens["access_token"].(string)

	_, _ = store.CreateUser(t.Context(), "bob@example.com", "Bob")

	wResp := authedGet(t, wrapped, "/auth/workspaces", ownerToken)
	var workspaces []map[string]any
	json.NewDecoder(wResp.Body).Decode(&workspaces)
	if len(workspaces) == 0 {
		t.Fatal("expected at least one workspace")
	}
	wsID := workspaces[0]["id"].(string)

	wList := authedGet(t, wrapped, "/auth/workspaces/"+wsID+"/members", ownerToken)
	if wList.Code != http.StatusOK {
		t.Fatalf("list members: expected 200, got %d body=%s", wList.Code, wList.Body.String())
	}

	wAdd := authedPost(t, wrapped, "/auth/workspaces/"+wsID+"/members",
		map[string]string{"email": "bob@example.com", "role": "developer"}, ownerToken)
	if wAdd.Code != http.StatusCreated {
		t.Fatalf("add member: expected 201, got %d body=%s", wAdd.Code, wAdd.Body.String())
	}

	addResp := parseJSON(t, wAdd)
	bobUserID := addResp["user_id"].(string)
	wUpdate := authedPatch(t, wrapped, "/auth/workspaces/"+wsID+"/members/"+bobUserID,
		map[string]string{"role": "admin"}, ownerToken)
	if wUpdate.Code != http.StatusOK {
		t.Fatalf("update role: expected 200, got %d body=%s", wUpdate.Code, wUpdate.Body.String())
	}

	wRemove := authedDelete(t, wrapped, "/auth/workspaces/"+wsID+"/members/"+bobUserID, ownerToken)
	if wRemove.Code != http.StatusNoContent {
		t.Fatalf("remove member: expected 204, got %d body=%s", wRemove.Code, wRemove.Body.String())
	}
}

func TestHandler_Members_CannotRemoveOwner(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	ownerTokens := mustProvision(t, h, "owner@example.com", "Owner")
	ownerToken := ownerTokens["access_token"].(string)

	wResp := authedGet(t, wrapped, "/auth/workspaces", ownerToken)
	var workspaces []map[string]any
	json.NewDecoder(wResp.Body).Decode(&workspaces)
	wsID := workspaces[0]["id"].(string)

	meResp := authedGet(t, wrapped, "/auth/me", ownerToken)
	me := parseJSON(t, meResp)
	ownerUser := me["user"].(map[string]any)
	ownerUserID := ownerUser["id"].(string)

	wRemove := authedDelete(t, wrapped, "/auth/workspaces/"+wsID+"/members/"+ownerUserID, ownerToken)
	if wRemove.Code != http.StatusBadRequest {
		t.Errorf("remove owner: expected 400, got %d", wRemove.Code)
	}
}

func TestHandler_AddMember_UnknownEmailInvites(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "owner@example.com", "Owner")
	token := tokens["access_token"].(string)

	wResp := authedGet(t, wrapped, "/auth/workspaces", token)
	var workspaces []map[string]any
	json.NewDecoder(wResp.Body).Decode(&workspaces)
	wsID := workspaces[0]["id"].(string)

	w := authedPost(t, wrapped, "/auth/workspaces/"+wsID+"/members",
		map[string]string{"email": "noexist@example.com"}, token)
	if w.Code != http.StatusCreated || parseJSON(t, w)["status"] != "invited" {
		t.Errorf("add nonexistent member: expected 201 invited, got %d %s", w.Code, w.Body)
	}
}

func TestHandler_APIKeys(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "admin@example.com", "Admin")
	token := tokens["access_token"].(string)

	wList := authedGet(t, wrapped, "/auth/api-keys", token)
	if wList.Code != http.StatusOK {
		t.Fatalf("list api-keys: expected 200, got %d body=%s", wList.Code, wList.Body.String())
	}

	wCreate := authedPost(t, wrapped, "/auth/api-keys", map[string]any{"name": "CI key", "permissions": []string{"ci"}}, token)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create api-key: expected 201, got %d body=%s", wCreate.Code, wCreate.Body.String())
	}
	created := parseJSON(t, wCreate)
	keyID := created["id"].(string)
	if created["key"] == nil {
		t.Error("create api-key: expected raw key in response")
	}

	wList2 := authedGet(t, wrapped, "/auth/api-keys", token)
	if wList2.Code != http.StatusOK {
		t.Fatalf("list api-keys (2): expected 200, got %d", wList2.Code)
	}

	wRevoke := authedDelete(t, wrapped, "/auth/api-keys/"+keyID, token)
	if wRevoke.Code != http.StatusOK {
		t.Fatalf("revoke api-key: expected 200, got %d body=%s", wRevoke.Code, wRevoke.Body.String())
	}
}

func TestHandler_APIKeys_ForbiddenWithoutAdmin(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	mustProvision(t, h, "admin@example.com", "Admin")

	w := authedGet(t, wrapped, "/auth/api-keys", "")
	if w.Code != http.StatusForbidden {
		t.Errorf("list api-keys no auth: expected 403, got %d", w.Code)
	}
}

func TestUserStore_DB_Accessor(t *testing.T) {
	_, store := newTestHandler(t)
	if store.DB() == nil {
		t.Error("DB() should not be nil")
	}
}

func TestHandler_Refresh_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	r := httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewReader([]byte("{bad")))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("refresh invalid JSON: expected 400, got %d", w.Code)
	}
}

func TestHandler_Refresh_InvalidToken(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	w := postJSON(t, mux, "/auth/refresh", map[string]string{"refresh_token": "bogus-token-xyz"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("refresh bogus token: expected 401, got %d", w.Code)
	}
}

func TestHandler_AddMember_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "owner@example.com", "Owner")
	token := tokens["access_token"].(string)

	wResp := authedGet(t, wrapped, "/auth/workspaces", token)
	var workspaces []map[string]any
	json.NewDecoder(wResp.Body).Decode(&workspaces)
	wsID := workspaces[0]["id"].(string)

	r := httptest.NewRequest(http.MethodPost, "/auth/workspaces/"+wsID+"/members", bytes.NewReader([]byte("{bad")))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("add-member invalid JSON: expected 400, got %d", w.Code)
	}
}

func TestHandler_AddMember_MissingEmail(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "owner@example.com", "Owner")
	token := tokens["access_token"].(string)

	wResp := authedGet(t, wrapped, "/auth/workspaces", token)
	var workspaces []map[string]any
	json.NewDecoder(wResp.Body).Decode(&workspaces)
	wsID := workspaces[0]["id"].(string)

	w := authedPost(t, wrapped, "/auth/workspaces/"+wsID+"/members",
		map[string]string{"email": ""}, token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("add-member missing email: expected 400, got %d", w.Code)
	}
}

func TestHandler_UpdateMemberRole_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "owner@example.com", "Owner")
	token := tokens["access_token"].(string)

	wResp := authedGet(t, wrapped, "/auth/workspaces", token)
	var workspaces []map[string]any
	json.NewDecoder(wResp.Body).Decode(&workspaces)
	wsID := workspaces[0]["id"].(string)

	r := httptest.NewRequest(http.MethodPatch, "/auth/workspaces/"+wsID+"/members/some-user",
		bytes.NewReader([]byte("{bad")))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("update-role invalid JSON: expected 400, got %d", w.Code)
	}
}

func TestHandler_UpdateMemberRole_MissingRole(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "owner@example.com", "Owner")
	token := tokens["access_token"].(string)

	wResp := authedGet(t, wrapped, "/auth/workspaces", token)
	var workspaces []map[string]any
	json.NewDecoder(wResp.Body).Decode(&workspaces)
	wsID := workspaces[0]["id"].(string)

	w := authedPatch(t, wrapped, "/auth/workspaces/"+wsID+"/members/some-user",
		map[string]string{"role": ""}, token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("update-role missing role: expected 400, got %d", w.Code)
	}
}

func TestHandler_CreateAPIKey_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "admin@example.com", "Admin")
	token := tokens["access_token"].(string)

	r := httptest.NewRequest(http.MethodPost, "/auth/api-keys", bytes.NewReader([]byte("{bad")))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("create api-key invalid JSON: expected 400, got %d", w.Code)
	}
}

func TestHandler_ListWorkspaces_Unauthenticated(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth/workspaces", nil)
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("list workspaces unauthenticated: expected 401, got %d", w.Code)
	}
}

func TestHandler_CreateWorkspace_Unauthenticated(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	b, _ := json.Marshal(map[string]string{"name": "ws"})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth/workspaces", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("create workspace unauthenticated: expected 401, got %d", w.Code)
	}
}

func TestHandler_ListMembers_Unauthenticated(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth/workspaces/any-ws-id/members", nil)
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("list members unauthenticated: expected 401, got %d", w.Code)
	}
}

func TestHandler_ListMembers_ForbiddenCrossWorkspace(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "alice@example.com", "Alice")
	token := tokens["access_token"].(string)

	w := authedGet(t, wrapped, "/auth/workspaces/nonexistent-workspace-id/members", token)
	if w.Code != http.StatusForbidden {
		t.Errorf("list members cross-workspace: expected 403, got %d", w.Code)
	}
}

func TestHandler_UpdateMemberRole_Unauthenticated(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	b, _ := json.Marshal(map[string]string{"role": "admin"})
	r := httptest.NewRequest(http.MethodPatch, "/auth/workspaces/ws-id/members/user-id", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("update-role unauthenticated: expected 401, got %d", w.Code)
	}
}

func TestHandler_RemoveMember_Unauthenticated(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	r := httptest.NewRequest(http.MethodDelete, "/auth/workspaces/ws-id/members/user-id", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("remove-member unauthenticated: expected 401, got %d", w.Code)
	}
}

func TestHandler_Me_InvalidToken(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	r := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	r.Header.Set("Authorization", "Bearer invalid.token.here")
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("me with invalid token: expected 401, got %d", w.Code)
	}
}

func TestHandler_Refresh_Expired(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	w := postJSON(t, mux, "/auth/refresh", map[string]string{
		"refresh_token": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("refresh with nonexistent token: expected 401, got %d", w.Code)
	}
}

func TestHandler_Logout_WithJWT(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "jwtlogout@example.com", "User")
	token := tokens["access_token"].(string)

	w := authedPost(t, wrapped, "/auth/logout", map[string]string{
		"refresh_token": tokens["refresh_token"].(string),
	}, token)
	if w.Code != http.StatusOK {
		t.Errorf("logout with JWT: expected 200, got %d", w.Code)
	}
}

func TestHandler_ListAPIKeys_DBError(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "listapidb@example.com", "User")
	token := tokens["access_token"].(string)
	store.DB().Close()

	w := authedGet(t, wrapped, "/auth/api-keys", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("list api-keys DB error: expected 500, got %d", w.Code)
	}
}

func TestHandler_CreateAPIKey_DBError(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "createapidb@example.com", "User")
	token := tokens["access_token"].(string)
	store.DB().Close()

	w := authedPost(t, wrapped, "/auth/api-keys", map[string]any{"name": "ci", "permissions": []string{"ci"}}, token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("create api-key DB error: expected 500, got %d", w.Code)
	}
}

func TestHandler_RevokeAPIKey_DBError(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "revokeapidb@example.com", "User")
	token := tokens["access_token"].(string)

	wCreate := authedPost(t, wrapped, "/auth/api-keys", map[string]any{"name": "test", "permissions": []string{"read-only"}}, token)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create api-key: expected 201, got %d body=%s", wCreate.Code, wCreate.Body.String())
	}
	keyID := parseJSON(t, wCreate)["id"].(string)

	store.DB().Close()
	w := authedDelete(t, wrapped, "/auth/api-keys/"+keyID, token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("revoke api-key DB error: expected 500, got %d", w.Code)
	}
}

func TestHandler_ListWorkspaces_DBError(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "listwsdb@example.com", "User")
	token := tokens["access_token"].(string)
	store.DB().Close()

	w := authedGet(t, wrapped, "/auth/workspaces", token)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("list workspaces DB error: expected 500, got %d", w.Code)
	}
}

func TestHandler_CreateWorkspace_InvalidJSON(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "ws-json@test.com", "User")
	token := tokens["access_token"].(string)

	req := httptest.NewRequest(http.MethodPost, "/auth/workspaces", bytes.NewReader([]byte("{invalid")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("create workspace invalid JSON: expected 400, got %d", w.Code)
	}
}

func TestHandler_HandleMe_DBClosed(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "me-dbclosed@test.com", "User")
	token := tokens["access_token"].(string)

	store.DB().Close()
	w := authedGet(t, wrapped, "/auth/me", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET /auth/me with closed DB: expected 404, got %d", w.Code)
	}
}

func TestHandler_ChangePassword_DBClosed(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "chpw-dbclosed@test.com", "User")
	token := tokens["access_token"].(string)

	store.DB().Close()
	w := authedPost(t, wrapped, "/auth/me/password", map[string]string{
		"current_password": "Password1234",
		"new_password":     "newpassword123",
	}, token)
	if w.Code != http.StatusNotFound {
		t.Errorf("change password DB closed: expected 404, got %d", w.Code)
	}
}

func TestHandler_Refresh_UserDeletedAfterToken(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "deleted@example.com", "Gone User")
	refreshToken := tokens["refresh_token"].(string)
	userID := tokens["user"]

	_ = userID
	if _, err := store.DB().Exec(`DELETE FROM workspace_members WHERE workspace_id IN (SELECT id FROM workspaces WHERE owner_id IN (SELECT id FROM users WHERE email = $1))`, "deleted@example.com"); err != nil {
		t.Fatalf("delete memberships: %v", err)
	}
	if _, err := store.DB().Exec(`DELETE FROM workspaces WHERE owner_id IN (SELECT id FROM users WHERE email = $1)`, "deleted@example.com"); err != nil {
		t.Fatalf("delete workspaces: %v", err)
	}
	if _, err := store.DB().Exec(`DELETE FROM users WHERE email = $1`, "deleted@example.com"); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	w := postJSON(t, wrapped, "/auth/refresh", map[string]string{"refresh_token": refreshToken})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("refresh after user deleted: expected 401, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_RemoveMember_DBError(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "rm-dberr@test.com", "User")
	token := tokens["access_token"].(string)

	store.DB().Close()

	req := httptest.NewRequest(http.MethodDelete, "/auth/workspaces/any-ws-id/members/some-user-id", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("remove member DB closed: expected 403 (fail-closed membership check), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_UpdateMemberRole_DBError(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	tokens := mustProvision(t, h, "upr-dberr@test.com", "User")
	token := tokens["access_token"].(string)

	store.DB().Close()

	b, _ := json.Marshal(map[string]string{"role": "developer"})
	req := httptest.NewRequest(http.MethodPatch, "/auth/workspaces/any-ws-id/members/some-user-id", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("update member role DB closed: expected 403 (fail-closed membership check), got %d body=%s", w.Code, w.Body.String())
	}
}

func getFirstWorkspaceID(t *testing.T, wrapped http.Handler, token string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/auth/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list workspaces: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var wsList []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&wsList); err != nil {
		t.Fatalf("decode workspaces: %v", err)
	}
	if len(wsList) == 0 {
		t.Fatal("expected at least one workspace")
	}
	return wsList[0]["id"].(string)
}

func TestHandler_AddMember_AddMemberDBError(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	aliceTokens := mustProvision(t, h, "alice-amdb@example.com", "Alice")
	aliceToken := aliceTokens["access_token"].(string)
	wsID := getFirstWorkspaceID(t, wrapped, aliceToken)

	if _, err := store.CreateUser(t.Context(), "bob-amdb@example.com", "Bob"); err != nil {
		t.Fatalf("create bob: %v", err)
	}

	store.DB().Close()

	addBody, _ := json.Marshal(map[string]string{"email": "bob-amdb@example.com", "role": "developer"})
	req := httptest.NewRequest(http.MethodPost, "/auth/workspaces/"+wsID+"/members", bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("add member DB closed: expected 403 (fail-closed membership check), got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandler_AddMember_DefaultRole(t *testing.T) {
	h, store := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	aliceTokens := mustProvision(t, h, "alice-dm@example.com", "Alice")
	aliceToken := aliceTokens["access_token"].(string)
	wsID := getFirstWorkspaceID(t, wrapped, aliceToken)

	if _, err := store.CreateUser(t.Context(), "bob-dm@example.com", "Bob"); err != nil {
		t.Fatalf("create bob: %v", err)
	}

	addBody, _ := json.Marshal(map[string]string{"email": "bob-dm@example.com"})
	addReq := httptest.NewRequest(http.MethodPost, "/auth/workspaces/"+wsID+"/members", bytes.NewReader(addBody))
	addReq.Header.Set("Content-Type", "application/json")
	addReq.Header.Set("Authorization", "Bearer "+aliceToken)
	wAdd := httptest.NewRecorder()
	wrapped.ServeHTTP(wAdd, addReq)
	if wAdd.Code != http.StatusCreated {
		t.Errorf("add member no role: expected 201, got %d body=%s", wAdd.Code, wAdd.Body.String())
	}
}

func TestHandler_AddMember_SeatCapEnforced(t *testing.T) {
	h, store := newTestHandler(t)
	h.WithSeatLimit(func(string) int { return 1 })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	wrapped := authedMux(mux)

	aliceTokens := mustProvision(t, h, "alice-seat@example.com", "Alice")
	aliceToken := aliceTokens["access_token"].(string)
	wsID := getFirstWorkspaceID(t, wrapped, aliceToken)

	if _, err := store.CreateUser(t.Context(), "bob-seat@example.com", "Bob"); err != nil {
		t.Fatalf("create bob: %v", err)
	}

	addBody, _ := json.Marshal(map[string]string{"email": "bob-seat@example.com", "role": "developer"})
	req := httptest.NewRequest(http.MethodPost, "/auth/workspaces/"+wsID+"/members", bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusPaymentRequired {
		t.Errorf("seat cap should block with 402, got %d body=%s", w.Code, w.Body.String())
	}
}
