package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

type countingProber struct{ pinged []string }

func (p *countingProber) Tools(context.Context, string) ([]mcp.ToolInfo, error) { return nil, nil }
func (p *countingProber) Ping(_ context.Context, u string) error {
	p.pinged = append(p.pinged, u)
	return nil
}

func TestListMCPsHidesAddressesFromTenants(t *testing.T) {
	const opWS = "ws-operator"
	t.Setenv(OperatorWorkspaceEnv, opWS)
	prober := &countingProber{}
	srv := &Server{}
	srv.mcp.env = map[string]string{"MCP_THULU_GITHUB_URL": "http://10.0.4.7:3000/sse"}
	srv.mcp.prober = prober

	w := httptest.NewRecorder()
	srv.handleListMCPs(w, tenantRequest(http.MethodGet, "/mcps", "ws-tenant"))
	if w.Code != http.StatusOK {
		t.Fatalf("tenant GET /mcps = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "10.0.4.7") {
		t.Errorf("tenant /mcps carries the server address: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "thulu-github") {
		t.Errorf("tenant /mcps lost the server name: %s", w.Body.String())
	}
	if len(prober.pinged) != 0 {
		t.Errorf("a tenant's /mcps pinged %v", prober.pinged)
	}

	w = httptest.NewRecorder()
	srv.handleListMCPs(w, tenantRequest(http.MethodGet, "/mcps", opWS))
	if !strings.Contains(w.Body.String(), "10.0.4.7") {
		t.Errorf("operator /mcps lost the address: %s", w.Body.String())
	}
	if len(prober.pinged) != 1 {
		t.Errorf("operator /mcps pinged %v, want the one env server", prober.pinged)
	}
}

func TestOnboardingKeepsInstallDetailsFromTenants(t *testing.T) {
	const opWS = "ws-operator"
	t.Setenv(OperatorWorkspaceEnv, opWS)
	t.Setenv("OCTOPUS_OIDC_ISSUER", "https://id.example.test")
	srv := &Server{}

	steps := func(wid string) (string, map[string]bool) {
		w := httptest.NewRecorder()
		srv.handleOnboarding(w, tenantRequest(http.MethodGet, "/onboarding", wid))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /onboarding from %s = %d: %s", wid, w.Code, w.Body.String())
		}
		var status onboardingStatus
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, s := range status.Steps {
			ids[s.ID] = true
		}
		return w.Body.String(), ids
	}

	body, ids := steps("ws-tenant")
	if strings.Contains(body, "id.example.test") {
		t.Errorf("tenant onboarding names the issuer: %s", body)
	}
	if ids["identity"] || ids["workspace"] {
		t.Errorf("tenant onboarding carries install-wide steps: %v", ids)
	}
	if !ids["llm_key"] || !ids["project"] {
		t.Errorf("tenant onboarding lost its own steps: %v", ids)
	}

	body, ids = steps(opWS)
	if !ids["identity"] || !strings.Contains(body, "id.example.test") {
		t.Errorf("operator onboarding lost the identity step: %s", body)
	}
}

func TestSettingsReadsAreTheOperatorsExceptIngest(t *testing.T) {
	store, err := settings.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.PutRaw(ctx, settings.SandboxMaxKey, "7"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRaw(ctx, settings.IngestPublicURLKey, `"https://octopus.example.test"`); err != nil {
		t.Fatal(err)
	}
	const opWS = "ws-operator"
	t.Setenv(OperatorWorkspaceEnv, opWS)
	srv := &Server{settingsStore: store}

	list := func(wid string) map[string]json.RawMessage {
		w := httptest.NewRecorder()
		srv.handleListSettings(w, tenantRequest(http.MethodGet, "/settings", wid))
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET /settings from %s: %s", wid, w.Body.String())
		}
		return out
	}
	tenant := list("ws-tenant")
	if _, ok := tenant[settings.SandboxMaxKey]; ok {
		t.Errorf("tenant GET /settings carries %s: %v", settings.SandboxMaxKey, tenant)
	}
	if _, ok := tenant[settings.IngestPublicURLKey]; !ok {
		t.Errorf("tenant GET /settings lost the ingest address: %v", tenant)
	}
	if _, ok := list(opWS)[settings.SandboxMaxKey]; !ok {
		t.Error("operator GET /settings lost the host keys")
	}

	get := func(wid, key string) int {
		r := tenantRequest(http.MethodGet, "/settings/"+key, wid)
		r.SetPathValue("key", key)
		w := httptest.NewRecorder()
		srv.handleGetSetting(w, r)
		return w.Code
	}
	if code := get("ws-tenant", settings.SandboxMaxKey); code != http.StatusForbidden {
		t.Errorf("tenant GET /settings/%s = %d, want 403", settings.SandboxMaxKey, code)
	}
	if code := get("ws-tenant", settings.IngestPublicURLKey); code != http.StatusOK {
		t.Errorf("tenant GET /settings/%s = %d, want 200", settings.IngestPublicURLKey, code)
	}
	if code := get(opWS, settings.SandboxMaxKey); code != http.StatusOK {
		t.Errorf("operator GET /settings/%s = %d, want 200", settings.SandboxMaxKey, code)
	}
}

func TestHealthReportIsTheOperatorsOrTheScrapers(t *testing.T) {
	srv, h := newIntegrationServer(t)
	tenantToken, _ := mintWorkspaceUser(t, srv, "health-tenant@test.com", "HealthTenant")
	opToken, opWS := mintWorkspaceUser(t, srv, "health-op@test.com", "HealthOp")
	t.Setenv(OperatorWorkspaceEnv, opWS)
	t.Setenv("OCTOPUS_DEV_MODE", "0")
	t.Setenv("OCTOPUS_METRICS_TOKEN", "scrape-secret")

	fields := func(bearer string) map[string]any {
		r := httptest.NewRequest(http.MethodGet, "/health", nil)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /health = %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	ghost, err := auth.IssueJWT(auth.Claims{UserID: "u-gone", Email: "gone@test", WorkspaceID: opWS, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	for name, bearer := range map[string]string{
		"anonymous":                  "",
		"a tenant":                   tenantToken,
		"a wrong token":              "not-the-token",
		"a revoked operator session": ghost,
	} {
		if got := fields(bearer); len(got) != 1 || got["status"] == nil {
			t.Errorf("%s GET /health = %v, want the status alone", name, got)
		}
	}
	for name, bearer := range map[string]string{
		"the operator":      opToken,
		"the metrics token": "scrape-secret",
	} {
		if got := fields(bearer); got["checks"] == nil || got["system"] == nil {
			t.Errorf("%s GET /health = %v, want the full report", name, got)
		}
	}
}
