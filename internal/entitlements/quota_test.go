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
	s := newStore(t, entitlements.PlanFree)
	f, _ := s.Get("ws-free")
	if f.StorageQuotaBytes != 2*entitlements.GiB {
		t.Errorf("free storage quota = %d, want 2 GiB", f.StorageQuotaBytes)
	}
	custom := entitlements.DefaultFor(entitlements.PlanFree)
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

func TestStorageQuota_LegacyRowFallsBackToPlan(t *testing.T) {
	db := testpg.New(t)
	s, err := entitlements.NewStore(db, entitlements.PlanFree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_entitlements
		(workspace_id, plan, seat_limit, concurrency_limit, run_quota_monthly, features, updated_at)
		VALUES ('ws-old', 'team', 20, 5, 1000, '[]', NOW())`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("ws-old"); got.StorageQuotaBytes != 20*entitlements.GiB {
		t.Errorf("legacy team row storage quota = %d, want 20 GiB", got.StorageQuotaBytes)
	}
}

func TestExemptWorkspace_ResolvesUnlimited(t *testing.T) {
	s := newStore(t, entitlements.PlanFree).ExemptWorkspace("ws-op")
	op, _ := s.Get("ws-op")
	if op.Plan != entitlements.PlanSelfHosted || op.RunQuotaMonthly != 0 || op.ConcurrencyLimit != 0 {
		t.Errorf("operator workspace should be unlimited, got %+v", op)
	}
	other, _ := s.Get("ws-tenant")
	if other.Plan != entitlements.PlanFree {
		t.Errorf("tenant should stay on free, got %s", other.Plan)
	}
	if err := s.SetPlan("ws-op", entitlements.PlanTeam); err != nil {
		t.Fatal(err)
	}
	if op, _ = s.Get("ws-op"); op.Plan != entitlements.PlanTeam {
		t.Errorf("explicit operator plan ignored, got %s", op.Plan)
	}
}

func TestConsumeRun_StopsAtQuota(t *testing.T) {
	s := newStore(t, entitlements.PlanFree)
	ctx := context.Background()
	custom := entitlements.DefaultFor(entitlements.PlanFree)
	custom.WorkspaceID = "ws-q"
	custom.RunQuotaMonthly = 3
	if err := s.Set(custom); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		u, ok, err := s.ConsumeRun(ctx, "ws-q")
		if err != nil || !ok {
			t.Fatalf("run %d refused: ok=%v err=%v", i, ok, err)
		}
		if u.Used != i || u.Remaining != 3-i {
			t.Fatalf("run %d usage = %+v", i, u)
		}
	}
	u, ok, err := s.ConsumeRun(ctx, "ws-q")
	if err != nil || ok {
		t.Fatalf("fourth run allowed: ok=%v err=%v", ok, err)
	}
	if u.Remaining != 0 || u.Used != 3 {
		t.Fatalf("spent usage = %+v", u)
	}
	if err := s.RefundRun(ctx, "ws-q", u.Period); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ConsumeRun(ctx, "ws-q"); !ok {
		t.Fatal("run after refund refused")
	}
}

func TestConsumeRun_Concurrent(t *testing.T) {
	s := newStore(t, entitlements.PlanFree)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 130; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := s.ConsumeRun(ctx, "ws-race"); err == nil && ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 100 {
		t.Fatalf("allowed %d runs, want exactly 100", allowed)
	}
}

func TestConsumeRun_MonthRollsOver(t *testing.T) {
	s := newStore(t, entitlements.PlanFree)
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	custom := entitlements.DefaultFor(entitlements.PlanFree)
	custom.WorkspaceID = "ws-m"
	custom.RunQuotaMonthly = 1
	if err := s.Set(custom); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ConsumeRun(ctx, "ws-m"); !ok {
		t.Fatal("first run refused")
	}
	if _, ok, _ := s.ConsumeRun(ctx, "ws-m"); ok {
		t.Fatal("second run in the same month allowed")
	}
	now = now.Add(2 * time.Hour)
	u, ok, _ := s.ConsumeRun(ctx, "ws-m")
	if !ok || u.Period != "2026-11" {
		t.Fatalf("new month refused or wrong period: ok=%v %+v", ok, u)
	}
	if !u.ResetsAt.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("resets_at = %s", u.ResetsAt)
	}
}

func TestConsumeRun_UnlimitedStillCounts(t *testing.T) {
	s := newStore(t, entitlements.PlanSelfHosted)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, ok, err := s.ConsumeRun(ctx, "ws-sh"); !ok || err != nil {
			t.Fatalf("unlimited run refused: %v", err)
		}
	}
	u, err := s.RunUsage(ctx, "ws-sh")
	if err != nil {
		t.Fatal(err)
	}
	if u.Used != 3 || u.Remaining != -1 || u.Limit != 0 {
		t.Fatalf("unlimited usage = %+v", u)
	}
}

func TestDeleteByWorkspace_DropsRunCounters(t *testing.T) {
	s := newStore(t, entitlements.PlanFree)
	ctx := context.Background()
	if _, _, err := s.ConsumeRun(ctx, "ws-gone"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteByWorkspace("ws-gone"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.RunUsage(ctx, "ws-gone"); u.Used != 0 {
		t.Fatalf("run counter survived erasure: %+v", u)
	}
}
