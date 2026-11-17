package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/middleware"
)

type Handler struct {
	store          *UserStore
	auditFn        AuditFn
	ssoProviders   map[string]*ssoProvider
	authRL         *middleware.RateLimiter
	seatLimitFn    func(workspaceID string) int
	grace          *refreshGrace
	purgeWorkspace func(ctx context.Context, workspaceID string) error
	taskTitles     func(ctx context.Context, taskIDs []string) map[string]string
	stageLabels    func(ctx context.Context, taskID, stage string) string
	eventFn        func(kind, result string)
}

func NewHandler(store *UserStore) *Handler {
	return &Handler{store: store, authRL: middleware.NewFixedRateLimiter(0.17, 20), grace: newRefreshGrace()}
}

func (h *Handler) WithSSO(providers map[string]*ssoProvider) *Handler {
	h.ssoProviders = providers
	return h
}

func (h *Handler) WithAudit(fn AuditFn) *Handler {
	h.auditFn = fn
	return h
}

// A callback so the host CLI, which imports auth to mint keys, does not link the Prometheus client.
func (h *Handler) WithEventCounter(fn func(kind, result string)) *Handler {
	h.eventFn = fn
	return h
}

func (h *Handler) countEvent(kind, result string) {
	if h.eventFn != nil {
		h.eventFn(kind, result)
	}
}

func (h *Handler) WithSeatLimit(fn func(workspaceID string) int) *Handler {
	h.seatLimitFn = fn
	return h
}

// Fails open on lookup errors so billing limits never lock admins out.
func (h *Handler) seatCapReached(ctx context.Context, wsID, userID string) bool {
	if h.seatLimit(wsID) == 0 {
		return false
	}
	if role, err := h.store.GetMemberRole(ctx, wsID, userID); err == nil && role != "" {
		return false
	}
	s, err := h.seats(ctx, wsID)
	return err == nil && s.Full()
}

func (h *Handler) rl(fn http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(h.authRL.Handler(fn).ServeHTTP)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/config", h.handleConfig)
	mux.HandleFunc("POST /auth/refresh", h.rl(h.handleRefresh))
	mux.HandleFunc("POST /auth/logout", h.rl(h.handleLogout))
	mux.HandleFunc("GET /auth/me", h.handleMe)
	mux.HandleFunc("DELETE /auth/me", h.handleDeleteAccount)
	mux.HandleFunc("GET /auth/sessions", h.handleListSessions)
	mux.HandleFunc("DELETE /auth/sessions/{familyId}", h.handleRevokeSession)
	mux.HandleFunc("POST /auth/sessions/revoke-all", h.handleRevokeAllSessions)
	mux.HandleFunc("GET /auth/workspaces", h.handleListWorkspaces)
	mux.HandleFunc("POST /auth/workspaces", h.handleCreateWorkspace)
	mux.HandleFunc("POST /auth/switch-workspace", h.handleSwitchWorkspace)
	mux.HandleFunc("POST /auth/workspaces/{id}/transfer", h.handleTransferOwnership)
	mux.HandleFunc("PATCH /auth/workspaces/{id}", h.handleRenameWorkspace)
	mux.HandleFunc("GET /auth/workspaces/{id}/seats", h.handleWorkspaceSeats)
	mux.HandleFunc("GET /auth/workspaces/{id}/license-seats", h.handleLicenseSeats)
	mux.HandleFunc("GET /auth/workspaces/{id}/members", h.handleListMembers)
	mux.HandleFunc("POST /auth/workspaces/{id}/members", h.handleAddMember)
	mux.HandleFunc("PATCH /auth/workspaces/{id}/members/{userId}", h.handleUpdateMemberRole)
	mux.HandleFunc("DELETE /auth/workspaces/{id}/members/{userId}", h.handleRemoveMember)
	mux.HandleFunc("GET /auth/workspaces/{id}/invitations", h.handleListInvitations)
	mux.HandleFunc("PATCH /auth/workspaces/{id}/invitations/{invitationId}", h.handleUpdateInvitation)
	mux.HandleFunc("DELETE /auth/workspaces/{id}/invitations/{invitationId}", h.handleDeleteInvitation)
	mux.HandleFunc("GET /auth/api-keys", h.handleListAPIKeys)
	mux.HandleFunc("POST /auth/api-keys", h.handleCreateAPIKey)
	mux.HandleFunc("DELETE /auth/api-keys/{id}", h.handleRevokeAPIKey)
	mux.HandleFunc("POST /auth/api-keys/{id}/rotate", h.handleRotateAPIKey)
	mux.HandleFunc("GET /auth/run-keys", h.handleListRunKeys)
	mux.HandleFunc("GET /auth/signup-allowlist", h.handleListSignupAllowlist)
	mux.HandleFunc("POST /auth/signup-allowlist", h.handleAddSignupAllowlist)
	mux.HandleFunc("DELETE /auth/signup-allowlist/{email}", h.handleRemoveSignupAllowlist)
	mux.HandleFunc("DELETE /auth/signup-requests/{email}", h.handleDismissSignupRequest)
	mux.HandleFunc("GET /auth/login-config", h.handleLoginConfig)
	mux.HandleFunc("POST /auth/access-request", h.rl(h.handleAccessRequest))
	mux.HandleFunc("GET /auth/invitation", h.rl(h.handleInvitationPreview))
	mux.HandleFunc("GET /auth/oidc/authorize", h.rl(h.handleOIDCAuthorize))
	mux.HandleFunc("GET /auth/oidc/callback", h.rl(h.handleOIDCCallback))
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	hasUser, err := h.store.HasAnyUser(r.Context())
	logx.Warn(err, "auth.log.config_has_user")
	jwtConfigured := os.Getenv("OCTOPUS_JWT_SECRET") != ""
	apiKeyConfigured := os.Getenv("OCTOPUS_API_KEY") != ""
	if !apiKeyConfigured {
		var apiKeyErr error
		apiKeyConfigured, apiKeyErr = h.store.HasAnyAPIKey(r.Context())
		logx.Warn(apiKeyErr, "auth.log.config_has_api_key")
	}
	ssoList := make([]SSOProviderInfo, 0, len(h.ssoProviders))
	for _, p := range h.ssoProviders {
		ssoList = append(ssoList, p.info)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"has_users":          hasUser,
		"jwt_configured":     jwtConfigured,
		"api_key_configured": apiKeyConfigured,
		"sso_providers":      ssoList,
		// Clients must read dev_mode, not infer it from jwt_configured/api_key_configured: a
		// no-login install has both set.
		"dev_mode":                DevMode(),
		"dev_session":             DevSession(r.Context()),
		"api_key_max_ttl_seconds": int64(APIKeyMaxTTL() / time.Second),
		"legal":                   legalInfo(),
		"signup_mode":             SignupMode(),
		"saas":                    SaaSMode(),
		"waitlist_url":            strings.TrimSpace(os.Getenv("OCTOPUS_WAITLIST_URL")),
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func refreshTokenFromRequest(r *http.Request) (tok string, fromBody, badJSON bool) {
	var req refreshRequest
	decodeErr := json.NewDecoder(r.Body).Decode(&req)
	tok = req.RefreshToken
	if tok != "" {
		return tok, true, false
	}
	if ck, ckErr := r.Cookie(CookieRefresh); ckErr == nil {
		tok = ck.Value
	}
	if tok == "" && decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		return "", false, true
	}
	return tok, false, false
}

// Carries no tokens: returning them for a cookie refresh would let any page script that can call
// /auth/refresh read them.
type refreshResponse struct {
	ExpiresAt time.Time `json:"expires_at"`
	TokenType string    `json:"token_type"`
}

// A revoked token presented again is a replay of a rotated token (theft signal): burn the whole
// family.
func (h *Handler) rejectBadRefresh(w http.ResponseWriter, r *http.Request, rec *RefreshTokenRecord) bool {
	switch {
	case rec.State == RefreshRevoked:
		logx.Warn(h.store.RevokeRefreshTokenFamily(r.Context(), rec.FamilyID), "auth.log.refresh_reuse_revoke_family")
		logx.Warn(h.store.BumpTokenVersion(r.Context(), rec.UserID), "auth.log.refresh_reuse_bump_version")
		h.auditUser(r, "auth.refresh_reuse_detected", rec.UserID, "", rec.WorkspaceID,
			i18n.Text("auth.audit.refresh_reuse_detected"))
		h.countEvent("refresh", "fail")
		ClearAuthCookies(w, requestIsSecure(r))
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.refresh_token_invalid"))
	case rec.State != RefreshValid:
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.refresh_token_invalid"))
	case !rec.FamilyStart.IsZero() && time.Since(rec.FamilyStart) > SessionMaxLifetime():
		logx.Warn(h.store.RevokeRefreshTokenFamily(r.Context(), rec.FamilyID), "auth.log.revoke_family_past_lifetime")
		ClearAuthCookies(w, requestIsSecure(r))
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.session_expired"))
	default:
		return false
	}
	return true
}

func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	refreshTok, fromBody, badJSON := refreshTokenFromRequest(r)
	if refreshTok == "" {
		if badJSON {
			apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
			return
		}
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.refresh_token_missing"))
		return
	}

	hash := HashRefreshToken(refreshTok)
	unlock := h.grace.lock(hash)
	defer unlock()
	rec, lookupErr := h.store.LookupRefreshToken(r.Context(), hash)
	if lookupErr != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	// Grace for a token another tab just rotated: only this exact token and only while its
	// successor is live; older replays still burn the family.
	if rec.State == RefreshRevoked {
		if tokens := h.grace.successor(r.Context(), h.store, hash); tokens != nil {
			h.writeRefreshed(w, r, tokens, fromBody)
			return
		}
	}
	if h.rejectBadRefresh(w, r, rec) {
		return
	}
	userID, wsID := rec.UserID, rec.WorkspaceID

	user, err := h.store.GetUserByID(r.Context(), userID)
	if err != nil || user == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.user_not_found"))
		return
	}

	// Fail closed: an evicted member or a DB error must not refresh into a default role.
	role, roleErr := h.store.GetMemberRole(r.Context(), wsID, userID)
	if roleErr != nil || role == "" {
		logx.Warn(roleErr, "auth.log.refresh_member_role", "user", userID, "workspace", wsID)
		logx.Warn(h.store.RevokeRefreshToken(r.Context(), hash), "auth.log.revoke_refresh_non_member")
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_member"))
		return
	}

	tokens, err := h.rotateTokenPair(r.Context(), user, wsID, role, hash, rec.FamilyID, rec.FamilyStart, clientDevice(r))
	if errors.Is(err, ErrRefreshAlreadyRotated) {
		// Another replica rotated it between our lookup and update; refuse without minting a second
		// successor.
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.refresh_token_invalid"))
		return
	}
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	h.grace.remember(hash, tokens)
	h.writeRefreshed(w, r, tokens, fromBody)
}

func (h *Handler) writeRefreshed(w http.ResponseWriter, r *http.Request, tokens *tokenResponse, fromBody bool) {
	if cookieErr := setSessionCookies(w, r, tokens); cookieErr != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	h.countEvent("refresh", "ok")
	w.Header().Set("Content-Type", "application/json")
	if !fromBody {
		json.NewEncoder(w).Encode(refreshResponse{ExpiresAt: tokens.ExpiresAt, TokenType: tokens.TokenType})
		return
	}
	json.NewEncoder(w).Encode(tokens) //nolint:gosec // G117: access_token is intentionally returned to the authenticating client on refresh
}

func SessionMaxLifetime() time.Duration {
	const def = 720 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("OCTOPUS_SESSION_MAX_LIFETIME")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		log.Printf("[auth] %s", i18n.Log("auth.log.session_max_lifetime_invalid", "default", def))
	}
	return def
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	refreshTok, _, _ := refreshTokenFromRequest(r)
	if refreshTok != "" {
		logx.Warn(h.store.RevokeRefreshToken(r.Context(), HashRefreshToken(refreshTok)), "auth.log.logout_revoke")
	}
	ClearAuthCookies(w, requestIsSecure(r))
	if c := ClaimsFromContext(r.Context()); c != nil {
		h.auditRequest(r, "auth.logout", c.WorkspaceID, c.UserID, "user", i18n.Text("auth.audit.logged_out", "email", c.Email))
	}
	resp := map[string]string{"status": "ok"}
	if u := h.idpLogoutURL(frontendOrigin(r)); u != "" {
		resp["logout_url"] = u
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return
	}
	user, err := h.store.GetUserByID(r.Context(), claims.UserID)
	if err != nil || user == nil {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.user_not_found"))
		return
	}
	providers, err := h.store.IdentityProviders(r.Context(), user.ID)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	block := h.createWorkspaceBlock(r.Context(), claims)
	wsName := ""
	if ws, wsErr := h.store.GetWorkspace(r.Context(), claims.WorkspaceID); wsErr == nil && ws != nil {
		wsName = ws.Name
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"user":         user,
		"workspace_id": claims.WorkspaceID,
		"role":         claims.Role,
		// Must mirror callerIsOperator in the api package. Display only: the server still checks
		// every write.
		"operator":                sessionIsOperator(claims),
		"api_key_max_ttl_seconds": int64(APIKeyMaxTTL() / time.Second),
		"identity_providers":      providers,
		"can_create_workspace":    block.Reason == "",
		"create_workspace_reason": block.Reason,
		"create_workspace_max":    block.Max,
		"workspace_name":          wsName,
	})
}

func sessionIsOperator(c *Claims) bool {
	op := strings.TrimSpace(os.Getenv("OCTOPUS_OPERATOR_WORKSPACE"))
	return op != "" && c.WorkspaceID == op && roleAtLeast(c.Role, RoleAdmin)
}

func (h *Handler) currentSessionFamily(r *http.Request) string {
	tok := ""
	if ck, err := r.Cookie(CookieRefresh); err == nil {
		tok = ck.Value
	}
	if tok == "" {
		return ""
	}
	fam, err := h.store.RefreshFamilyForHash(r.Context(), HashRefreshToken(tok))
	logx.Warn(err, "auth.log.resolve_current_family")
	return fam
}

func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return
	}
	sessions, err := h.store.ListSessionFamilies(r.Context(), claims.UserID)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	current := h.currentSessionFamily(r)
	for i := range sessions {
		if sessions[i].FamilyID == current {
			sessions[i].IsCurrent = true
		}
	}
	if sessions == nil {
		sessions = []SessionFamily{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (h *Handler) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return
	}
	familyID := r.PathValue("familyId")
	owner, err := h.store.RefreshFamilyOwner(r.Context(), familyID)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if owner == "" || owner != claims.UserID {
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.session_not_found"))
		return
	}
	if err := h.store.RevokeRefreshTokenFamily(r.Context(), familyID); err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if familyID == h.currentSessionFamily(r) {
		ClearAuthCookies(w, requestIsSecure(r))
	}
	h.auditRequest(r, "auth.session_revoked", claims.WorkspaceID, claims.UserID, "user",
		i18n.Text("auth.audit.session_revoked", "family", familyID))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) handleRevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return
	}

	current := h.currentSessionFamily(r)

	if err := h.store.RevokeAllRefreshFamiliesExcept(r.Context(), claims.UserID, current); err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if err := h.store.BumpTokenVersion(r.Context(), claims.UserID); err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	// The version bump also killed the caller's access token, so re-issue for this device.
	// Tokens go in the body only for Authorization-header callers, never cookie sessions.
	_, cookieSession := bearerCredential(r)
	resp := map[string]any{"status": "ok"}
	if user, err := h.store.GetUserByID(r.Context(), claims.UserID); err == nil && user != nil {
		if current != "" {
			logx.Warn(h.store.RevokeRefreshTokenFamily(r.Context(), current), "auth.log.revoke_superseded_family")
		}
		if tokens, issueErr := h.issueTokenPair(r.Context(), user, claims.WorkspaceID, claims.Role, clientDevice(r)); issueErr == nil {
			logx.Warn(setSessionCookies(w, r, tokens), "auth.log.revoke_all_set_cookies")
			resp["expires_at"] = tokens.ExpiresAt
			resp["token_type"] = tokens.TokenType
			if !cookieSession {
				resp["access_token"] = tokens.AccessToken
				resp["refresh_token"] = tokens.RefreshToken
			}
		}
	}

	h.auditRequest(r, "auth.sessions_revoked_all", claims.WorkspaceID, claims.UserID, "user",
		i18n.Text("auth.audit.sessions_revoked_all"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return
	}
	wss, err := h.store.ListMemberships(r.Context(), claims.UserID)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	for i := range wss {
		wss[i].Current = wss[i].ID == claims.WorkspaceID
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wss)
}

type createWorkspaceRequest struct {
	Name string `json:"name"`
}

func (h *Handler) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return
	}
	if !h.mayCreateWorkspace(r.Context(), claims) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.workspace_creation_reserved"))
		return
	}

	var req createWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.workspace_name_required"))
		return
	}

	slug := slugify(name)
	if slug == "" {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.workspace_name_no_slug"))
		return
	}

	maxOwned := MaxOwnedWorkspaces()
	if maxOwned > 0 && h.isOperatorAdmin(r.Context(), claims.UserID) {
		maxOwned = 0
	}
	ws, err := h.store.CreateOwnedWorkspace(r.Context(), claims.UserID, name, slug, maxOwned)
	if errors.Is(err, ErrWorkspaceCapReached) {
		apperr.WriteJSON(w, r, http.StatusForbidden, err)
		return
	}
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.create_workspace_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ws)
}

func (h *Handler) mayCreateWorkspace(ctx context.Context, c *Claims) bool {
	if PersonalWorkspacesAllowed() {
		return true
	}
	return h.isOperatorAdmin(ctx, c.UserID)
}

func (h *Handler) isOperatorAdmin(ctx context.Context, userID string) bool {
	op := strings.TrimSpace(os.Getenv("OCTOPUS_OPERATOR_WORKSPACE"))
	if op == "" {
		return false
	}
	role, err := h.store.GetMemberRole(ctx, op, userID)
	return err == nil && roleAtLeast(role, RoleAdmin)
}

// Checks the caller's role in the target workspace, not the JWT's.
// API keys must pass all four checks; each was once missing and let any key make itself admin
// anywhere.
func (h *Handler) requireWorkspaceRole(w http.ResponseWriter, r *http.Request, wsID string,
	minRole Role, perm Permission) bool {
	ctx := r.Context()
	if IsAPIKeyAuthed(ctx) {
		switch {
		case APIKeyRunScope(ctx).IsRun():
			apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.run_key_cannot_manage_members"))
		case APIKeyWorkspace(ctx) == "" || APIKeyWorkspace(ctx) != wsID:
			apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.key_other_workspace"))
		case !roleAtLeast(APIKeyRole(ctx), minRole):
			apperr.WriteJSON(w, r, http.StatusForbidden,
				apperr.Forbidden("auth.errors.key_role_too_low", "role", APIKeyRole(ctx), "need", minRole))
		case !APIKeyGrants(ctx, perm):
			apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.key_lacks_permission", "permission", perm))
		default:
			return true
		}
		return false
	}
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.not_authenticated"))
		return false
	}
	role, err := h.store.GetMemberRole(ctx, wsID, claims.UserID)
	if err != nil || role == "" || !roleAtLeast(role, minRole) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.workspace_role_required", "role", minRole))
		return false
	}
	return true
}

func (h *Handler) handleListMembers(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleViewer, PermMembersRead) {
		return
	}
	members, err := h.store.ListMembers(r.Context(), wsID)
	if err != nil {
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}

type addMemberRequest struct {
	Email string `json:"email"`
	Role  Role   `json:"role"`
}

func (h *Handler) handleAddMember(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	if !h.requireWorkspaceRole(w, r, wsID, RoleAdmin, PermMembersWrite) {
		return
	}

	var req addMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.member_email_required"))
		return
	}
	if req.Role == "" {
		req.Role = RoleDeveloper
	}
	if !isKnownRole(req.Role) {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.member_role_invalid", "role", req.Role))
		return
	}
	// workspaces.owner_id names the only owner: adding an owner is only a hand-over from the
	// `thulu init` placeholder (owner_handoff.go).
	if req.Role == RoleOwner {
		if err := h.callerMayGrantOwner(r, wsID); err != nil {
			apperr.WriteJSON(w, r, apperr.Status(err), err)
			return
		}
	}

	user, err := h.store.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		logx.Warn(err, "auth.log.add_member_lookup", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if user != nil && req.Role == RoleOwner {
		h.handOwnerTo(w, r, wsID, user)
		return
	}
	if user == nil {
		h.invite(w, r, wsID, req.Email, req.Role)
		return
	}
	// Add upserts, so an admin could demote the owner through it: the owner's row is fixed.
	ws, wsErr := h.store.GetWorkspace(r.Context(), wsID)
	if wsErr != nil || ws == nil {
		logx.Warn(wsErr, "auth.log.add_member_get_workspace", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.workspace_not_found"))
		return
	}
	if user.ID == ws.OwnerID {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.owner_membership_fixed"))
		return
	}
	prevRole, prevErr := h.store.GetMemberRole(r.Context(), wsID, user.ID)
	logx.Warn(prevErr, "auth.log.add_member_prior_role", "workspace", wsID)

	if h.seatCapReached(r.Context(), wsID, user.ID) {
		apperr.WriteJSON(w, r, http.StatusPaymentRequired, apperr.Forbidden("auth.errors.seat_limit_reached"))
		return
	}

	if err := h.store.AddMember(r.Context(), wsID, user.ID, req.Role); err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.add_member_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	logx.Warn(h.store.deleteInvitationFor(r.Context(), wsID, user.Email), "auth.log.invitation_delete_failed", "workspace", wsID)
	// Re-adding with another role is a role change: revoke tokens now, as PATCH does.
	if prevRole != "" && prevRole != req.Role {
		logx.Warn(h.store.RevokeAllRefreshTokensForUser(r.Context(), user.ID), "auth.log.role_change_revoke_refresh")
		logx.Warn(h.store.BumpTokenVersion(r.Context(), user.ID), "auth.log.role_change_bump_version")
	}

	h.auditRequest(r, "workspace.member_added", wsID, user.ID, "user",
		i18n.Text("auth.audit.member_added", "email", req.Email, "role", req.Role))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "user_id": user.ID})
}

type updateMemberRoleRequest struct {
	Role Role `json:"role"`
}

func (h *Handler) handleUpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	userID := r.PathValue("userId")
	if !h.requireWorkspaceRole(w, r, wsID, RoleAdmin, PermMembersWrite) {
		return
	}

	var req updateMemberRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	if req.Role == "" {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.member_role_required"))
		return
	}
	if !isKnownRole(req.Role) {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.member_role_invalid", "role", req.Role))
		return
	}

	// Owner changes only via transfer: granting owner here makes an owner workspaces.owner_id
	// doesn't know, and the owner's own row is fixed.
	// A failed workspace lookup refuses, so a DB hiccup can't let an admin demote the owner.
	if req.Role == RoleOwner {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.owner_not_grantable_on_update"))
		return
	}
	ws, wsErr := h.store.GetWorkspace(r.Context(), wsID)
	if wsErr != nil || ws == nil {
		logx.Warn(wsErr, "auth.log.update_member_role_get_workspace", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.workspace_not_found"))
		return
	}
	if userID == ws.OwnerID {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.owner_role_fixed"))
		return
	}

	err := h.store.UpdateMemberRole(r.Context(), wsID, userID, req.Role)
	if errors.Is(err, ErrNotAMember) {
		apperr.WriteJSON(w, r, http.StatusNotFound, err)
		return
	}
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.update_member_role_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	logx.Warn(h.store.RevokeAllRefreshTokensForUser(r.Context(), userID), "auth.log.role_change_revoke_refresh")
	logx.Warn(h.store.BumpTokenVersion(r.Context(), userID), "auth.log.role_change_bump_version")

	h.auditRequest(r, "workspace.member_role_changed", wsID, userID, "user",
		i18n.Text("auth.audit.member_role_changed", "user", h.userLabel(r.Context(), userID), "role", req.Role))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("id")
	userID := r.PathValue("userId")
	if !h.requireWorkspaceRole(w, r, wsID, RoleAdmin, PermMembersWrite) {
		return
	}

	ws, wsErr := h.store.GetWorkspace(r.Context(), wsID)
	if wsErr != nil || ws == nil {
		logx.Warn(wsErr, "auth.log.remove_member_get_workspace", "workspace", wsID)
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.workspace_not_found"))
		return
	}
	if ws.OwnerID == userID {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.owner_cannot_be_removed"))
		return
	}

	// RemoveMember must fail before the revokes below: a non-member is not this admin's to sign
	// out.
	err := h.store.RemoveMember(r.Context(), wsID, userID)
	if errors.Is(err, ErrNotAMember) {
		apperr.WriteJSON(w, r, http.StatusNotFound, err)
		return
	}
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.remove_member_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	logx.Warn(h.store.RevokeAllRefreshTokensForUser(r.Context(), userID), "auth.log.removal_revoke_refresh")
	logx.Warn(h.store.BumpTokenVersion(r.Context(), userID), "auth.log.removal_bump_version")

	h.auditRequest(r, "workspace.member_removed", wsID, userID, "user",
		i18n.Text("auth.audit.member_removed", "user", h.userLabel(r.Context(), userID)))
	if r.URL.Query().Get("revoke_keys") == "1" {
		h.revokeRemovedMemberKeys(r, wsID, userID)
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !h.isAdminOrAPIKey(r) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.admin_or_key_required"))
		return
	}
	if reason := keyManagementDenial(r); reason != nil {
		apperr.WriteJSON(w, r, http.StatusForbidden, reason)
		return
	}
	_, ws, _, ok := h.keyCaller(w, r)
	if !ok {
		return
	}
	keys, err := h.store.ListAPIKeys(r.Context(), ws)
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.list_api_keys_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.withCreatorEmails(r.Context(), keys))
}

type createAPIKeyRequest struct {
	Name          string   `json:"name"`
	ExpiresInDays int      `json:"expires_in_days"`
	Role          string   `json:"role"`
	Permissions   []string `json:"permissions"`
}

func (h *Handler) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if !h.isAdminOrAPIKey(r) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.admin_or_key_required"))
		return
	}
	if reason := keyManagementDenial(r); reason != nil {
		apperr.WriteJSON(w, r, http.StatusForbidden, reason)
		return
	}

	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.invalid_json"))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "API Key"
	}

	creatorID, callerWS, callerRole, ok := h.keyCaller(w, r)
	if !ok {
		return
	}

	grant, gerr := resolveGrant(req, callerWS, callerRole, callerPermissions(r))
	if gerr == nil {
		gerr = capToParentExpiry(&grant, req.ExpiresInDays > 0, apiKeyExpiry(r.Context()))
	}
	if gerr != nil {
		apperr.WriteJSON(w, r, http.StatusBadRequest, gerr)
		return
	}

	rawKey, meta, err := h.store.CreateAPIKey(r.Context(), req.Name, creatorID, grant)
	if err != nil {
		if errors.Is(err, ErrKeyNeedsWorkspace) ||
			errors.Is(err, ErrKeyNeedsPermissions) ||
			errors.Is(err, ErrPermissionsAboveRole) ||
			errors.Is(err, ErrKeyTTLTooLong) {
			apperr.WriteJSON(w, r, http.StatusBadRequest, err)
			return
		}
		log.Printf("[auth] %s", i18n.Log("auth.log.create_api_key_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}

	h.auditRequest(r, "auth.api_key_created", meta.WorkspaceID, meta.ID, "api_key",
		i18n.Text("auth.audit.api_key_created", "name", meta.Name, "role", meta.Role,
			"permissions", EncodePermissions(meta.Permissions)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":           meta.ID,
		"name":         meta.Name,
		"prefix":       meta.Prefix,
		"key":          rawKey,
		"created_at":   meta.CreatedAt,
		"expires_at":   meta.ExpiresAt,
		"workspace_id": meta.WorkspaceID,
		"role":         meta.Role,
		"permissions":  meta.Permissions,
	})
}

// The dev session has no workspace: use ?workspace= or the install's only one. With several and
// none named, refuse rather than hand a key to an unchosen tenant.
func (h *Handler) keyCaller(w http.ResponseWriter, r *http.Request) (creatorID, workspaceID string, role Role, ok bool) {
	ctx := r.Context()
	if c := ClaimsFromContext(ctx); c != nil {
		creatorID, workspaceID, role = c.UserID, c.WorkspaceID, c.Role
	} else if IsAPIKeyAuthed(ctx) {
		workspaceID, role = APIKeyWorkspace(ctx), APIKeyRole(ctx)
	} else if DevSession(ctx) {
		return h.devSessionKeyWorkspace(w, r)
	}
	if workspaceID == "" {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.key_caller_no_workspace"))
		return "", "", "", false
	}
	return creatorID, workspaceID, role, true
}

func (h *Handler) devSessionKeyWorkspace(w http.ResponseWriter, r *http.Request) (creatorID, workspaceID string, role Role, ok bool) {
	ctx := r.Context()
	if named := strings.TrimSpace(r.URL.Query().Get("workspace")); named != "" {
		ws, err := h.store.GetWorkspace(ctx, named)
		if err != nil {
			log.Printf("[auth] %s", i18n.Log("auth.log.key_workspace_read_failed", "err", err))
			apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
			return "", "", "", false
		}
		if ws == nil {
			apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.workspace_not_found"))
			return "", "", "", false
		}
		return "", ws.ID, RoleOwner, true
	}
	sole, err := h.store.SoleWorkspaceID(ctx)
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.key_workspace_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return "", "", "", false
	}
	if sole == "" {
		apperr.WriteJSON(w, r, http.StatusBadRequest, apperr.Invalid("auth.errors.key_workspace_ambiguous"))
		return "", "", "", false
	}
	return "", sole, RoleOwner, true
}

// Listing needs keys:write too: the list is the workspace's credential inventory.
func keyManagementDenial(r *http.Request) error {
	ctx := r.Context()
	if !IsAPIKeyAuthed(ctx) {
		return nil
	}
	if APIKeyRunScope(ctx).IsRun() {
		return apperr.Forbidden("auth.errors.run_key_cannot_manage_keys")
	}
	if !APIKeyGrants(ctx, PermKeysWrite) {
		return apperr.Forbidden("auth.errors.key_lacks_permission", "permission", PermKeysWrite)
	}
	return nil
}

// nil means a session (bounded by its role). A key with no permissions gets a non-nil empty slice
// so it can grant nothing.
func callerPermissions(r *http.Request) []Permission {
	if IsAPIKeyAuthed(r.Context()) {
		held := APIKeyPermissions(r.Context())
		if held == nil {
			return []Permission{}
		}
		return held
	}
	return nil
}

func permissionsBeyond(want, held []Permission) []Permission {
	have := make(map[Permission]bool, len(held))
	for _, p := range held {
		if p == PermAll {
			return nil
		}
		have[p] = true
	}
	var extra []Permission
	for _, p := range want {
		if !have[p] {
			extra = append(extra, p)
		}
	}
	return extra
}

func capToParentExpiry(grant *KeyGrant, explicit bool, parent *time.Time) error {
	if parent == nil {
		return nil
	}
	left := time.Until(*parent)
	if left <= 0 {
		return apperr.Invalid("auth.errors.parent_key_expired")
	}
	if explicit && grant.ExpiresIn > left {
		return apperr.Invalid("auth.errors.key_outlives_parent", "expires", parent.UTC().Format(time.RFC3339))
	}
	if !explicit {
		grant.ExpiresIn = left
		if maxTTL := APIKeyMaxTTL(); maxTTL > 0 && grant.ExpiresIn > maxTTL {
			grant.ExpiresIn = maxTTL
		}
	}
	return nil
}

func resolveGrant(req createAPIKeyRequest, workspaceID string, callerRole Role, callerPerms []Permission) (KeyGrant, error) {
	grant := KeyGrant{WorkspaceID: workspaceID, Role: RoleDeveloper}
	if req.ExpiresInDays > 0 {
		grant.ExpiresIn = time.Duration(req.ExpiresInDays) * 24 * time.Hour
	}

	if strings.TrimSpace(req.Role) != "" {
		role := Role(strings.ToLower(strings.TrimSpace(req.Role)))
		if !validRole(role) {
			return KeyGrant{}, apperr.Invalid("auth.errors.unknown_role", "role", req.Role)
		}
		grant.Role = role
	}
	if callerRole != "" && !roleAtLeast(callerRole, grant.Role) {
		return KeyGrant{}, apperr.Forbidden("auth.errors.role_above_caller", "role", grant.Role, "caller", callerRole)
	}

	perms, err := ParsePermissions(req.Permissions)
	if err != nil {
		return KeyGrant{}, err
	}
	if len(perms) == 0 {
		return KeyGrant{}, apperr.Invalid("auth.errors.permissions_required",
			"permissions", joinPermissions(AllPermissions()), "presets", strings.Join(PresetNames(), ", "))
	}
	// Capping the role alone let a key holding only keys:write mint one with every permission its
	// role reaches.
	if callerPerms != nil {
		if extra := permissionsBeyond(perms, callerPerms); len(extra) > 0 {
			return KeyGrant{}, apperr.Forbidden("auth.errors.permissions_beyond_caller", "permissions", joinPermissions(extra))
		}
	}
	grant.Permissions = perms
	return grant, nil
}

func validRole(r Role) bool {
	switch r {
	case RoleViewer, RoleDeveloper, RoleAdmin, RoleOwner:
		return true
	}
	return false
}

func (h *Handler) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	if !h.isAdminOrAPIKey(r) {
		apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.admin_or_key_required"))
		return
	}
	if reason := keyManagementDenial(r); reason != nil {
		apperr.WriteJSON(w, r, http.StatusForbidden, reason)
		return
	}
	_, ws, _, ok := h.keyCaller(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	name := id
	if k, gerr := h.store.GetWorkspaceAPIKey(r.Context(), id, ws); gerr == nil && k != nil && k.Name != "" {
		name = k.Name
	}
	found, err := h.store.RevokeWorkspaceAPIKey(r.Context(), id, ws)
	if err != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.revoke_api_key_failed", "err", err))
		apperr.WriteJSON(w, r, http.StatusInternalServerError, apperr.Internal("auth.errors.internal"))
		return
	}
	if !found {
		// Same 404 for another workspace's key as for a missing one, so ids can't be probed across
		// workspaces.
		apperr.WriteJSON(w, r, http.StatusNotFound, apperr.NotFound("auth.errors.api_key_not_found"))
		return
	}
	h.auditRequest(r, "auth.api_key_revoked", ws, id, "api_key", i18n.Text("auth.audit.api_key_revoked", "name", name))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

func (h *Handler) userLabel(ctx context.Context, id string) string {
	if u, err := h.store.GetUserByID(ctx, id); err == nil && u != nil && u.Email != "" {
		return u.Email
	}
	return id
}

func (h *Handler) isAdminOrAPIKey(r *http.Request) bool {
	if IsAPIKeyAuthed(r.Context()) {
		return true
	}
	if c := ClaimsFromContext(r.Context()); c != nil {
		return roleAtLeast(c.Role, RoleAdmin)
	}
	return DevSession(r.Context())
}

type tokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
}

// refreshCookieTTL in cookies.go must match.
const refreshTokenTTL = 7 * 24 * time.Hour

type deviceInfo struct {
	ua string
	ip string
}

// Use ClientIP, never X-Forwarded-For directly: only trusted proxies may set it, or clients spoof
// the session list.
func clientDevice(r *http.Request) deviceInfo {
	ua := r.UserAgent()
	if len(ua) > 255 {
		ua = ua[:255]
	}
	return deviceInfo{ua: ua, ip: middleware.ClientIP(r)}
}

func (h *Handler) issueTokenPair(ctx context.Context, user *User, wsID string, role Role, dev deviceInfo) (*tokenResponse, error) {
	now := time.Now().UTC()
	accessToken, err := h.issueAccess(user, wsID, role, now)
	if err != nil {
		return nil, err
	}
	rawRefresh, refreshHash, err := IssueRefreshToken()
	if err != nil {
		return nil, err
	}
	if _, err := h.store.StoreRefreshTokenFamily(ctx, user.ID, wsID, refreshHash, "", now, now.Add(refreshTokenTTL), dev.ua, dev.ip); err != nil {
		return nil, err
	}
	return &tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		ExpiresAt:    now.Add(AccessTTL()),
		TokenType:    "Bearer", //i18n:ignore OAuth token type
	}, nil
}

func (h *Handler) rotateTokenPair(ctx context.Context, user *User, wsID string, role Role, oldHash, familyID string,
	familyStart time.Time, dev deviceInfo) (*tokenResponse, error) {
	now := time.Now().UTC()
	accessToken, err := h.issueAccess(user, wsID, role, now)
	if err != nil {
		return nil, err
	}
	rawRefresh, refreshHash, err := IssueRefreshToken()
	if err != nil {
		return nil, err
	}
	if err := h.store.RotateRefreshToken(ctx, oldHash, refreshHash, user.ID, wsID, familyID,
		familyStart, now.Add(refreshTokenTTL), dev.ua, dev.ip); err != nil {
		return nil, err
	}
	return &tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		ExpiresAt:    now.Add(AccessTTL()),
		TokenType:    "Bearer", //i18n:ignore OAuth token type
	}, nil
}

func (h *Handler) issueAccess(user *User, wsID string, role Role, now time.Time) (string, error) {
	return IssueJWT(Claims{
		UserID:       user.ID,
		Email:        user.Email,
		WorkspaceID:  wsID,
		Role:         role,
		TokenVersion: user.TokenVersion,
		IssuedAt:     now,
		ExpiresAt:    now.Add(AccessTTL()),
	})
}

func slugify(s string) string {
	s = foldDiacritics(strings.ToLower(strings.TrimSpace(s)))
	var out strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		} else if out.Len() > 0 {
			if out.String()[out.Len()-1] != '-' {
				out.WriteByte('-')
			}
		}
	}
	result := strings.Trim(out.String(), "-")
	if r := []rune(result); len(r) > 50 {
		result = strings.TrimRight(string(r[:50]), "-")
	}
	return result
}

func foldDiacritics(s string) string {
	out, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err != nil {
		return s
	}
	return out
}
