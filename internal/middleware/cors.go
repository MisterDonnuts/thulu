package middleware

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Without this the browser hides X-Next-Cursor from cross-origin callers and the dev SPA cannot
// paginate.
const exposedHeaders = "X-Next-Cursor"

const devOrigins = "http://localhost:4321,http://localhost:3000,http://localhost:5173"

// Mirrors auth.DevMode, which this package cannot import (auth imports middleware); keep both in
// sync.
func devModeForCORS() bool {
	return os.Getenv("OCTOPUS_DEV_MODE") == "1" && os.Getenv("KUBERNETES_SERVICE_HOST") == ""
}

// Dev-server origins are the default only in dev mode: with credentials allowed, any page on a
// user's localhost:3000 could use their session cookie.
func CORS(next http.Handler) http.Handler {
	raw := os.Getenv("OCTOPUS_ALLOWED_ORIGINS")
	if raw == "" && devModeForCORS() {
		raw = devOrigins
	}

	if strings.TrimSpace(raw) == "*" {
		log.Printf("[cors] %s", i18n.Log("middleware.log.cors_wildcard"))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			//i18n:ignore HTTP header names, not a message
			//i18n:ignore HTTP header names, not a message
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Request-ID, X-CSRF-Token")
			w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	allowedSet := make(map[string]bool)
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			allowedSet[o] = true
		}
	}
	if len(allowedSet) == 0 {
		log.Printf("[cors] %s", i18n.Log("middleware.log.cors_same_origin_only"))
	} else {
		//nolint:gosec // G706: raw is the OCTOPUS_ALLOWED_ORIGINS env value set by the operator at startup, not per-request user input
		log.Printf("[cors] %s", i18n.Log("middleware.log.cors_allowed_origins", "origins", raw))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin != "" && allowedSet[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin") //i18n:ignore HTTP header name
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		//i18n:ignore HTTP header names, not a message
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Request-ID, X-CSRF-Token")
		w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
