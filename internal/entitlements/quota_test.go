package entitlements_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestStorageQuota_PlanDefaultsAndOverride(t *testing.T) {
	s := newStore(t, entitlements.PlanTrial)
	f, _ := s.Get("ws-trial")
	if f.StorageQuotaBytes != 2*entitlements.GiB {
		t.Errorf("trial storage quota = %d, want 2 GiB", f.StorageQuotaBytes)
	}
	custom := entitlements.DefaultFor(entitlements.PlanStarter)
	custom.WorkspaceID = "ws-big"
	custom.StorageQuotaBytes = 5 * entitlements.GiB
	if err := s.Set(custom); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("ws-big"); got.StorageQuotaBytes != 5*entitlements.GiB {
		t.Errorf("custom storage quota = %d, want 5 GiB", got.StorageQuotaBytes)
	}
	custom.StorageQuotaBytes = 0
	if err := s.Set(custom); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("ws-big"); got.StorageQuotaBytes != 0 {
		t.Errorf("explicit unlimited storage quota = %d, want 0", got.StorageQuotaBytes)
	}
}

// Rows written before agent hours carry no hour quota: they get their plan's hours for the agents
// they already allow.
func TestLegacyRowFallsBackToPlan(t *testing.T) {
	db := testpg.New(t)
	s, err := entitlements.NewStore(db, entitlements.PlanTrial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_entitlements
		(workspace_id, plan, seat_limit, concurrency_limit, run_quota_monthly, features, updated_at)
		VALUES ('ws-old', 'team', 20, 5, 1000, '[]', NOW())`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("ws-old")
	if got.StorageQuotaBytes != 50*entitlements.GiB || got.HourQuotaMonthly != 500 {
		t.Errorf("legacy team row = %+v, want 50 GiB and 500 h for its 5 agents", got)
	}
	if got.ExtraCap() != 5*entitlements.DefaultExtraCapPerAgentUSD {
		t.Errorf("legacy row cap = %v, want the default for its 5 agents", got.ExtraCap())
	}
}

func TestForPlan_CapacityFollowsAgentsAndNodes(t *testing.T) {
	cases := []struct {
		name                 string
		plan                 entitlements.Plan
		agents, nodes        int
		wantAgents, wantHour int
		wantStorage          int64
	}{
		{"trial", entitlements.PlanTrial, 0, 0, 2, 50, 2},
		{"starter", entitlements.PlanStarter, 0, 0, 2, 200, 20},
		{"starter with a third agent", entitlements.PlanStarter, 3, 0, 3, 300, 30},
		{"team with 8 agents", entitlements.PlanTeam, 8, 0, 8, 800, 80},
		{"enterprise on 25 nodes", entitlements.PlanEnterprise, 0, 25, 200, 0, 2500},
		{"enterprise on the shared cluster", entitlements.PlanEnterprise, 12, 0, 12, 1200, 120},
		{"self-hosted", entitlements.PlanSelfHosted, 0, 0, 0, 0, 0},
	}
	for _, c := range cases {
		e := entitlements.ForPlan(c.plan, c.agents, c.nodes)
		if e.ConcurrencyLimit != c.wantAgents || e.HourQuotaMonthly != c.wantHour ||
			e.StorageQuotaBytes != c.wantStorage*entitlements.GiB {
			t.Errorf("%s: got %d agents, %d h, %d bytes", c.name, e.ConcurrencyLimit, e.HourQuotaMonthly, e.StorageQuotaBytes)
		}
	}
	if e := entitlements.ForPlan(entitlements.PlanStarter, 0, 0); hasFeature(e, "audit_export") {
		t.Error("Starter should not export the audit log, like Community")
	}
}

func hasFeature(e entitlements.Entitlement, f string) bool {
	for _, x := range e.Features {
		if x == f {
			return true
		}
	}
	return false
}

func TestExemptWorkspace_ResolvesUnlimited(t *testing.T) {
	s := newStore(t, entitlements.PlanTrial).ExemptWorkspace("ws-op")
	op, _ := s.Get("ws-op")
	if op.Plan != entitlements.PlanSelfHosted || op.HourQuotaMonthly != 0 || op.ConcurrencyLimit != 0 {
		t.Errorf("operator workspace should be unlimited, got %+v", op)
	}
	other, _ := s.Get("ws-tenant")
	if other.Plan != entitlements.PlanTrial {
		t.Errorf("tenant should stay on the trial, got %s", other.Plan)
	}
	if err := s.SetPlan("ws-op", entitlements.PlanTeam); err != nil {
		t.Fatal(err)
	}
	if op, _ = s.Get("ws-op"); op.Plan != entitlements.PlanTeam {
		t.Errorf("explicit operator plan ignored, got %s", op.Plan)
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func storeAt(t *testing.T, def entitlements.Plan, at time.Time) (*entitlements.Store, *clock) {
	t.Helper()
	s := newStore(t, def)
	c := &clock{now: at}
	s.SetClock(c.Now)
	return s, c
}

func addHours(t *testing.T, s *entitlements.Store, ws, period string, h float64) {
	t.Helper()
	if err := s.AddSeconds(context.Background(), ws, period, int64(h*3600)); err != nil {
		t.Fatal(err)
	}
}

func admit(t *testing.T, s *entitlements.Store, ws string) entitlements.Admission {
	t.Helper()
	a, err := s.AdmitRun(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAdmitRun_TrialStartsOnFirstRunAndEnds(t *testing.T) {
	start := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	s, c := storeAt(t, entitlements.PlanTrial, start)
	if a := admit(t, s, "ws-t"); !a.OK {
		t.Fatalf("first run refused: %+v", a)
	}
	e, _ := s.Get("ws-t")
	if want := start.AddDate(0, 0, 30); !e.TrialEndsAt.Equal(want) {
		t.Fatalf("trial ends %s, want %s", e.TrialEndsAt, want)
	}
	c.Add(29 * 24 * time.Hour)
	if a := admit(t, s, "ws-t"); !a.OK {
		t.Fatalf("day 29 refused: %+v", a)
	}
	c.Add(48 * time.Hour)
	a := admit(t, s, "ws-t")
	if a.OK || a.Refusal != entitlements.RefusedTrialEnded {
		t.Fatalf("after 30 days = %+v, want trial_ended", a)
	}
	if a.Err() == nil {
		t.Fatal("a refused admission must explain itself")
	}
}

func TestAdmitRun_TrialHasNoExtraHours(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC))
	addHours(t, s, "ws-t", "2026-12", 50)
	if a := admit(t, s, "ws-t"); a.OK || a.Refusal != entitlements.RefusedTrialHours {
		t.Fatalf("trial past 50 h = %+v, want refused for the trial's hours", a)
	}
}

func TestAdmitRun_ExtraHoursUntilTheCap(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC))
	if err := s.ApplyPlan("ws-s", entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	addHours(t, s, "ws-s", "2026-12", 159)
	if a := admit(t, s, "ws-s"); !a.OK || a.Notice != 0 {
		t.Fatalf("79.5%% = %+v, want admitted without notice", a)
	}
	addHours(t, s, "ws-s", "2026-12", 1)
	if a := admit(t, s, "ws-s"); !a.OK || a.Notice != 80 {
		t.Fatalf("80%% = %+v, want the 80 notice", a)
	}
	if a := admit(t, s, "ws-s"); a.Notice != 0 {
		t.Fatalf("80 notice repeated: %+v", a)
	}
	addHours(t, s, "ws-s", "2026-12", 40)
	a := admit(t, s, "ws-s")
	if !a.OK || a.Notice != 100 {
		t.Fatalf("200 h = %+v, want admitted with the 100 notice", a)
	}
	// Starter's default cap is $5 per agent, $10 for its 2: 200 extra hours at $0.05.
	addHours(t, s, "ws-s", "2026-12", 199)
	if a = admit(t, s, "ws-s"); !a.OK {
		t.Fatalf("199 extra hours = %+v, want admitted", a)
	}
	if a.Usage.ExtraHours != 199 || a.Usage.ExtraUSD != 9.95 || a.Usage.CapUSD != 10 {
		t.Fatalf("usage = %+v", a.Usage)
	}
	addHours(t, s, "ws-s", "2026-12", 1)
	if a = admit(t, s, "ws-s"); a.OK || a.Refusal != entitlements.RefusedCap {
		t.Fatalf("at the cap = %+v, want refused for the cap", a)
	}
	if err := s.SetExtraHoursCap("ws-s", 100); err != nil {
		t.Fatal(err)
	}
	if a = admit(t, s, "ws-s"); !a.OK {
		t.Fatalf("after raising the cap = %+v", a)
	}
}

func TestAdmitRun_NoCapStopsAtIncludedHours(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC))
	if err := s.ApplyPlan("ws-n", entitlements.PlanTeam, 4, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExtraHoursCap("ws-n", 0); err != nil {
		t.Fatal(err)
	}
	addHours(t, s, "ws-n", "2026-12", 400)
	if a := admit(t, s, "ws-n"); a.OK || a.Refusal != entitlements.RefusedHours {
		t.Fatalf("cap 0 at 400 h = %+v, want refused for hours", a)
	}
}

func TestAdmitRun_MonthRollsOver(t *testing.T) {
	s, c := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC))
	if err := s.ApplyPlan("ws-m", entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExtraHoursCap("ws-m", 0); err != nil {
		t.Fatal(err)
	}
	addHours(t, s, "ws-m", "2026-12", 200)
	if a := admit(t, s, "ws-m"); a.OK {
		t.Fatal("spent month admitted")
	}
	c.Add(2 * time.Hour)
	a := admit(t, s, "ws-m")
	if !a.OK || a.Usage.Period != "2027-01" || a.Usage.UsedHours != 0 {
		t.Fatalf("new month = %+v", a)
	}
	if !a.Usage.ResetsAt.Equal(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("resets_at = %s", a.Usage.ResetsAt)
	}
}

func TestAdmitRun_InactiveAndSelfHosted(t *testing.T) {
	s := newStore(t, entitlements.PlanSelfHosted)
	addHours(t, s, "ws-sh", "2026-12", 100000)
	if a := admit(t, s, "ws-sh"); !a.OK {
		t.Fatalf("self-hosted refused: %+v", a)
	}
	if err := s.SetPlan("ws-off", entitlements.PlanInactive); err != nil {
		t.Fatal(err)
	}
	if a := admit(t, s, "ws-off"); a.OK || a.Refusal != entitlements.RefusedPlanInactive {
		t.Fatalf("inactive = %+v", a)
	}
}

// 130 launches cross 80% together: exactly one carries the notice.
func TestAdmitRun_NoticeOnceUnderConcurrency(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC))
	if err := s.ApplyPlan("ws-c", entitlements.PlanTeam, 2, 0); err != nil {
		t.Fatal(err)
	}
	addHours(t, s, "ws-c", "2026-12", 170)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		notices int
	)
	for i := 0; i < 130; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := s.AdmitRun(context.Background(), "ws-c")
			if err == nil && a.Notice == 80 {
				mu.Lock()
				notices++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if notices != 1 {
		t.Fatalf("%d launches carried the 80 notice, want 1", notices)
	}
}

func TestApplyPlan_KeepsCapAndTrialEnd(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	admit(t, s, "ws-a")
	before, _ := s.Get("ws-a")
	if err := s.ApplyPlan("ws-a", entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExtraHoursCap("ws-a", 12.5); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPlan("ws-a", entitlements.PlanTeam, 6, 0); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("ws-a")
	if e.ExtraCap() != 12.5 || !e.TrialEndsAt.Equal(before.TrialEndsAt) || e.ConcurrencyLimit != 6 {
		t.Fatalf("after plan change = %+v", e)
	}
	if err := s.SetExtraHoursCap("ws-a", -1); err == nil {
		t.Fatal("negative cap accepted")
	}
}

func TestDeleteByWorkspace_DropsHourCounters(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	addHours(t, s, "ws-gone", "2026-12", 3)
	if err := s.DeleteByWorkspace("ws-gone"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.HourUsage(context.Background(), "ws-gone"); u.UsedHours != 0 {
		t.Fatalf("hour counter survived erasure: %+v", u)
	}
}
