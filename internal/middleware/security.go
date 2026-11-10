package middleware

import (
	"net/http"
	"strings"
)

// script-src 'self' relies on web/astro.config.mjs (assetsInlineLimit 0, build fails on inline
// <script> or on*= handlers).
// style-src keeps 'unsafe-inline' for JS-set style="" attributes; style-src-elem 'self' still
// refuses injected <style>.
const (
	CSPStrict = "default-src 'none'; frame-ancestors 'none'"
	CSPWebApp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"style-src-elem 'self'; " +
		//i18n:ignore CSP directive, not a message
		"img-src 'self' data:; " +
		"font-src 'self'; " +
		"connect-src 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"object-src 'none'"
)

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		h.Set("X-Content-Type-Options", "nosniff")

		h.Set("X-Frame-Options", "DENY")

		h.Set("X-XSS-Protection", "0")

		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		// HSTS only when the daemon terminated TLS itself; behind a proxy the proxy owns it, and
		// X-Forwarded-Proto alone is not enough.
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		h.Set("Content-Security-Policy", CSPStrict)

		next.ServeHTTP(w, r)
	})
}

const (
	maxJSONBody   int64 = 4 * 1024 * 1024
	maxUploadBody int64 = 50 * 1024 * 1024
	// project.MaxUploadArchiveBytes (200 MB) plus multipart framing; change together.
	maxProjectUploadBody int64 = 201 * 1024 * 1024
)

func RequestBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		limit := maxJSONBody
		switch p := r.URL.Path; {
		case strings.HasPrefix(p, "/projects/") && strings.HasSuffix(p, "/upload"):
			limit = maxProjectUploadBody
		case strings.Contains(p, "/attachments") || strings.Contains(p, "/files"):
			limit = maxUploadBody
		case strings.HasPrefix(p, "/mcp-proxy/"):
			limit = maxUploadBody
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
