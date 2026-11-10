package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createEnvForIngest(t *testing.T, h http.Handler, token, projID, name string) string {
	t.Helper()
	w := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": name, "provider": "aws"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create env = %d: %s", w.Code, w.Body.String())
	}
	return decodeEnv(t, w)["id"].(string)
}

func mintIngestToken(t *testing.T, h http.Handler, token, projID, envID string, cidrs []string) (raw, id string) {
	t.Helper()
	body := map[string]any{"name": "collector"}
	if cidrs != nil {
		body["allow_cidrs"] = cidrs
	}
	w := sendJSON(t, h, http.MethodPost,
		"/projects/"+projID+"/environments/"+envID+"/ingest-tokens", token, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("mint token = %d: %s", w.Code, w.Body.String())
	}
	out := decodeEnv(t, w)
	raw, _ = out["raw"].(string)
	tok, _ := out["token"].(map[string]any)
	if raw == "" || tok == nil {
		t.Fatalf("mint response missing raw or token: %s", w.Body.String())
	}
	id, _ = tok["id"].(string)
	return raw, id
}

func postOTLP(t *testing.T, h http.Handler, kind, rawToken, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/ingest/v1/"+kind, strings.NewReader(body))
	if rawToken != "" {
		req.Header.Set("X-Octopus-Token", rawToken)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// Routes register before SetIngest; a handler captured at registration is nil and the door silently
// 404s.
func TestIntegration_IngestDoorIsMounted(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ingestdoor@test.com", "ID", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "DoorProject")
	envID := createEnvForIngest(t, h, token, projID, "prod")
	raw, _ := mintIngestToken(t, h, token, projID, envID, nil)

	for _, kind := range []string{"traces", "metrics", "logs"} {
		if w := postOTLP(t, h, kind, raw, "payload"); w.Code != http.StatusOK {
			t.Errorf("/ingest/v1/%s = %d, want 200: %s", kind, w.Code, w.Body.String())
		}
	}
}

// /ingest must stay in isPublic: collectors send only a telemetry token, and the API-key gate would
// refuse first.
func TestIntegration_IngestDoorBypassesTheAPIKeyGate(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ingestgate@test.com", "IG", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "GateProject")
	envID := createEnvForIngest(t, h, token, projID, "prod")
	raw, _ := mintIngestToken(t, h, token, projID, envID, nil)

	if w := postOTLP(t, h, "traces", raw, "x"); w.Code != http.StatusOK {
		t.Fatalf("collector request = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := postOTLP(t, h, "traces", "", "x"); w.Code != http.StatusForbidden {
		t.Errorf("anonymous = %d, want 403", w.Code)
	}
}

func TestIntegration_IngestTokensScopedToProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ingestscope@test.com", "IS", "SecurePassword1")
	projA := createProjectForMetrics(t, h, token, "IngestA")
	projB := createProjectForMetrics(t, h, token, "IngestB")
	envID := createEnvForIngest(t, h, token, projA, "prod")
	_, tokenID := mintIngestToken(t, h, token, projA, envID, nil)

	base := "/projects/" + projB + "/environments/" + envID
	if w := getJSON(t, h, base+"/ingest-tokens", token); w.Code != http.StatusNotFound {
		t.Errorf("cross-project list = %d, want 404", w.Code)
	}
	if w := getJSON(t, h, base+"/ingest-sources", token); w.Code != http.StatusNotFound {
		t.Errorf("cross-project sources = %d, want 404", w.Code)
	}
	if w := sendJSON(t, h, http.MethodPost, base+"/ingest-tokens", token, map[string]any{"name": "x"}); w.Code != http.StatusNotFound {
		t.Errorf("cross-project mint = %d, want 404", w.Code)
	}
	if w := sendJSON(t, h, http.MethodDelete, base+"/ingest-tokens/"+tokenID, token, nil); w.Code != http.StatusNotFound {
		t.Errorf("cross-project revoke = %d, want 404", w.Code)
	}
	if w := getJSON(t, h, "/projects/"+projA+"/environments/"+envID+"/ingest-tokens", token); w.Code != http.StatusOK {
		t.Errorf("owning project list = %d, want 200", w.Code)
	}
}

func TestIntegration_IngestSecretShownOnceOnly(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ingestsecret@test.com", "ISC", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "SecretProject")
	envID := createEnvForIngest(t, h, token, projID, "prod")
	raw, _ := mintIngestToken(t, h, token, projID, envID, nil)

	w := getJSON(t, h, "/projects/"+projID+"/environments/"+envID+"/ingest-tokens", token)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), raw) {
		t.Fatal("the token listing contains the raw secret")
	}
}

func TestIntegration_IngestRevokedTokenIsRefused(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ingestrevoke@test.com", "IR", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "RevokeProject")
	envID := createEnvForIngest(t, h, token, projID, "prod")
	raw, tokenID := mintIngestToken(t, h, token, projID, envID, nil)

	if w := postOTLP(t, h, "metrics", raw, "x"); w.Code != http.StatusOK {
		t.Fatalf("before revoke = %d, want 200", w.Code)
	}
	if w := sendJSON(t, h, http.MethodDelete,
		"/projects/"+projID+"/environments/"+envID+"/ingest-tokens/"+tokenID, token, nil); w.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := postOTLP(t, h, "metrics", raw, "x"); w.Code != http.StatusForbidden {
		t.Errorf("after revoke = %d, want 403", w.Code)
	}
}

func TestIntegration_IngestSourceAuditIsVisible(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "ingestaudit@test.com", "IA", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "AuditProject")
	envID := createEnvForIngest(t, h, token, projID, "prod")
	// httptest requests come from 192.0.2.1, so this allowlist blocks them.
	raw, _ := mintIngestToken(t, h, token, projID, envID, []string{"10.0.0.0/8"})

	if w := postOTLP(t, h, "traces", raw, "x"); w.Code != http.StatusForbidden {
		t.Fatalf("blocked address = %d, want 403", w.Code)
	}
	w := getJSON(t, h, "/projects/"+projID+"/environments/"+envID+"/ingest-sources", token)
	if w.Code != http.StatusOK {
		t.Fatalf("sources = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "ip_not_allowed") {
		t.Errorf("audit does not report why the request was refused: %s", body)
	}
}
