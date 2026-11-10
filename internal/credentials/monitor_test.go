package credentials

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeProvider struct {
	id    string
	res   Introspection
	err   error
	calls int
}

func (f *fakeProvider) Spec() ProviderSpec {
	return ProviderSpec{ID: f.id, Label: f.id, Kinds: []KindSpec{{ID: "aws_keys"}}}
}

func (f *fakeProvider) Introspect(context.Context, string, map[string]string, []string) (Introspection, error) {
	f.calls++
	return f.res, f.err
}

type monitorFixture struct {
	store *Store
	prov  *fakeProvider
	mon   *Monitor
	cred  *Credential
}

func newMonitorFixture(t *testing.T) *monitorFixture {
	t.Helper()
	s := newTestStore(t)
	prov := &fakeProvider{id: "aws"}
	return &monitorFixture{
		store: s,
		prov:  prov,
		cred:  mustCreate(t, s, "ws1", awsInput("prod-aws")),
		mon:   NewMonitor(s, &Registry{byID: map[string]Provider{"aws": prov}}, NewCatalog().Permissions()),
	}
}

func (f *monitorFixture) valid(t *testing.T, expires time.Time, caps ...string) {
	t.Helper()
	if err := f.store.SetIntrospection(context.Background(), "ws1", f.cred.ID, Introspection{
		State: StateValid, Identity: "arn:x", Capabilities: caps, ExpiresAt: expires,
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
}

func (f *monitorFixture) backdateCheck(t *testing.T, id string, by time.Duration) {
	t.Helper()
	f.exec(t, `UPDATE credentials SET validated_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-by), id)
}

func (f *monitorFixture) setExpiry(t *testing.T, id string, at time.Time) {
	t.Helper()
	f.exec(t, `UPDATE credentials SET expires_at = ? WHERE id = ?`, at.UTC(), id)
}

func (f *monitorFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.store.db.ExecContext(context.Background(), f.store.rb(q), args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (f *monitorFixture) reload(t *testing.T) *Credential {
	t.Helper()
	c, err := f.store.Get(context.Background(), "ws1", f.cred.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return c
}

func TestMonitor_ReportsTheExpiryCrossing(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Now().Add(30*24*time.Hour), "aws:logs:StartQuery")

	if changes := f.mon.Sweep(context.Background()); len(changes) != 0 {
		t.Fatalf("first sweep on a healthy credential reported %+v, want nothing", changes)
	}

	f.setExpiry(t, f.cred.ID, time.Now().Add(-time.Minute))
	changes := f.mon.Sweep(context.Background())
	if len(changes) != 1 {
		t.Fatalf("got %d changes after expiry, want 1: %+v", len(changes), changes)
	}
	if changes[0].From != StateValid || changes[0].To != StateExpired {
		t.Errorf("transition = %s→%s, want valid→expired", changes[0].From, changes[0].To)
	}
	if !changes[0].Degraded() {
		t.Error("an expiry is not reported as a degradation")
	}
}

func TestMonitor_ReportsATransitionOnlyOnce(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Now().Add(30*24*time.Hour), "aws:logs:StartQuery")
	f.mon.Sweep(context.Background())

	f.setExpiry(t, f.cred.ID, time.Now().Add(-time.Minute))
	if n := len(f.mon.Sweep(context.Background())); n != 1 {
		t.Fatalf("first post-expiry sweep reported %d changes, want 1", n)
	}
	if n := len(f.mon.Sweep(context.Background())); n != 0 {
		t.Errorf("second post-expiry sweep reported %d changes, want 0", n)
	}
}

func TestMonitor_FirstSweepReportsAnAlreadyDeadCredential(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Now().Add(-time.Hour), "aws:logs:StartQuery")

	changes := f.mon.Sweep(context.Background())
	if len(changes) != 1 {
		t.Fatalf("got %d changes on a cold start over a dead credential, want 1", len(changes))
	}
	if !changes[0].First || changes[0].To != StateExpired {
		t.Errorf("got %+v, want a first-observation report of expired", changes[0])
	}
}

func TestMonitor_FirstSweepStaysQuietAboutHealthyCredentials(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Time{}, "aws:logs:StartQuery")
	if changes := f.mon.Sweep(context.Background()); len(changes) != 0 {
		t.Errorf("cold start over a healthy credential reported %+v, want nothing", changes)
	}
}

func TestMonitor_RespectsTheStalenessWindow(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Time{}, "aws:logs:StartQuery")
	f.mon.WithTimings(0, 6*time.Hour, 0)

	f.backdateCheck(t, f.cred.ID, time.Hour)
	f.mon.Sweep(context.Background())
	if f.prov.calls != 0 {
		t.Fatalf("provider called %d times an hour after a check, want 0", f.prov.calls)
	}

	f.backdateCheck(t, f.cred.ID, 7*time.Hour)
	f.mon.Sweep(context.Background())
	if f.prov.calls != 1 {
		t.Errorf("provider called %d times past the staleness window, want 1", f.prov.calls)
	}
}

func TestMonitor_DiscoversAProviderRevocation(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Time{}, "aws:logs:StartQuery")
	f.mon.WithTimings(0, time.Hour, 0)
	f.backdateCheck(t, f.cred.ID, 2*time.Hour)
	f.prov.res = Introspection{State: StateInvalid, Err: "InvalidClientTokenId"}

	changes := f.mon.Sweep(context.Background())
	if len(changes) != 1 || changes[0].To != StateInvalid {
		t.Fatalf("got %+v, want one valid→invalid change", changes)
	}
	if got := f.reload(t); got.State != StateInvalid {
		t.Errorf("stored state = %q, want invalid", got.State)
	}
}

func TestMonitor_UnreachableProviderDoesNotWidenTheCredential(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Time{}, "aws:logs:StartQuery")
	f.mon.WithTimings(0, time.Hour, 0)
	f.backdateCheck(t, f.cred.ID, 2*time.Hour)
	f.prov.err = errors.New("dial tcp: i/o timeout")

	f.mon.Sweep(context.Background())

	got := f.reload(t)
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "aws:logs:StartQuery" {
		t.Fatalf("capabilities after a failed check = %v, want them untouched", got.Capabilities)
	}
	if got.State != StateValid {
		t.Errorf("state after a failed check = %q, want the previous verdict (valid)", got.State)
	}
	if got.HasCapability("aws:ecs:UpdateService") {
		t.Error("a failed check widened the credential to permissions it never had")
	}
	if got.IntrospectError != IntrospectUnreachable {
		t.Errorf("introspect error after a failed check = %q, want %q", got.IntrospectError, IntrospectUnreachable)
	}
}

func TestMonitor_SkipsDisabledCredentials(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Time{}, "aws:logs:StartQuery")
	if err := f.store.SetDisabled(context.Background(), "ws1", f.cred.ID, true); err != nil {
		t.Fatalf("SetDisabled: %v", err)
	}
	f.mon.WithTimings(0, time.Hour, 0)
	f.backdateCheck(t, f.cred.ID, 2*time.Hour)

	f.mon.Sweep(context.Background())
	if f.prov.calls != 0 {
		t.Errorf("provider called %d times for a disabled credential, want 0", f.prov.calls)
	}
}

func TestMonitor_BoundsRevalidationsPerSweep(t *testing.T) {
	f := newMonitorFixture(t)
	for _, name := range []string{"b", "c", "d"} {
		mustCreate(t, f.store, "ws1", awsInput(name))
	}
	f.mon.WithTimings(0, 0, 2)
	f.prov.res = Introspection{State: StateValid}

	f.mon.Sweep(context.Background())
	if f.prov.calls != 2 {
		t.Fatalf("sweep made %d provider calls with a batch of 2, want 2", f.prov.calls)
	}
	f.mon.Sweep(context.Background())
	if f.prov.calls != 4 {
		t.Errorf("second sweep did not drain the backlog: %d calls total, want 4", f.prov.calls)
	}
}

func TestMonitor_SweepsEveryWorkspace(t *testing.T) {
	f := newMonitorFixture(t)
	f.valid(t, time.Time{}, "aws:logs:StartQuery")
	other := mustCreate(t, f.store, "ws2", awsInput("other-aws"))
	if err := f.store.SetIntrospection(context.Background(), "ws2", other.ID, Introspection{
		State: StateValid, ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("SetIntrospection: %v", err)
	}
	f.mon.WithTimings(0, 24*time.Hour, 0)

	var seen []string
	for _, ch := range f.mon.Sweep(context.Background()) {
		seen = append(seen, ch.Credential.WorkspaceID)
	}
	if len(seen) != 1 || seen[0] != "ws2" {
		t.Errorf("cross-workspace sweep reported %v, want the expired ws2 credential", seen)
	}
}

func TestMonitor_OffMakesNoProviderCalls(t *testing.T) {
	t.Setenv("OCTOPUS_CREDENTIAL_MONITOR", "off")
	f := newMonitorFixture(t)
	f.valid(t, time.Now().Add(30*24*time.Hour), "aws:logs:StartQuery")
	f.mon.WithTimings(0, time.Hour, 0)
	f.backdateCheck(t, f.cred.ID, 2*time.Hour)
	f.prov.res = Introspection{State: StateInvalid, Err: "InvalidClientTokenId"}

	f.mon.Sweep(context.Background())
	if f.prov.calls != 0 {
		t.Fatalf("provider called %d time(s) with the monitor off", f.prov.calls)
	}

	f.setExpiry(t, f.cred.ID, time.Now().Add(-time.Minute))
	changes := f.mon.Sweep(context.Background())
	if len(changes) != 1 || changes[0].To != StateExpired {
		t.Errorf("got %+v, want the clock-driven valid→expired change", changes)
	}
	if f.prov.calls != 0 {
		t.Errorf("provider called %d time(s) with the monitor off", f.prov.calls)
	}
}

func TestMonitorOutbound(t *testing.T) {
	for v, want := range map[string]bool{
		"": true, "on": true, "1": true, "off": false, " OFF ": false, "0": false, "false": false,
	} {
		if got := monitorOutbound(func(string) string { return v }); got != want {
			t.Errorf("OCTOPUS_CREDENTIAL_MONITOR=%q: outbound = %v, want %v", v, got, want)
		}
	}
}
