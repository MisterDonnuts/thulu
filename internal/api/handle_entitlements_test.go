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
	es, err := entitlements.NewStore(db, entitlements.PlanFree)
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
	if ent.Plan != entitlements.PlanFree || ent.SeatLimit != 4 {
		t.Errorf("expected default Free plan, got %+v", ent)
	}

	body, _ := json.Marshal(map[string]string{"plan": "business"})
	p := httptest.NewRequest(http.MethodPut, "/entitlements", strings.NewReader(string(body)))
	p.Header.Set("Content-Type", "application/json")
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, p)
	if pw.Code != http.StatusOK {
		t.Fatalf("PUT /entitlements = %d: %s", pw.Code, pw.Body.String())
	}
	var upgraded entitlements.Entitlement
	_ = json.Unmarshal(pw.Body.Bytes(), &upgraded)
	if upgraded.Plan != entitlements.PlanBusiness || upgraded.ConcurrencyLimit != 15 {
		t.Errorf("expected Business limits after upgrade, got %+v", upgraded)
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
