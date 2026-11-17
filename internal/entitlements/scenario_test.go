package entitlements_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
)

// sim plays a workspace through days of work: agents ask AdmitRun before starting, as the
// dispatcher does, their pods run for a fixed time, and the meter samples every tick. It is the
// whole loop the daemon runs, minus Kubernetes.
type sim struct {
	t     *testing.T
	s     *entitlements.Store
	clk   *clock
	ws    string
	tick  time.Duration
	seq   int
	pods  []*simPod
	log   []simEvent
	limit int // agents at a time
}

type simPod struct {
	key        string
	start, end time.Time
	sandbox    bool
	reported   bool
}

type simEvent struct {
	at      time.Time
	ok      bool
	refusal entitlements.Refusal
	notice  int
	used    float64
}

func newSim(t *testing.T, plan entitlements.Plan, agents int, at time.Time) *sim {
	t.Helper()
	s, c := storeAt(t, entitlements.PlanTrial, at)
	m := &sim{t: t, s: s, clk: c, ws: "ws-sim", tick: 15 * time.Minute}
	if plan != entitlements.PlanTrial {
		if err := s.ApplyPlan(m.ws, plan, agents, 0); err != nil {
			t.Fatal(err)
		}
	}
	e, err := s.Get(m.ws)
	if err != nil {
		t.Fatal(err)
	}
	m.limit = e.ConcurrencyLimit
	return m
}

func (m *sim) now() time.Time { return m.clk.Now() }

func (m *sim) running() int {
	n := 0
	for _, p := range m.pods {
		if m.now().Before(p.end) {
			n++
		}
	}
	return n
}

// launch asks for one run of d and starts its pod when admitted.
func (m *sim) launch(d time.Duration) simEvent {
	m.t.Helper()
	a, err := m.s.AdmitRun(context.Background(), m.ws)
	if err != nil {
		m.t.Fatal(err)
	}
	ev := simEvent{at: m.now(), ok: a.OK, refusal: a.Refusal, notice: a.Notice, used: a.Usage.UsedHours}
	m.log = append(m.log, ev)
	if a.OK {
		m.seq++
		m.pods = append(m.pods, &simPod{key: fmt.Sprintf("pod-%d", m.seq), start: m.now(), end: m.now().Add(d)})
	}
	return ev
}

// sample is what the runtime lists: running pods, and each finished pod once more with its end.
func (m *sim) sample() []entitlements.PodSample {
	var out []entitlements.PodSample
	keep := m.pods[:0]
	for _, p := range m.pods {
		ps := entitlements.PodSample{Key: p.key, WorkspaceID: m.ws, StartedAt: p.start, Sandbox: p.sandbox}
		if !m.now().Before(p.end) {
			if p.reported {
				continue
			}
			ps.EndedAt = p.end
			p.reported = true
		}
		out = append(out, ps)
		keep = append(keep, p)
	}
	m.pods = keep
	return out
}

func (m *sim) meterNow() {
	m.t.Helper()
	if err := m.s.Meter(context.Background(), m.sample(), m.now()); err != nil {
		m.t.Fatal(err)
	}
}

// busy keeps every agent slot working on runs of d until the clock reaches until, metering each
// tick. Refused launches are retried every tick, as a board with work waiting would.
func (m *sim) busy(until time.Time, d time.Duration) {
	m.t.Helper()
	for m.now().Before(until) {
		for m.running() < m.limit {
			if !m.launch(d).ok {
				break
			}
		}
		m.clk.Add(m.tick)
		m.meterNow()
	}
}

// idle lets the clock pass with whatever is still running, metering each tick.
func (m *sim) idle(until time.Time) {
	m.t.Helper()
	for m.now().Before(until) {
		m.clk.Add(m.tick)
		m.meterNow()
	}
}

func (m *sim) usage() entitlements.HourUsage {
	m.t.Helper()
	u, err := m.s.HourUsage(context.Background(), m.ws)
	if err != nil {
		m.t.Fatal(err)
	}
	return u
}

func (m *sim) notices() []simEvent {
	var out []simEvent
	for _, e := range m.log {
		if e.notice > 0 {
			out = append(out, e)
		}
	}
	return out
}

func (m *sim) firstRefusal() (simEvent, bool) {
	for _, e := range m.log {
		if !e.ok {
			return e, true
		}
	}
	return simEvent{}, false
}

func (m *sim) report() []entitlements.ExtraHoursReport {
	m.t.Helper()
	if err := m.s.SetBillingCustomer(m.ws, "cus_sim"); err != nil {
		m.t.Fatal(err)
	}
	r := &recordingReporter{}
	if err := m.s.ReportClosedMonths(context.Background(), r); err != nil {
		m.t.Fatal(err)
	}
	return r.got
}

func day(d, h int) time.Time { return time.Date(2026, 12, d, h, 0, 0, 0, time.UTC) }

// A Starter workspace with both agents busy all month: notices at 80% and 100% once each, extra hours
// until the $10 default cap, then every launch is refused until January. The runs going when the cap
// hits finish; Stripe is billed for the cap and not a cent more.
func TestScenario_StarterBusyAllMonth(t *testing.T) {
	m := newSim(t, entitlements.PlanStarter, 0, day(1, 0))
	m.busy(day(31, 23), 3*time.Hour)

	n := m.notices()
	if len(n) != 2 || n[0].notice != 80 || n[1].notice != 100 {
		t.Fatalf("notices = %+v, want 80 then 100, once each", n)
	}
	if n[0].used < 160 || n[0].used >= 200 || n[1].used < 200 {
		t.Errorf("80 notice at %v h and 100 notice at %v h", n[0].used, n[1].used)
	}
	first, ok := m.firstRefusal()
	if !ok || first.refusal != entitlements.RefusedCap {
		t.Fatalf("first refusal = %+v, want the cap", first)
	}
	if first.used < 400 {
		t.Errorf("refused at %v h, before the cap (200 included + 200 extra)", first.used)
	}
	for _, e := range m.log {
		if e.at.After(first.at) && e.ok {
			t.Fatalf("launch admitted at %v after the cap was reached at %v", e.at, first.at)
		}
	}
	// Two runs of 3 h can be going when the cap hits: they finish and their time counts.
	dec, err := m.s.HourUsage(context.Background(), m.ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("December: %v h used, cap reached at %v (%v h), launches tried in the month: %d",
		dec.UsedHours, first.at.Format("Jan 2 15:04"), first.used, len(m.log))
	if dec.UsedHours < 400 || dec.UsedHours > 406 {
		t.Errorf("December used %v h, want 400 to 406 (at most 2 agents × 3 h past the cap)", dec.UsedHours)
	}
	m.idle(time.Date(2027, 1, 1, 0, 15, 0, 0, time.UTC))
	got := m.report()
	if len(got) != 1 || got[0].Period != "2026-12" || got[0].Hours != 200 {
		t.Fatalf("Stripe report = %+v, want 200 extra hours for 2026-12 ($10 cap)", got)
	}

	// January starts clean: admitted, and the notices come back at 80%.
	if ev := m.launch(3 * time.Hour); !ev.ok || ev.used != 0 {
		t.Fatalf("first January launch = %+v, want admitted with 0 h", ev)
	}
}

// The trial's 50 hours are for the whole 30 days. A trial that starts in December and runs out its
// hours does not get 50 more on January 1.
func TestScenario_TrialHoursDoNotResetWithTheMonth(t *testing.T) {
	m := newSim(t, entitlements.PlanTrial, 0, day(20, 9))
	m.busy(day(31, 23), 2*time.Hour)

	first, ok := m.firstRefusal()
	if !ok || first.refusal != entitlements.RefusedTrialHours {
		t.Fatalf("first refusal = %+v, want trial_hours (the trial has no extra hours)", first)
	}
	if u := m.usage(); u.UsedHours < 50 || u.UsedHours > 54 {
		t.Errorf("trial used %v h in December, want 50 to 54", u.UsedHours)
	}
	m.busy(time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC), 2*time.Hour)
	for _, e := range m.log {
		if e.at.Year() == 2027 && e.ok {
			t.Fatalf("trial admitted a run on %v with its 50 h already spent", e.at)
		}
	}
	if u := m.usage(); u.UsedHours < 50 || u.IncludedHours != 50 || !u.Trial || !u.ResetsAt.Equal(u.TrialEndsAt) {
		t.Errorf("January usage = %+v, want the trial's total against its 50 h, ending with the trial", u)
	}
	if len(m.report()) != 0 {
		t.Error("a trial never reports extra hours")
	}
}

// The trial ends on day 30 even with hours left, and moving to Starter lets runs start again.
func TestScenario_TrialEndsByDateThenUpgrade(t *testing.T) {
	m := newSim(t, entitlements.PlanTrial, 0, day(1, 9))
	m.launch(time.Hour)
	m.idle(day(31, 9))
	if ev := m.launch(time.Hour); ev.ok || ev.refusal != entitlements.RefusedTrialEnded {
		t.Fatalf("day 30 launch = %+v, want trial_ended", ev)
	}
	if err := m.s.ApplyPlan(m.ws, entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(time.Hour); !ev.ok {
		t.Fatalf("after moving to Starter = %+v, want admitted", ev)
	}
}

// Stripe cancels mid-month: launches stop at once, the run going finishes and is metered, and the
// month is billed with the terms it had while it was paid.
func TestScenario_CanceledMidMonth(t *testing.T) {
	m := newSim(t, entitlements.PlanStarter, 0, day(1, 0))
	m.busy(day(10, 0), 4*time.Hour) // 2 agents × 216 h = 432 h: past the cap
	m.busy(day(10, 0), 4*time.Hour)
	if err := m.s.SetExtraHoursCap(m.ws, 100); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(4 * time.Hour); !ev.ok {
		t.Fatalf("after raising the cap = %+v", ev)
	}
	m.idle(day(10, 1))
	if err := m.s.SetPlan(m.ws, entitlements.PlanInactive); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(time.Hour); ev.ok || ev.refusal != entitlements.RefusedPlanInactive {
		t.Fatalf("after cancel = %+v, want plan_inactive", ev)
	}
	before := m.usage().UsedHours
	m.idle(day(10, 6))
	after := m.usage().UsedHours
	if after-before < 2.9 || after-before > 3.1 {
		t.Errorf("the run going at cancel added %v h, want the 3 h it had left", after-before)
	}
	m.clk.Add(30 * 24 * time.Hour) // January
	got := m.report()
	want := int(math.Floor(after - 200))
	if len(got) != 1 || got[0].Hours != want {
		t.Fatalf("report = %+v, want %d extra hours billed with Starter's terms and the $100 cap", got, want)
	}
}

// Plan changes in the middle of a month take effect on the next launch.
func TestScenario_PlanChangesMidMonth(t *testing.T) {
	m := newSim(t, entitlements.PlanTeam, 4, day(1, 0))
	addHours(t, m.s, m.ws, "2026-12", 350)

	// Team with 4 agents (400 h) → Starter (200 h): 150 h are extra now, $7.50 of the $10 cap.
	if err := m.s.ApplyPlan(m.ws, entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(time.Hour); !ev.ok {
		t.Fatalf("downgrade with room under the cap = %+v", ev)
	}
	addHours(t, m.s, m.ws, "2026-12", 60)
	if ev := m.launch(time.Hour); ev.ok || ev.refusal != entitlements.RefusedCap {
		t.Fatalf("410 h on Starter = %+v, want the cap", ev)
	}

	// Starter at the cap → Team with 10 agents (1,000 h): runs start again, and the 80% notice comes
	// back when the workspace reaches 80% of the new hours.
	if err := m.s.ApplyPlan(m.ws, entitlements.PlanTeam, 10, 0); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(time.Hour); !ev.ok || ev.notice != 0 {
		t.Fatalf("after the upgrade = %+v, want admitted without a notice (41%%)", ev)
	}
	addHours(t, m.s, m.ws, "2026-12", 400)
	if ev := m.launch(time.Hour); !ev.ok || ev.notice != 80 {
		t.Fatalf("810 of 1,000 h = %+v, want the 80 notice again", ev)
	}
}

// The spending cap moves both ways during the month.
func TestScenario_CapChanges(t *testing.T) {
	m := newSim(t, entitlements.PlanTeam, 2, day(1, 0)) // 200 h, default cap $10
	addHours(t, m.s, m.ws, "2026-12", 300)              // $5 of extra hours
	if ev := m.launch(time.Hour); !ev.ok {
		t.Fatalf("$5 under a $10 cap = %+v", ev)
	}
	if err := m.s.SetExtraHoursCap(m.ws, 4); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(time.Hour); ev.ok || ev.refusal != entitlements.RefusedCap {
		t.Fatalf("cap lowered under what was spent = %+v, want the cap", ev)
	}
	if err := m.s.SetExtraHoursCap(m.ws, 0); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(time.Hour); ev.ok || ev.refusal != entitlements.RefusedHours {
		t.Fatalf("cap 0 past the included hours = %+v, want hours", ev)
	}
	if err := m.s.SetExtraHoursCap(m.ws, 25); err != nil {
		t.Fatal(err)
	}
	if ev := m.launch(time.Hour); !ev.ok {
		t.Fatalf("cap raised to $25 = %+v", ev)
	}
}

// A run admitted on December 31 that ends on January 1 counts in both months, and December's part is
// billed with December.
func TestScenario_RunAcrossNewYear(t *testing.T) {
	m := newSim(t, entitlements.PlanStarter, 0, day(31, 22))
	addHours(t, m.s, m.ws, "2026-12", 250)
	if ev := m.launch(4 * time.Hour); !ev.ok {
		t.Fatalf("launch on the 31st = %+v", ev)
	}
	m.idle(time.Date(2027, 1, 1, 6, 0, 0, 0, time.UTC))
	if u := m.usage(); u.Period != "2027-01" || u.UsedHours != 2 {
		t.Fatalf("January = %+v, want the 2 h of the run after midnight", u)
	}
	got := m.report()
	if len(got) != 1 || got[0].Period != "2026-12" || got[0].Hours != 52 {
		t.Fatalf("report = %+v, want 52 extra hours for December (250 + 2 − 200)", got)
	}
}

// If the daemon is down for a few hours, the pods that kept running are counted when it comes back.
// A pod that ended and was deleted while it was down loses only the time after the last tick.
func TestScenario_DaemonDownForHours(t *testing.T) {
	m := newSim(t, entitlements.PlanStarter, 0, day(5, 8))
	m.launch(10 * time.Hour)
	m.launch(2 * time.Hour)
	m.idle(day(5, 9))
	// Down from 09:00 to 13:00: no ticks. The 2 h pod ended at 10:00 and was deleted meanwhile.
	m.clk.Add(4 * time.Hour)
	m.pods = m.pods[:1]
	m.meterNow()
	if u := m.usage(); u.UsedHours != 6 {
		t.Fatalf("after the outage = %v h, want 6 (5 h of the long pod, 1 h of the short one)", u.UsedHours)
	}
	m.idle(day(5, 20))
	if u := m.usage(); u.UsedHours != 11 {
		t.Errorf("at the end = %v h, want 11 (10 + the 1 h sampled before the outage)", u.UsedHours)
	}
}

// Sandboxes count half: a day with one agent and one sandbox busy is 36 hours, not 48.
func TestScenario_SandboxHalf(t *testing.T) {
	m := newSim(t, entitlements.PlanStarter, 0, day(2, 0))
	m.launch(24 * time.Hour)
	m.pods = append(m.pods, &simPod{key: "sandbox", start: m.now(), end: m.now().Add(24 * time.Hour), sandbox: true})
	m.idle(day(3, 1))
	if u := m.usage(); u.UsedHours != 36 {
		t.Fatalf("one agent + one sandbox for a day = %v h, want 36", u.UsedHours)
	}
}

// The operator's workspace and self-hosted installs are never refused, whatever the meter says.
func TestScenario_NeverRefused(t *testing.T) {
	m := newSim(t, entitlements.PlanSelfHosted, 0, day(1, 0))
	addHours(t, m.s, m.ws, "2026-12", 5000)
	if ev := m.launch(time.Hour); !ev.ok {
		t.Fatalf("self-hosted = %+v", ev)
	}
	s, _ := storeAt(t, entitlements.PlanTrial, day(1, 0))
	s.ExemptWorkspace("ops")
	addHours(t, s, "ops", "2026-12", 5000)
	if a := admit(t, s, "ops"); !a.OK {
		t.Fatalf("exempt workspace = %+v", a)
	}
}
