package auth

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/middleware"
)

type apiKeyContextKey struct{}

// Every field comes from the key's row; no value of this struct may mean "all workspaces".
type apiKeyAuth struct {
	id          string
	name        string
	scope       RunScope
	workspaceID string
	role        Role
	permissions []Permission
	expiresAt   *time.Time
}

func withKeyAuth(ctx context.Context, info *APIKeyInfo) context.Context {
	return context.WithValue(ctx, apiKeyContextKey{}, apiKeyAuth{
		id:          info.ID,
		name:        info.Name,
		scope:       info.Scope,
		workspaceID: info.WorkspaceID,
		role:        info.Role,
		permissions: info.Permissions,
		expiresAt:   info.ExpiresAt,
	})
}

func APIKeyID(ctx context.Context) string { return keyAuthFrom(ctx).id }

func APIKeyName(ctx context.Context) string { return keyAuthFrom(ctx).name }

func apiKeyExpiry(ctx context.Context) *time.Time { return keyAuthFrom(ctx).expiresAt }

func keyAuthFrom(ctx context.Context) apiKeyAuth {
	if v, ok := ctx.Value(apiKeyContextKey{}).(apiKeyAuth); ok {
		return v
	}
	return apiKeyAuth{}
}

func IsAPIKeyAuthed(ctx context.Context) bool {
	_, ok := ctx.Value(apiKeyContextKey{}).(apiKeyAuth)
	return ok
}

func APIKeyTaskScope(ctx context.Context) string {
	v, _ := ctx.Value(apiKeyContextKey{}).(apiKeyAuth) //nolint:errcheck // type assertion ok-idiom; zero value is the safe default
	return v.scope.TaskID
}

// Gate "is this an agent" on this, not APIKeyTaskScope: a chat key not linked to a task has no task
// ID.
func APIKeyRunScope(ctx context.Context) RunScope {
	v, _ := ctx.Value(apiKeyContextKey{}).(apiKeyAuth) //nolint:errcheck // type assertion ok-idiom; zero value is the safe default
	return v.scope
}

// Empty for per-run keys stored before workspaces were recorded; resolve those from their task.
func APIKeyWorkspace(ctx context.Context) string {
	v, _ := ctx.Value(apiKeyContextKey{}).(apiKeyAuth) //nolint:errcheck // type assertion ok-idiom; zero value is the safe default
	return v.workspaceID
}

func APIKeyRole(ctx context.Context) Role {
	v, _ := ctx.Value(apiKeyContextKey{}).(apiKeyAuth) //nolint:errcheck // type assertion ok-idiom; zero value is the safe default
	return v.role
}

func APIKeyPermissions(ctx context.Context) []Permission {
	v, _ := ctx.Value(apiKeyContextKey{}).(apiKeyAuth) //nolint:errcheck // type assertion ok-idiom; zero value is the safe default
	return v.permissions
}

// Ask for the permission directly: /auth/* is not in the path→resource table, so APIKeyAllows on an
// invented path denies everyone, wildcard included.
func APIKeyGrants(ctx context.Context, want Permission) bool {
	for _, held := range APIKeyPermissions(ctx) {
		if held == PermAll || held == want {
			return true
		}
	}
	return false
}

// isPublic paths bypass the env-key gate so the SPA can load /login before the browser has any
// credential.
func MiddlewareWithStoreAndPublic(store *UserStore, isPublic func(string) bool, next http.Handler) http.Handler {
	envKey := strings.TrimSpace(os.Getenv("OCTOPUS_API_KEY"))
	envKeyBytes := []byte(envKey)
	hasEnvKey := envKey != ""

	if hasEnvKey {
		log.Printf("[auth] %s", i18n.Log("auth.log.api_key_env_enabled"))
	}
	var keyRL *middleware.RateLimiter
	if store != nil {
		log.Printf("[auth] %s", i18n.Log("auth.log.api_key_db_enabled"))
		keyRL = middleware.NewPerKeyRateLimiter()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		candidate := extractKey(r)

		// An oct_ key must validate against the DB; a revoked or unknown one never falls through to
		// other auth methods.
		if store != nil && candidate != "" && strings.HasPrefix(candidate, "oct_") {
			serveOctKeyAuth(store, keyRL, candidate, next, w, r)
			return
		}

		if !hasEnvKey || envGateExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		// The env key only opens the perimeter; it grants no workspace authority, so scoped routes
		// still need a session or oct_ key.
		// Checked before isPublic because API routes share prefixes with dashboard pages (/tasks),
		// which would skip the gate.
		if candidate != "" && secureEqual([]byte(candidate), envKeyBytes) {
			next.ServeHTTP(w, r)
			return
		}
		if isPublic != nil && isPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		writeUnauthorized(w, r, apperr.Unauthenticated("auth.errors.api_key_required"))
	})
}

func serveOctKeyAuth(store *UserStore, keyRL *middleware.RateLimiter, candidate string, next http.Handler, w http.ResponseWriter, r *http.Request) {
	if info := store.LookupAPIKeyInfo(r.Context(), candidate); info != nil {
		// Limited after the lookup, by key id: unknown keys get no bucket (the per-IP limiter
		// handles guessing).
		if ok, retryAfter := keyRL.Allow(info.ID); !ok {
			middleware.WriteRateLimited(w, r, retryAfter)
			return
		}
		next.ServeHTTP(w, r.WithContext(withKeyAuth(r.Context(), info)))
		return
	}
	writeUnauthorized(w, r, apperr.Unauthenticated("auth.errors.api_key_invalid"))
}

// /metrics enforces its own auth (OCTOPUS_METRICS_TOKEN or viewer role); a Prometheus scraper can't
// also carry the env key.
func envGateExempt(r *http.Request) bool {
	return isHealthPath(r.URL.Path) || isMetricsPath(r.URL.Path) ||
		isPreSessionAuthPath(r.URL.Path) || ClaimsFromContext(r.Context()) != nil
}

func isHealthPath(p string) bool {
	return p == "/health" || p == "/health/live" || p == "/health/ready"
}

func isMetricsPath(p string) bool {
	return p == "/metrics"
}

// Gating these behind the env key would make signing in impossible: the client has no credential
// yet.
func isPreSessionAuthPath(p string) bool {
	switch p {
	case "/auth/refresh", "/auth/logout", "/auth/config":
		return true
	case "/auth/access-request", "/auth/invitation":
		return true
	// The Stripe webhook authenticates by Stripe-Signature, not a Thulu credential.
	case "/billing/stripe/webhook":
		return true
	}
	return strings.HasPrefix(p, "/auth/oidc/")
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request, err error) {
	apperr.WriteJSON(w, r, http.StatusUnauthorized, err)
}

func extractKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if after, ok := strings.CutPrefix(auth, "Bearer "); ok { //i18n:ignore HTTP auth scheme
			return strings.TrimSpace(after)
		}
	}
	if key := r.Header.Get("X-API-Key"); key != "" {
		return strings.TrimSpace(key)
	}
	return ""
}

func secureEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
