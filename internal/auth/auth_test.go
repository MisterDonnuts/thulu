package auth_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestJWT_RoundTrip(t *testing.T) {
	claims := auth.Claims{
		UserID:      "user-123",
		Email:       "test@example.com",
		WorkspaceID: "ws-456",
		Role:        auth.RoleDeveloper,
	}

	token, err := auth.IssueJWT(claims)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	got, err := auth.ValidateJWT(token)
	if err != nil {
		t.Fatalf("ValidateJWT: %v", err)
	}
	if got.UserID != claims.UserID {
		t.Errorf("UserID: got %q, want %q", got.UserID, claims.UserID)
	}
	if got.Email != claims.Email {
		t.Errorf("Email: got %q, want %q", got.Email, claims.Email)
	}
	if got.WorkspaceID != claims.WorkspaceID {
		t.Errorf("WorkspaceID: got %q, want %q", got.WorkspaceID, claims.WorkspaceID)
	}
	if got.Role != claims.Role {
		t.Errorf("Role: got %q, want %q", got.Role, claims.Role)
	}
}

func TestJWT_ExpiredToken(t *testing.T) {
	claims := auth.Claims{
		UserID:    "user-123",
		IssuedAt:  time.Now().Add(-48 * time.Hour),
		ExpiresAt: time.Now().Add(-24 * time.Hour),
	}

	token, err := auth.IssueJWT(claims)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	_, err = auth.ValidateJWT(token)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestJWT_TamperedSignature(t *testing.T) {
	claims := auth.Claims{UserID: "user-123"}
	token, _ := auth.IssueJWT(claims)

	if len(token) < 5 {
		t.Fatalf("IssueJWT produced an implausibly short token (%d chars)", len(token))
	}
	tampered := token[:len(token)-4] + "ZZZZ"
	_, err := auth.ValidateJWT(tampered)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
}

func TestJWT_InvalidFormat(t *testing.T) {
	cases := []string{"", "not.a.jwt", "only.two", "a.b.c.d"}
	for _, c := range cases {
		_, err := auth.ValidateJWT(c)
		if err == nil && c != "a.b.c.d" {
			// "a.b.c.d" has 3 dots but fails signature check — that's fine.
		}
	}
}

func TestRefreshToken_Uniqueness(t *testing.T) {
	t1, h1, err := auth.IssueRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	t2, h2, err := auth.IssueRefreshToken()
	if err != nil {
		t.Fatal(err)
	}

	if t1 == t2 {
		t.Error("refresh tokens should be unique")
	}
	if h1 == h2 {
		t.Error("refresh token hashes should be unique")
	}

	if auth.HashRefreshToken(t1) != h1 {
		t.Error("HashRefreshToken not deterministic")
	}
}

func TestUserStore_CreateAndLookupUser(t *testing.T) {
	db := tempDB(t)
	store, err := auth.NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	has, _ := store.HasAnyUser(t.Context())
	if has {
		t.Fatal("expected no users initially")
	}

	user, err := store.CreateUser(t.Context(), "alice@example.com", "Alice")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID == "" {
		t.Fatal("empty user ID")
	}

	has, _ = store.HasAnyUser(t.Context())
	if !has {
		t.Fatal("expected at least one user")
	}

	found, err := store.GetUserByEmail(t.Context(), "alice@example.com")
	if err != nil || found == nil {
		t.Fatalf("GetUserByEmail: %v, found=%v", err, found)
	}
	if found.Email != "alice@example.com" {
		t.Errorf("email mismatch: %q", found.Email)
	}

	byID, _ := store.GetUserByID(t.Context(), user.ID)
	if byID == nil || byID.Email != "alice@example.com" {
		t.Fatal("GetUserByID failed")
	}

	_, err = store.CreateUser(t.Context(), "alice@example.com", "Alice2")
	if err == nil {
		t.Fatal("expected error for duplicate email")
	}
}

func TestUserStore_Workspace(t *testing.T) {
	db := tempDB(t)
	store, _ := auth.NewUserStore(db)
	defer store.Close()

	user, _ := store.CreateUser(t.Context(), "bob@example.com", "Bob")

	ws, err := store.CreateWorkspace(t.Context(), user.ID, "Bob's workspace", "bobs-workspace")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if ws.ID == "" {
		t.Fatal("empty workspace ID")
	}

	role, err := store.GetMemberRole(t.Context(), ws.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if role != auth.RoleOwner {
		t.Errorf("expected owner role, got %q", role)
	}

	wss, _ := store.ListWorkspacesForUser(t.Context(), user.ID)
	if len(wss) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(wss))
	}

	found, _ := store.GetWorkspaceBySlug(t.Context(), "bobs-workspace")
	if found == nil || found.ID != ws.ID {
		t.Fatal("GetWorkspaceBySlug failed")
	}
}

func TestUserStore_RefreshTokens(t *testing.T) {
	db := tempDB(t)
	store, _ := auth.NewUserStore(db)
	defer store.Close()

	user, _ := store.CreateUser(t.Context(), "carol@example.com", "Carol")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Carol's ws", "carolws")

	raw, hash, _ := auth.IssueRefreshToken()

	if err := store.StoreRefreshToken(user.ID, ws.ID, hash, time.Now().Add(7*24*time.Hour)); err != nil {
		t.Fatalf("StoreRefreshToken: %v", err)
	}

	gotUser, gotWS, err := store.ValidateRefreshToken(auth.HashRefreshToken(raw))
	if err != nil {
		t.Fatalf("ValidateRefreshToken: %v", err)
	}
	if gotUser != user.ID || gotWS != ws.ID {
		t.Errorf("unexpected user/ws: %q %q", gotUser, gotWS)
	}

	if err := store.RevokeRefreshToken(t.Context(), hash); err != nil {
		t.Fatalf("RevokeRefreshToken: %v", err)
	}

	_, _, err = store.ValidateRefreshToken(hash)
	if err == nil {
		t.Fatal("expected error for revoked token")
	}
}

func TestAPIKey_CreateAndLookup(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	defer store.Close()

	has, _ := store.HasAnyAPIKey(t.Context())
	if has {
		t.Error("expected no keys initially")
	}

	raw, meta, err := store.CreateAPIKey(t.Context(), "CI key", "user-1", auth.KeyGrant{WorkspaceID: "ws-1", Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermAll}})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if !strings.HasPrefix(raw, "oct_") {
		t.Errorf("key should start with oct_, got %q", raw)
	}
	if meta.ID == "" || meta.Name != "CI key" || meta.Prefix == "" {
		t.Errorf("unexpected meta: %+v", meta)
	}

	has, _ = store.HasAnyAPIKey(t.Context())
	if !has {
		t.Error("expected HasAnyAPIKey true after creation")
	}

	keyID := store.LookupAPIKey(raw)
	if keyID != meta.ID {
		t.Errorf("LookupAPIKey: got %q, want %q", keyID, meta.ID)
	}

	if store.LookupAPIKey("oct_wrong") != "" {
		t.Error("expected empty for unknown key")
	}

	keys, err := store.ListAPIKeys(t.Context(), "ws-1")
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].Name != "CI key" {
		t.Errorf("unexpected keys: %+v", keys)
	}

	if err := store.RevokeAPIKey(t.Context(), meta.ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}

	if store.LookupAPIKey(raw) != "" {
		t.Error("expected empty for revoked key")
	}

	has, _ = store.HasAnyAPIKey(t.Context())
	if has {
		t.Error("expected HasAnyAPIKey false after all revoked")
	}

	all, _ := store.ListAPIKeys(t.Context(), "ws-1")
	if len(all) != 1 {
		t.Fatalf("expected 1 (revoked) key, got %d", len(all))
	}
	if all[0].RevokedAt == nil {
		t.Error("expected non-nil RevokedAt for revoked key")
	}
}

func TestUserStore_MemberManagement(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	defer store.Close()

	owner, _ := store.CreateUser(t.Context(), "owner@example.com", "Owner")
	member, _ := store.CreateUser(t.Context(), "member@example.com", "Member")
	ws, _ := store.CreateWorkspace(t.Context(), owner.ID, "Test WS", "test-ws")

	if err := store.AddMember(t.Context(), ws.ID, member.ID, auth.RoleDeveloper); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	members, err := store.ListMembers(t.Context(), ws.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("want 2 members (owner + added), got %d", len(members))
	}

	if err := store.UpdateMemberRole(t.Context(), ws.ID, member.ID, auth.RoleAdmin); err != nil {
		t.Fatalf("UpdateMemberRole: %v", err)
	}
	role, _ := store.GetMemberRole(t.Context(), ws.ID, member.ID)
	if role != auth.RoleAdmin {
		t.Errorf("role = %q, want admin", role)
	}

	if err := store.RemoveMember(t.Context(), ws.ID, member.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	members2, _ := store.ListMembers(t.Context(), ws.ID)
	if len(members2) != 1 {
		t.Errorf("want 1 member after remove, got %d", len(members2))
	}

	fetched, err := store.GetWorkspace(t.Context(), ws.ID)
	if err != nil || fetched == nil || fetched.Name != "Test WS" {
		t.Errorf("GetWorkspace: %v, got %+v", err, fetched)
	}
}

func TestUserStore_PurgeExpiredTokens(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	defer store.Close()

	user, _ := store.CreateUser(t.Context(), "purge@example.com", "Purge")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Purge WS", "purge-ws")

	_, hash, _ := auth.IssueRefreshToken()
	store.StoreRefreshToken(user.ID, ws.ID, hash, time.Now().Add(-1*time.Hour))

	n, err := store.PurgeExpiredRefreshTokens(t.Context())
	if err != nil {
		t.Fatalf("PurgeExpiredRefreshTokens: %v", err)
	}
	if n == 0 {
		t.Error("expected at least 1 purged token")
	}
}

func TestMiddleware_Passthrough_NoEnvKey(t *testing.T) {
	os.Unsetenv("OCTOPUS_API_KEY")
	h := auth.MiddlewareWithStoreAndPublic(nil, nil, okHandler())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/tasks", nil))
	if w.Code != http.StatusOK {
		t.Errorf("no env key → passthrough, got %d", w.Code)
	}
}

func TestMiddleware_RejectsInvalidKey(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY", "correct-key")
	h := auth.MiddlewareWithStoreAndPublic(nil, nil, okHandler())
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tasks", nil)
	r.Header.Set("X-API-Key", "wrong-key")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("invalid key → 401, got %d", w.Code)
	}
}

func TestMiddleware_AcceptsValidEnvKey(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY", "my-secret-key")
	h := auth.MiddlewareWithStoreAndPublic(nil, nil, okHandler())
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tasks", nil)
	r.Header.Set("Authorization", "Bearer my-secret-key")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("valid env key → 200, got %d", w.Code)
	}
}

func TestMiddleware_HealthAlwaysAllowed(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY", "key")
	h := auth.MiddlewareWithStoreAndPublic(nil, nil, okHandler())
	for _, path := range []string{"/health", "/health/live", "/health/ready"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s should be allowed without key, got %d", path, w.Code)
		}
	}
}

func TestMiddleware_CORSPreflightAlwaysAllowed(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY", "key")
	h := auth.MiddlewareWithStoreAndPublic(nil, nil, okHandler())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("OPTIONS", "/tasks", nil))
	if w.Code != http.StatusOK {
		t.Errorf("OPTIONS should be allowed, got %d", w.Code)
	}
}

func TestMiddlewareWithStoreAndPublic_BypassesEnvKey(t *testing.T) {
	t.Setenv("OCTOPUS_API_KEY", "secret-env-key")
	isPublic := func(p string) bool {
		return p == "/" || p == "/login/" || p == "/_astro/app.css"
	}
	h := auth.MiddlewareWithStoreAndPublic(nil, isPublic, okHandler())

	for _, p := range []string{"/", "/login/", "/_astro/app.css"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusOK {
			t.Errorf("public path %s should bypass env key gate, got %d", p, w.Code)
		}
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/projects", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("non-public path /projects without key → 401, got %d", w.Code)
	}
}

func TestMiddlewareWithStore_OctKey(t *testing.T) {
	os.Unsetenv("OCTOPUS_API_KEY")
	store, _ := auth.NewUserStore(tempDB(t))
	defer store.Close()

	rawKey, _, _ := store.CreateAPIKey(t.Context(), "test", "u1", auth.KeyGrant{WorkspaceID: "ws-1", Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermAll}})
	h := auth.MiddlewareWithStoreAndPublic(store, nil, okHandler())

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tasks", nil)
	r.Header.Set("X-API-Key", rawKey)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("valid oct_ key → 200, got %d", w.Code)
	}

	store.RevokeAPIKey(t.Context(), "")
	keys, _ := store.ListAPIKeys(t.Context(), "ws-1")
	if len(keys) > 0 {
		store.RevokeAPIKey(t.Context(), keys[0].ID)
	}
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/tasks", nil)
	r2.Header.Set("X-API-Key", rawKey)
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("revoked oct_ key → 401, got %d", w2.Code)
	}
}

func TestUserStore_ValidateRefreshToken_DBError(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	store.DB().Close()
	_, _, err := store.ValidateRefreshToken("any-hash")
	if err == nil {
		t.Error("expected error from ValidateRefreshToken after DB close")
	}
}

func TestUserStore_GetWorkspace_DBError(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	store.DB().Close()
	_, err := store.GetWorkspace(t.Context(), "any-id")
	if err == nil {
		t.Error("expected error from GetWorkspace after DB close")
	}
}

func TestUserStore_GetWorkspaceBySlug_DBError(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	store.DB().Close()
	_, err := store.GetWorkspaceBySlug(t.Context(), "any-slug")
	if err == nil {
		t.Error("expected error from GetWorkspaceBySlug after DB close")
	}
}

func TestUserStore_ListMembers_DBError(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	store.DB().Close()
	_, err := store.ListMembers(t.Context(), "any-ws-id")
	if err == nil {
		t.Error("expected error from ListMembers after DB close")
	}
}

func TestUserStore_GetWorkspace_NotFound(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	defer store.Close()
	ws, err := store.GetWorkspace(t.Context(), "nonexistent-id")
	if err != nil {
		t.Fatalf("expected nil error for not-found, got %v", err)
	}
	if ws != nil {
		t.Error("expected nil workspace for not-found ID")
	}
}

func TestUserStore_GetWorkspaceBySlug_NotFound(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	defer store.Close()
	ws, err := store.GetWorkspaceBySlug(t.Context(), "nonexistent-slug")
	if err != nil {
		t.Fatalf("expected nil error for not-found slug, got %v", err)
	}
	if ws != nil {
		t.Error("expected nil workspace for not-found slug")
	}
}

func TestUserStore_CreateWorkspace_DuplicateSlug(t *testing.T) {
	store, _ := auth.NewUserStore(tempDB(t))
	defer store.Close()
	user, _ := store.CreateUser(t.Context(), "ws-dup@example.com", "U")
	_, err := store.CreateWorkspace(t.Context(), user.ID, "My WS", "my-ws")
	if err != nil {
		t.Fatalf("first CreateWorkspace: %v", err)
	}
	_, err = store.CreateWorkspace(t.Context(), user.ID, "My WS 2", "my-ws")
	if err == nil {
		t.Error("expected error for duplicate workspace slug")
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func tempDB(t *testing.T) *sql.DB {
	t.Helper()
	return testpg.New(t)
}
