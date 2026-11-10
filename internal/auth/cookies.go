package auth

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/middleware"
)

const (
	CookieAccess  = "octopus_at"
	CookieRefresh = "octopus_rt"
	CookieCSRF    = "octopus_csrf"
)

// Must match refreshTokenTTL in handlers.go so a session can refresh after the access token lapses.
const refreshCookieTTL = 7 * 24 * time.Hour

// Strict covers embedded and same-site localhost frontends. A cross-site frontend needs a code
// change here, not an env override that silently weakens the default.
const cookieSameSite = http.SameSiteStrictMode

// X-Forwarded-Proto counts only from OCTOPUS_TRUSTED_PROXIES peers (it decides the OIDC callback
// and redirect scheme); the last entry is our proxy's.
// Not-Secure over plain HTTP is deliberate: browsers drop Secure cookies over HTTP, which breaks
// LAN login.
func requestIsSecure(r *http.Request) bool {
	if r == nil {
		return true
	}
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" || !middleware.PeerIsTrustedProxy(r) {
		return false
	}
	if i := strings.LastIndexByte(proto, ','); i >= 0 {
		proto = proto[i+1:]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func NewCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func SetAuthCookies(w http.ResponseWriter, accessToken, refreshToken, csrf string, accessExpiry time.Time, secure bool) {
	refreshMaxAge := int(refreshCookieTTL.Seconds())

	http.SetCookie(w, &http.Cookie{
		Name:     CookieAccess,
		Value:    accessToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: cookieSameSite,
		Expires:  accessExpiry,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CookieRefresh,
		Value:    refreshToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: cookieSameSite,
		MaxAge:   refreshMaxAge,
	})
	// HttpOnly:false on purpose: the SPA echoes this value in X-CSRF-Token (double-submit,
	// CSRFMiddleware).
	// cookies.go is excluded from gosec G124 in .golangci.yml for this reason.
	http.SetCookie(w, &http.Cookie{
		Name:     CookieCSRF,
		Value:    csrf,
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: cookieSameSite,
		MaxAge:   refreshMaxAge,
	})
}

func ClearAuthCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{CookieAccess, CookieRefresh, CookieCSRF} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: name != CookieCSRF,
			Secure:   secure,
			SameSite: cookieSameSite,
			MaxAge:   -1,
		})
	}
}

func setSessionCookies(w http.ResponseWriter, r *http.Request, t *tokenResponse) error {
	csrf, err := NewCSRFToken()
	if err != nil {
		return err
	}
	SetAuthCookies(w, t.AccessToken, t.RefreshToken, csrf, t.ExpiresAt, requestIsSecure(r))
	return nil
}

func writeDevSessionCookies(w http.ResponseWriter, tok, csrf string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieDevSession, Value: tok, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: cookieSameSite, MaxAge: maxAge,
	})
	// HttpOnly:false on purpose, as in SetAuthCookies: the SPA echoes it back.
	http.SetCookie(w, &http.Cookie{
		Name: CookieCSRF, Value: csrf, Path: "/",
		HttpOnly: false, Secure: secure, SameSite: cookieSameSite, MaxAge: maxAge,
	})
}

// A fixed CSRF pair is fine: double-submit only stops cross-site forgery, and this caller already
// holds the dev token.
func AttachDevSession(req *http.Request, tok string) {
	if tok == "" {
		return
	}
	req.AddCookie(&http.Cookie{Name: CookieDevSession, Value: tok})
	if !isSafeMethod(req.Method) {
		const pair = "cli"
		req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: pair})
		req.Header.Set("X-CSRF-Token", pair)
	}
}

// Lax, not Strict: the flow ends with a cross-site top-level redirect back, and Strict would strip
// the cookie.
func SetFlowBindCookie(w http.ResponseWriter, r *http.Request, name, path, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode,
	})
}

func ClearFlowBindCookie(w http.ResponseWriter, r *http.Request, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: path, MaxAge: -1,
		HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode,
	})
}
