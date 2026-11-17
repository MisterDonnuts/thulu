package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestEntitlements_GetDefaultAndSetPlan(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)

	db := testpg.New(t)
	es, err := entitlements.NewStore(db, entitlements.PlanTrial)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetEntitlements(es)

	g := httptest.NewRequest(http.MethodGet, "/entitlements", nil)
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, g)
	if gw.Code != http.StatusOK {
		t.Fatalf("GET /entitlements = %d", gw.Code)
	}
	var ent entitlements.Entitlement
	_ = json.Unmarshal(gw.Body.Bytes(), &ent)
	if ent.Plan != entitlements.PlanTrial || ent.SeatLimit != 4 {
		t.Errorf("expected the default trial, got %+v", ent)
	}

	body, _ := json.Marshal(map[string]any{"plan": "team", "agents": 15})
	p := httptest.NewRequest(http.MethodPut, "/entitlements", strings.NewReader(string(body)))
	p.Header.Set("Content-Type", "application/json")
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, p)
	if pw.Code != http.StatusOK {
		t.Fatalf("PUT /entitlements = %d: %s", pw.Code, pw.Body.String())
	}
	var upgraded entitlements.Entitlement
	_ = json.Unmarshal(pw.Body.Bytes(), &upgraded)
	if upgraded.Plan != entitlements.PlanTeam || upgraded.ConcurrencyLimit != 15 || upgraded.HourQuotaMonthly != 1500 {
		t.Errorf("expected Team with 15 agents after upgrade, got %+v", upgraded)
	}

	bad, _ := json.Marshal(map[string]string{"plan": "platinum"})
	bp := httptest.NewRequest(http.MethodPut, "/entitlements", strings.NewReader(string(bad)))
	bp.Header.Set("Content-Type", "application/json")
	bpw := httptest.NewRecorder()
	h.ServeHTTP(bpw, bp)
	if bpw.Code != http.StatusBadRequest {
		t.Errorf("unknown plan should be 400, got %d", bpw.Code)
	}
}

// The plan's cap on people applies in the SaaS only. A self-hosted install with a plan row (say an
// operator set Starter by hand, or it runs on Kubernetes) never blocks anyone.
func TestSeatLimit_OnlyInSaaS(t *testing.T) {
	srv, _ := newIntegrationServer(t)
	es, err := entitlements.NewStore(testpg.New(t), entitlements.PlanStarter)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetEntitlements(es)

	t.Setenv(entitlements.SaaSModeEnv, "")
	if got := srv.seatLimitForWorkspace("ws-1"); got != 0 {
		t.Errorf("self-hosted seat limit = %d, want 0", got)
	}
	t.Setenv(entitlements.SaaSModeEnv, "1")
	if got := srv.seatLimitForWorkspace("ws-1"); got != 4 {
		t.Errorf("SaaS Starter seat limit = %d, want 4", got)
	}
}
