package entitlements_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newStore(t *testing.T, def entitlements.Plan) *entitlements.Store {
	t.Helper()
	db := testpg.New(t)
	s, err := entitlements.NewStore(db, def)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGet_DefaultsToConfiguredPlan(t *testing.T) {
	sh := newStore(t, entitlements.PlanSelfHosted)
	e, _ := sh.Get("ws-x")
	if e.Plan != entitlements.PlanSelfHosted || e.SeatLimit != 0 {
		t.Errorf("self-hosted default should be unlimited, got %+v", e)
	}
	saas := newStore(t, entitlements.PlanTrial)
	f, _ := saas.Get("ws-y")
	if f.Plan != entitlements.PlanTrial || f.SeatLimit != 4 || f.ConcurrencyLimit != 2 || f.HourQuotaMonthly != 50 {
		t.Errorf("saas default should be the trial, got %+v", f)
	}
}

func TestSetPlan_AppliesDefaults(t *testing.T) {
	s := newStore(t, entitlements.PlanFree)
	if err := s.SetPlan("ws-1", entitlements.PlanBusiness); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("ws-1")
	if e.Plan != entitlements.PlanBusiness || e.ConcurrencyLimit != 15 || e.HourQuotaMonthly != 1500 {
		t.Errorf("legacy business should read as Team with 15 agents, got %+v", e)
	}
	if len(e.Features) == 0 {
		t.Error("business plan should carry feature unlocks")
	}
}

func TestSet_CustomEnterprise(t *testing.T) {
	s := newStore(t, entitlements.PlanFree)
	custom := entitlements.Entitlement{
		WorkspaceID: "ws-ent", Plan: entitlements.PlanEnterprise,
		SeatLimit: 500, ConcurrencyLimit: 50, HourQuotaMonthly: 0,
		Features: []string{"audit_export", "kubernetes"},
	}
	if err := s.Set(custom); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("ws-ent")
	if got.SeatLimit != 500 || got.ConcurrencyLimit != 50 || len(got.Features) != 2 {
		t.Errorf("custom entitlement round-trip failed: %+v", got)
	}
}

func TestDeleteByWorkspace(t *testing.T) {
	s := newStore(t, entitlements.PlanTrial)
	_ = s.SetPlan("ws-del", entitlements.PlanBusiness)
	if err := s.DeleteByWorkspace("ws-del"); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("ws-del")
	if e.Plan != entitlements.PlanTrial {
		t.Errorf("expected default plan after delete, got %s", e.Plan)
	}
}
