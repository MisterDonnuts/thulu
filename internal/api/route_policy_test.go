package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRoutePolicy(t *testing.T) {
	publicRouteAllowlist := map[string]string{
		"GET /health":                   "liveness/health probe — must be unauthenticated",
		"GET /health/live":              "liveness probe",
		"GET /health/ready":             "readiness probe",
		"GET /openapi.json":             "public API schema",
		"GET /info":                     "public edition banner; version/licensee only to authenticated callers",
		"GET /metrics":                  "gated by gateMetrics (token or operator admin), not rr()",
		"POST /billing/stripe/webhook":  "public; authenticated by the Stripe signature in the handler",
		"POST /github/webhook":          "public; authenticated by the X-Hub-Signature-256 HMAC in the handler (fails closed without a secret)",
		"GET /github/setup":             "public; GitHub browser redirect — persists nothing unverified (AES-GCM state + re-fetch from GitHub as the App)",
		"GET /github/oauth/callback":    "public; GitHub browser redirect — same verification as /github/setup, code exchange is audit-only",
		"GET /github/manifest/callback": "public; GitHub browser redirect after registering the App — needs the single-use manifest state and its binder cookie before the code is exchanged",
		"/":                             "SPA catch-all; static assets are public, API routes under it are gated individually",
	}

	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}

	// Only single-line mux.Handle/HandleFunc registrations in server.go match; a route split across
	// lines escapes this check.
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)",\s*([^\n]+?)\)\s*(?://.*)?$`)
	lines := strings.Split(string(src), "\n")

	var ungated []string
	for _, line := range lines {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		route, handler := m[1], m[2]
		if _, ok := publicRouteAllowlist[route]; ok {
			continue
		}
		if strings.Contains(handler, "rr(") || strings.Contains(handler, "gateMetrics(") {
			continue
		}
		ungated = append(ungated, route+"  →  "+handler)
	}

	if len(ungated) > 0 {
		t.Errorf("found %d route(s) registered without a role gate and not in the public allowlist:\n  %s\n\n"+
			"Wrap them with rr(auth.Role…, …) or, if genuinely public, add them to publicRouteAllowlist with a reason.",
			len(ungated), strings.Join(ungated, "\n  "))
	}
}
