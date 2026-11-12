package entitlements_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func hoursOf(t *testing.T, s *entitlements.Store, ws string) float64 {
	t.Helper()
	u, err := s.HourUsage(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	return u.UsedHours
}

func meter(t *testing.T, s *entitlements.Store, now time.Time, pods ...entitlements.PodSample) {
	t.Helper()
	if err := s.Meter(context.Background(), pods, now); err != nil {
		t.Fatal(err)
	}
}

func TestMeter_CountsEachSecondOnce(t *testing.T) {
	t0 := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	s, _ := storeAt(t, entitlements.PlanTrial, t0)
	agent := entitlements.PodSample{Key: "uid-a", WorkspaceID: "ws", StartedAt: t0}
	meter(t, s, t0.Add(30*time.Minute), agent)
	meter(t, s, t0.Add(30*time.Minute), agent)
	if got := hoursOf(t, s, "ws"); got != 0.5 {
		t.Fatalf("after a repeated tick = %v h, want 0.5", got)
	}
	meter(t, s, t0.Add(90*time.Minute), agent)
	if got := hoursOf(t, s, "ws"); got != 1.5 {
		t.Fatalf("after 90 min = %v h, want 1.5", got)
	}
	// The pod ended at minute 100; a later tick counts up to the end only.
	agent.EndedAt = t0.Add(100 * time.Minute)
	meter(t, s, t0.Add(3*time.Hour), agent)
	if got := hoursOf(t, s, "ws"); got != 1.67 {
		t.Fatalf("after the pod ended = %v h, want 1.67", got)
	}
}

func TestMeter_SandboxCountsHalfAndPendingCountsNothing(t *testing.T) {
	t0 := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	s, _ := storeAt(t, entitlements.PlanTrial, t0)
	meter(t, s, t0.Add(2*time.Hour),
		entitlements.PodSample{Key: "uid-sb", WorkspaceID: "ws", Sandbox: true, StartedAt: t0},
		entitlements.PodSample{Key: "uid-pending", WorkspaceID: "ws"},
		entitlements.PodSample{Key: "uid-orphan", StartedAt: t0},
	)
	if got := hoursOf(t, s, "ws"); got != 1 {
		t.Fatalf("2 h of sandbox = %v h, want 1", got)
	}
}

func TestMeter_RunAcrossMidnightSplitsMonths(t *testing.T) {
	t0 := time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)
	s, c := storeAt(t, entitlements.PlanTrial, t0)
	meter(t, s, t0.Add(3*time.Hour), entitlements.PodSample{Key: "uid-x", WorkspaceID: "ws", StartedAt: t0})
	if got := hoursOf(t, s, "ws"); got != 1 {
		t.Fatalf("December = %v h, want 1", got)
	}
	c.Add(2 * time.Hour)
	if got := hoursOf(t, s, "ws"); got != 2 {
		t.Fatalf("January = %v h, want 2", got)
	}
}

// A daemon restart opens a new store on the same database: the pod's counted time survives.
func TestMeter_SurvivesRestart(t *testing.T) {
	db := testpg.New(t)
	t0 := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	pod := entitlements.PodSample{Key: "uid-r", WorkspaceID: "ws", StartedAt: t0}
	for _, at := range []time.Duration{time.Hour, 2 * time.Hour} {
		s, err := entitlements.NewStore(db, entitlements.PlanTrial)
		if err != nil {
			t.Fatal(err)
		}
		s.SetClock(func() time.Time { return t0.Add(at) })
		meter(t, s, t0.Add(at), pod)
		if got := hoursOf(t, s, "ws"); got != at.Hours() {
			t.Fatalf("after restart at %s = %v h", at, got)
		}
	}
}

type stubSource struct {
	pods []entitlements.PodSample
	err  error
}

func (s stubSource) MeteredPods(context.Context) ([]entitlements.PodSample, error) {
	return s.pods, s.err
}

func TestRunMeter_StopsWithContextAndSurvivesListErrors(t *testing.T) {
	s := newStore(t, entitlements.PlanTrial)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunMeter(ctx, stubSource{err: errors.New("api down")}, 10*time.Millisecond, nil)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunMeter did not stop")
	}
}

type recordingReporter struct {
	got []entitlements.ExtraHoursReport
	err error
}

func (r *recordingReporter) ReportExtraHours(_ context.Context, rep entitlements.ExtraHoursReport) error {
	if r.err != nil {
		return r.err
	}
	r.got = append(r.got, rep)
	return nil
}

func TestReportClosedMonths(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	if err := s.ApplyPlan("ws-paid", entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBillingCustomer("ws-paid", "cus_123"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPlan("ws-nocust", entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPlan("ws-under", entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	addHours(t, s, "ws-paid", "2026-12", 237.9) // 37.9 extra → 37 billed
	addHours(t, s, "ws-nocust", "2026-12", 300) // nobody to charge yet
	addHours(t, s, "ws-under", "2026-12", 120)  // no extra hours
	addHours(t, s, "ws-paid", "2027-01", 500)   // current month: not closed
	r := &recordingReporter{}
	if err := s.ReportClosedMonths(ctx, r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 {
		t.Fatalf("reports = %+v, want one", r.got)
	}
	got := r.got[0]
	if got.Customer != "cus_123" || got.Hours != 37 || got.Period != "2026-12" ||
		!got.At.Equal(time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)) || got.Identifier == "" {
		t.Fatalf("report = %+v", got)
	}
	if err := s.ReportClosedMonths(ctx, r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 {
		t.Fatalf("a month was reported twice: %+v", r.got)
	}
	if err := s.SetBillingCustomer("ws-nocust", "cus_9"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportClosedMonths(ctx, r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 2 || r.got[1].Hours != 100 {
		t.Fatalf("late customer report = %+v, want 100 h", r.got)
	}
}

// The spending cap stops new runs; a run that finished past it is not billed beyond the cap.
func TestReportClosedMonths_NeverPastTheCap(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC))
	if err := s.ApplyPlan("ws", entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExtraHoursCap("ws", 10); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBillingCustomer("ws", "cus_1"); err != nil {
		t.Fatal(err)
	}
	addHours(t, s, "ws", "2026-12", 200+250)
	r := &recordingReporter{}
	if err := s.ReportClosedMonths(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 || r.got[0].Hours != 200 {
		t.Fatalf("report = %+v, want 200 h ($10 / $0.05)", r.got)
	}
}

func TestReportClosedMonths_RetriesAfterProviderError(t *testing.T) {
	s, _ := storeAt(t, entitlements.PlanTrial, time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC))
	_ = s.ApplyPlan("ws", entitlements.PlanStarter, 0, 0)
	_ = s.SetBillingCustomer("ws", "cus_1")
	addHours(t, s, "ws", "2026-12", 210)
	r := &recordingReporter{err: errors.New("stripe 500")}
	if err := s.ReportClosedMonths(context.Background(), r); err == nil {
		t.Fatal("provider error swallowed")
	}
	r.err = nil
	if err := s.ReportClosedMonths(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 || r.got[0].Hours != 10 {
		t.Fatalf("retry = %+v", r.got)
	}
}

// A workspace that cancels before the month closes is billed by the plan it had while it ran.
func TestReportClosedMonths_CanceledWorkspaceKeepsItsMonthTerms(t *testing.T) {
	s, c := storeAt(t, entitlements.PlanTrial, time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC))
	_ = s.ApplyPlan("ws", entitlements.PlanStarter, 0, 0)
	_ = s.SetBillingCustomer("ws", "cus_1")
	addHours(t, s, "ws", "2026-12", 230)
	if err := s.SetPlan("ws", entitlements.PlanInactive); err != nil {
		t.Fatal(err)
	}
	c.Add(15 * 24 * time.Hour)
	r := &recordingReporter{}
	if err := s.ReportClosedMonths(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 || r.got[0].Hours != 30 {
		t.Fatalf("reports = %+v, want 30 extra hours on Starter terms", r.got)
	}
}
