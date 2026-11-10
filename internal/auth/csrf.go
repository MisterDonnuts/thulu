package auth

import (
	"crypto/subtle"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) || isCSRFExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Cross-site forms can't set these headers and they ride no ambient cookie, so they are not
		// a CSRF vector.
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
			next.ServeHTTP(w, r)
			return
		}
		if !hasSessionCookie(r) {
			next.ServeHTTP(w, r)
			return
		}

		csrfCookie, cErr := r.Cookie(CookieCSRF)
		header := r.Header.Get("X-CSRF-Token")
		if cErr != nil || csrfCookie.Value == "" || header == "" ||
			subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(header)) != 1 {
			apperr.WriteJSON(w, r, http.StatusForbidden, apperr.Forbidden("auth.errors.csrf_invalid"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hasSessionCookie(r *http.Request) bool {
	for _, name := range []string{CookieAccess, CookieDevSession} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return true
		}
	}
	return false
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// refresh/logout carry their own rotated refresh token under SameSite=Strict. Authenticated
// state-changing endpoints must never be added here.
func isCSRFExemptPath(path string) bool {
	switch path {
	case "/auth/refresh", "/auth/logout":
		return true
	// Authenticated by the signed token in its body, never a cookie: a forged post can only file
	// the request the token already names.
	case "/auth/access-request":
		return true
	default:
		return false
	}
}
