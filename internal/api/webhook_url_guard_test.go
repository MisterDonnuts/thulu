package api

import (
	"net/http"
	"testing"
)

func TestIntegration_Webhooks_RejectInternalURLs(t *testing.T) {
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")
	_, h := newIntegrationServer(t)

	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8585/internal/tasks",
		"http://10.0.0.5/hook",
		"http://[::1]/hook",
		"file:///etc/passwd",
		"https:///no-host",
	} {
		w := postJSON(t, h, "/webhooks", map[string]any{"url": u})
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST /webhooks %q = %d, want 400: %s", u, w.Code, w.Body.String())
		}
	}

	w := postJSON(t, h, "/webhooks", map[string]any{"url": "https://hooks.invalid/octopus"})
	if w.Code != http.StatusCreated {
		t.Errorf("POST /webhooks with an unresolvable host = %d, want 201: %s", w.Code, w.Body.String())
	}

	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "10.0.0.0/24")
	w = postJSON(t, h, "/webhooks", map[string]any{"url": "http://10.0.0.5/hook"})
	if w.Code != http.StatusCreated {
		t.Errorf("POST /webhooks to an allowed private range = %d, want 201: %s", w.Code, w.Body.String())
	}
}
