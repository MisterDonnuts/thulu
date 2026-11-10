package evidence_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/evidence"
)

type fakeStarter struct {
	mu        sync.Mutex
	release   chan struct{}
	fail      error
	reqs      []evidence.SandboxRequest
	stopped   []string
	leftovers []string
}

func (f *fakeStarter) Owner(batchID string) string { return "evidence:" + batchID }

func (f *fakeStarter) Start(ctx context.Context, req evidence.SandboxRequest) (*evidence.SandboxApp, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	req.Progress(evidence.PhaseSetup)
	if f.release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.release:
		}
	}
	if f.fail != nil {
		return nil, f.fail
	}
	return &evidence.SandboxApp{Owner: f.Owner(req.BatchID), URL: "http://sandbox-evidence:3000/", Commit: "abc123", Port: 3000}, nil
}

func (f *fakeStarter) Stop(_ context.Context, owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, owner)
}

func (f *fakeStarter) Leftovers(context.Context) []string { return f.leftovers }

func (f *fakeStarter) stoppedOwner(owner string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.stopped, owner)
}

func (f *fakeStarter) requests() []evidence.SandboxRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

type sandboxRig struct {
	s       *evidence.Store
	svc     *evidence.Service
	l       *fakeLauncher
	k       *fakeKeys
	st      *fakeStarter
	caseRef *evidence.Case
	ctx     context.Context
}

func newSandboxRig(t *testing.T, st *fakeStarter) *sandboxRig {
	t.Helper()
	s, _ := newStores(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mustProject(t, s.DB(), "ws", "p1", "shop", "")
	c, err := s.CreateCase(ctx, "ws", "ana", evidence.Case{Title: "Shop", ProjectID: "p1", Take: mustTake(t), TakeSource: evidence.SourceUpload})
	if err != nil {
		t.Fatal(err)
	}
	r := &sandboxRig{s: s, l: &fakeLauncher{}, k: &fakeKeys{}, st: st, caseRef: c, ctx: ctx}
	r.svc = &evidence.Service{Store: s, Launcher: r.l, Keys: r.k, MaxRunners: 1}
	if st != nil {
		r.svc.Sandbox = st
	}
	return r
}

func (r *sandboxRig) start(t *testing.T) {
	t.Helper()
	sandboxEnv(t, r.s, "ws", "p1")
	r.svc.Start(r.ctx)
}

func (r *sandboxRig) batch(t *testing.T, id string) *evidence.Batch {
	t.Helper()
	b, err := r.s.GetBatch(r.ctx, "ws", id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (r *sandboxRig) caseUnmoved(t *testing.T) {
	t.Helper()
	c, err := r.s.GetCase(r.ctx, "ws", r.caseRef.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != r.caseRef.Status {
		t.Fatalf("the case moved from %s to %s", r.caseRef.Status, c.Status)
	}
}

func TestSandboxBatchRunsAgainstTheAppAndTearsDown(t *testing.T) {
	r := newSandboxRig(t, &fakeStarter{})
	r.start(t)
	b, err := r.svc.QueueSandbox(r.ctx, "ws", "ana", evidence.SandboxBatchSpec{ProjectID: "p1", Branch: "feature/login"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runner", func() bool { return r.l.count() == 1 })
	owner := r.st.Owner(b.ID)
	r.l.mu.Lock()
	spec, exit := r.l.started[0], r.l.exits[0]
	r.l.mu.Unlock()
	if spec.SandboxOwner != owner || spec.Batch.BaseURL != "http://sandbox-evidence:3000/" {
		t.Fatalf("runner spec: owner %q, url %q", spec.SandboxOwner, spec.Batch.BaseURL)
	}
	reqs := r.st.requests()
	if len(reqs) != 1 || reqs[0].ProjectID != "p1" || reqs[0].Branch != "feature/login" || reqs[0].WorkspaceID != "ws" {
		t.Fatalf("start requests: %+v", reqs)
	}
	got := r.batch(t, b.ID)
	if got.SandboxStatus != evidence.SandboxReady || got.BaseURL != "http://sandbox-evidence:3000" || got.SourceCommit != "abc123" {
		t.Fatalf("ready batch: %+v", got)
	}
	if r.st.stoppedOwner(owner) {
		t.Fatal("the sandbox went while the runner tested it")
	}
	exit("")
	waitFor(t, "the teardown", func() bool { return r.st.stoppedOwner(owner) })
	waitFor(t, "released", func() bool { return r.batch(t, b.ID).SandboxStatus == evidence.SandboxReleased })
	r.k.mu.Lock()
	defer r.k.mu.Unlock()
	if len(r.k.revoked) != 1 {
		t.Fatalf("runner key not revoked: %v", r.k.revoked)
	}
}

func TestSandboxStartFailureIsAnEnvironmentFailure(t *testing.T) {
	reason := evidence.SandboxReason(evidence.SandboxSetupFailed)
	r := newSandboxRig(t, &fakeStarter{fail: &evidence.SandboxFailure{Reason: reason, Log: "npm ERR! missing script"}})
	r.start(t)
	b, err := r.svc.QueueSandbox(r.ctx, "ws", "ana", evidence.SandboxBatchSpec{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure", func() bool { return r.batch(t, b.ID).Status == evidence.BatchError })
	got := r.batch(t, b.ID)
	if got.SandboxStatus != evidence.SandboxFailed || got.SandboxError != reason.String() || !strings.Contains(got.SandboxLog, "missing script") ||
		!strings.Contains(got.Error, "Couldn't bring the environment up") || got.Canceled != 1 || got.Errored != 0 {
		t.Fatalf("failed batch: %+v", got)
	}
	r.caseUnmoved(t)
	if !r.st.stoppedOwner(r.st.Owner(b.ID)) {
		t.Fatal("what came up of the sandbox was left behind")
	}
	if r.l.count() != 0 {
		t.Fatal("a runner started for an environment that never came up")
	}
	e := mustEnv(t, r.s, "ws", "Staging")
	if _, err := r.svc.Queue(r.ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the next batch's runner", func() bool { return r.l.count() == 1 })
}

func TestSandboxCancelWhileStartingTearsDown(t *testing.T) {
	r := newSandboxRig(t, &fakeStarter{release: make(chan struct{})})
	r.start(t)
	b, err := r.svc.QueueSandbox(r.ctx, "ws", "ana", evidence.SandboxBatchSpec{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return len(r.st.requests()) == 1 })
	waitFor(t, "the setup phase", func() bool { return r.batch(t, b.ID).SandboxPhase == evidence.PhaseSetup })
	if _, err := r.svc.CancelBatch(r.ctx, "ws", b.ID); err != nil {
		t.Fatal(err)
	}
	owner := r.st.Owner(b.ID)
	waitFor(t, "the teardown", func() bool { return r.st.stoppedOwner(owner) })
	waitFor(t, "released", func() bool { return r.batch(t, b.ID).SandboxStatus == evidence.SandboxReleased })
	waitFor(t, "finished", func() bool { return r.batch(t, b.ID).FinishedAt != nil })
	if got := r.batch(t, b.ID); got.Status != evidence.BatchCanceled || r.l.count() != 0 {
		t.Fatalf("canceled batch: %+v, runners %d", got, r.l.count())
	}
	r.caseUnmoved(t)
}

func TestSandboxBatchWithoutStarter(t *testing.T) {
	r := newSandboxRig(t, nil)
	r.start(t)
	b, err := r.svc.QueueSandbox(r.ctx, "ws", "ana", evidence.SandboxBatchSpec{ProjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure", func() bool { return r.batch(t, b.ID).Status == evidence.BatchError })
	if got := r.batch(t, b.ID); got.SandboxError != evidence.SandboxReason(evidence.SandboxUnavailable).String() {
		t.Fatalf("batch: %+v", got)
	}
	r.caseUnmoved(t)
}

func TestBootReapsEvidenceSandboxes(t *testing.T) {
	st := &fakeStarter{leftovers: []string{"evidence:orphan"}}
	r := newSandboxRig(t, st)
	b, err := r.s.CreateBatch(r.ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: sandboxEnv(t, r.s, "ws", "p1").ID})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.s.StartBatch(r.ctx, b.ID, ""); !ok {
		t.Fatal("start")
	}
	if err := r.s.MarkSandboxStarting(r.ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	r.svc.Start(r.ctx)
	got := r.batch(t, b.ID)
	if got.Status != evidence.BatchError || got.SandboxStatus != evidence.SandboxFailed || !strings.Contains(got.Error, "restarted") ||
		got.Canceled != 1 {
		t.Fatalf("interrupted batch: %+v", got)
	}
	r.caseUnmoved(t)
	for _, owner := range []string{st.Owner(b.ID), "evidence:orphan"} {
		if !st.stoppedOwner(owner) {
			t.Errorf("%s was not torn down at boot", owner)
		}
	}
}

func TestFailUnreachable(t *testing.T) {
	r := newSandboxRig(t, nil)
	r.start(t)
	e := mustEnv(t, r.s, "ws", "Staging")
	b, err := r.svc.Queue(r.ctx, "ws", "ana", evidence.BatchSpec{EnvironmentID: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runner", func() bool { return r.l.count() == 1 })
	if _, err := r.s.ClaimNextRun(r.ctx, "ws", b.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.FailUnreachable(r.ctx, "ws", b.ID, "fetch failed (ECONNREFUSED)"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.CloseBatch(r.ctx, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	got := r.batch(t, b.ID)
	if got.Status != evidence.BatchError || got.SandboxStatus != "" || got.SandboxError != "" ||
		!strings.Contains(got.Error, "ECONNREFUSED") || got.Canceled != 1 || got.Runs[0].Status != evidence.RunCanceled {
		t.Fatalf("unreachable batch: %+v", got)
	}
	r.caseUnmoved(t)
	if err := r.svc.FailUnreachable(r.ctx, "ws", b.ID, "again"); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("second report: %v", err)
	}
}
