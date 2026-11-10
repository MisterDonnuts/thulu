package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type contextKey string

const claimsKey contextKey = "octopus_claims"

var errInvalidTokenVersion error = apperr.Unauthenticated("auth.errors.token_version_mismatch")

var errTokenVersionUserGone error = apperr.Unauthenticated("auth.errors.token_user_gone")

func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsKey).(*Claims) //nolint:errcheck // type assertion ok-idiom; nil is the safe default
	return c
}

// A token_version bump evicts access tokens immediately on this daemon and within the
// tokenVersionCache TTL on others.
func JWTMiddlewareWithStore(store *UserStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, publicRequest(r, store))
			return
		}

		authz, fromCookie := bearerCredential(r)
		if authz == "" {
			next.ServeHTTP(w, r)
			return
		}

		tokenStr, ok := strings.CutPrefix(authz, "Bearer ") //i18n:ignore HTTP auth scheme
		if !ok {
			// Basic credentials pass through: the /git transport authenticates git's Basic password
			// (a task-scoped key) itself in gitAuthorize.
			if strings.HasPrefix(authz, "Basic ") { //i18n:ignore HTTP auth scheme
				next.ServeHTTP(w, r)
				return
			}
			apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.authorization_header_invalid"))
			return
		}

		if strings.Count(tokenStr, ".") != 2 {
			next.ServeHTTP(w, r)
			return
		}

		claims, err := ValidateJWT(tokenStr)
		if err == nil && tokenVersionStale(r.Context(), store, claims) {
			err = errInvalidTokenVersion
		}
		if err != nil {
			if fromCookie {
				handleStaleCookieAuth(w, r, next)
				return
			}
			apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.token_invalid"))
			return
		}

		next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), claims)))
	})
}

// Any bad credential on /health leaves the request anonymous so a probe never fails on a stale
// cookie.
func publicRequest(r *http.Request, store *UserStore) *http.Request {
	if r.URL.Path != "/health" {
		return r
	}
	authz, _ := bearerCredential(r)
	tokenStr, ok := strings.CutPrefix(authz, "Bearer ") //i18n:ignore HTTP auth scheme
	if !ok || strings.Count(tokenStr, ".") != 2 {
		return r
	}
	claims, err := ValidateJWT(tokenStr)
	if err != nil || tokenVersionStale(r.Context(), store, claims) {
		return r
	}
	return r.WithContext(WithClaims(r.Context(), claims))
}

func bearerCredential(r *http.Request) (authz string, fromCookie bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		return h, false
	}
	if ck, err := r.Cookie(CookieAccess); err == nil && ck.Value != "" {
		return "Bearer " + ck.Value, true //i18n:ignore HTTP auth scheme
	}
	return "", false
}

func TokenVersionStale(ctx context.Context, store *UserStore, claims *Claims) bool {
	return tokenVersionStale(ctx, store, claims)
}

// A transient DB error is not a revocation: the signature validated, so the request passes rather
// than logging everyone out on a hiccup.
func tokenVersionStale(ctx context.Context, store *UserStore, claims *Claims) bool {
	if store == nil {
		return false
	}
	current, err := store.GetTokenVersion(ctx, claims.UserID)
	if errors.Is(err, errTokenVersionUserGone) {
		return true
	}
	return err == nil && current != claims.TokenVersion
}

func handleStaleCookieAuth(w http.ResponseWriter, r *http.Request, next http.Handler) {
	ClearAuthCookies(w, requestIsSecure(r))
	if !isBrowserNavigation(r) {
		stripped := r.Clone(r.Context())
		stripped.Header.Del("Cookie")
		next.ServeHTTP(w, stripped)
		return
	}
	dest := "/login/"
	if back := r.URL.Path; back != "" && back != "/login/" && back != "/login" {
		full := back
		if r.URL.RawQuery != "" {
			full = back + "?" + r.URL.RawQuery
		}
		dest = "/login/?next=" + url.QueryEscape(full)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func isBrowserNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.Header.Get("X-Requested-With") != "" {
		return false
	}
	mode := r.Header.Get("Sec-Fetch-Mode")
	if mode == "cors" || mode == "no-cors" || mode == "websocket" {
		return false
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}

func keyDenialReason(r *http.Request, minRole Role) error {
	ctx := r.Context()
	effective := APIKeyRole(ctx)
	if effective == "" {
		// Keys from before keys carried a role are denied, not assumed: the upgrade adopted every
		// placeable key and revoked the rest.
		return apperr.Forbidden("auth.errors.key_predates_workspaces")
	}
	if APIKeyRunScope(ctx).IsRun() && !roleAtLeast(RoleDeveloper, effective) {
		effective = RoleDeveloper
	}
	if !roleAtLeast(effective, minRole) {
		return apperr.Forbidden("auth.errors.key_role_too_low", "role", effective, "need", minRole)
	}
	if !permitted(APIKeyPermissions(ctx), r.Method, r.URL.Path) {
		need := PermissionForRequest(r.Method, r.URL.Path)
		if need == "" {
			return apperr.Forbidden("auth.errors.route_without_permission")
		}
		return apperr.Forbidden("auth.errors.key_lacks_permission", "permission", need)
	}
	return nil
}

func denyKey(w http.ResponseWriter, r *http.Request, reason error) {
	apperr.WriteJSON(w, r, http.StatusForbidden, reason)
}

func RequireRole(minRole Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if IsAPIKeyAuthed(r.Context()) {
			if reason := keyDenialReason(r, minRole); reason != nil {
				denyKey(w, r, reason)
				return
			}
			next(w, r)
			return
		}

		if DevSession(r.Context()) {
			next(w, r)
			return
		}

		c := ClaimsFromContext(r.Context())
		if c == nil {
			apperr.WriteJSON(w, r, http.StatusUnauthorized, apperr.Unauthenticated("auth.errors.authentication_required"))
			return
		}
		if !roleAtLeast(c.Role, minRole) {
			apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.insufficient_permissions"))
			return
		}
		next(w, r)
	}
}

func roleAtLeast(have, need Role) bool {
	order := map[Role]int{
		RoleViewer:    1,
		RoleDeveloper: 2,
		RoleAdmin:     3,
		RoleOwner:     4,
	}
	return order[have] >= order[need]
}

func isPublicPath(path string) bool {
	public := []string{
		"/health", "/health/live", "/health/ready",
		"/auth/refresh", "/auth/config",
		// Read by the login page before sign-in, where a stale session cookie must not cause a 401.
		// Also listed in isPreSessionAuthPath.
		"/auth/invitation", "/auth/access-request",
	}
	for _, p := range public {
		if path == p {
			return true
		}
	}
	return false
}

func (r Role) AtLeast(need Role) bool { return roleAtLeast(r, need) }

// Any API key, run keys included, is not a person: some decisions (delegating a chat, confirming
// it) need a person at the keyboard.
func PersonRole(ctx context.Context) (role Role, ok bool) {
	if IsAPIKeyAuthed(ctx) {
		return "", false
	}
	if c := ClaimsFromContext(ctx); c != nil {
		return c.Role, true
	}
	if DevSession(ctx) {
		return RoleOwner, true
	}
	return "", false
}

// Must read roles exactly as RequireRole/keyDenialReason do, or UI flags promise what the route
// refuses.
func CallerRole(ctx context.Context) Role {
	if IsAPIKeyAuthed(ctx) {
		role := APIKeyRole(ctx)
		if role != "" && APIKeyRunScope(ctx).IsRun() && !roleAtLeast(RoleDeveloper, role) {
			role = RoleDeveloper
		}
		return role
	}
	if DevSession(ctx) {
		return RoleOwner
	}
	if c := ClaimsFromContext(ctx); c != nil {
		return c.Role
	}
	return ""
}
