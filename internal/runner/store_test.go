package runner

import (
	"context"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func register(t *testing.T, s *Store, ws, name string) *RegisterResponse {
	t.Helper()
	ctx := context.Background()
	tok, _, err := s.CreateRegistration(ctx, ws, name, "admin@test")
	if err != nil {
		t.Fatalf("CreateRegistration: %v", err)
	}
	resp, err := s.Register(ctx, RegisterRequest{Token: tok, Hostname: "mac"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return resp
}

func enqueue(t *testing.T, s *Store, runnerID, ws, project string) *Job {
	t.Helper()
	j := &Job{RunnerID: runnerID, WorkspaceID: ws, TaskID: "task1", ProjectID: project,
		InstanceID: "dev-task-task1-in_progress-1", AgentName: "dev", Stage: "in_progress",
		Spec: JobSpec{InstanceID: "dev-task-task1-in_progress-1", TaskID: "task1",
			Env: map[string]string{"OCTOPUS_API_KEY": "okey_secret"}}}
	if err := s.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return j
}

func TestRegistrationIsSpentOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tok, _, err := s.CreateRegistration(ctx, "ws1", "laptop", "admin@test")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Register(ctx, RegisterRequest{Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if resp.WorkspaceID != "ws1" || resp.Name != "laptop" || resp.Credential == "" {
		t.Fatalf("Register = %+v", resp)
	}
	if _, err := s.Register(ctx, RegisterRequest{Token: tok}); apperr.KindOf(err) != apperr.KindUnauthenticated {
		t.Fatalf("second Register err = %v, want unauthenticated", err)
	}
	r, err := s.Authenticate(ctx, resp.Credential, "1.0")
	if err != nil || r.ID != resp.RunnerID {
		t.Fatalf("Authenticate = %+v, %v", r, err)
	}
	if _, err := s.Authenticate(ctx, "orn_nope", ""); apperr.KindOf(err) != apperr.KindUnauthenticated {
		t.Fatalf("Authenticate(bad) err = %v", err)
	}
}

func TestExpiredRegistrationIsRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tok, _, err := s.CreateRegistration(ctx, "ws1", "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Now().UTC().Add(2 * RegistrationTTL) }
	if _, err := s.Register(ctx, RegisterRequest{Token: tok}); err == nil {
		t.Fatal("an expired token registered a runner")
	}
}

func TestClaimHandsOnlyMappedProjectsAndDropsEnv(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	j := enqueue(t, s, rn.RunnerID, "ws1", "projA")

	spec, err := s.Claim(ctx, rn.RunnerID, []string{"projB"}, time.Minute)
	if err != nil || spec != nil {
		t.Fatalf("Claim(projB) = %+v, %v; want nothing", spec, err)
	}
	spec, err = s.Claim(ctx, rn.RunnerID, []string{"projB", "projA"}, time.Minute)
	if err != nil || spec == nil || spec.ID != j.ID {
		t.Fatalf("Claim(projA) = %+v, %v", spec, err)
	}
	if spec.Env["OCTOPUS_API_KEY"] != "okey_secret" {
		t.Errorf("claimed spec lost its env: %v", spec.Env)
	}
	stored, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Spec.Env) != 0 {
		t.Errorf("stored spec still has the env after the claim: %v", stored.Spec.Env)
	}
	if again, _ := s.Claim(ctx, rn.RunnerID, []string{"projA"}, time.Minute); again != nil {
		t.Errorf("a claimed job was handed out twice")
	}
}

func TestOtherRunnerCannotClaimOrFinish(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := register(t, s, "ws1", "a")
	b := register(t, s, "ws1", "b")
	j := enqueue(t, s, a.RunnerID, "ws1", "projA")
	if spec, _ := s.Claim(ctx, b.RunnerID, []string{"projA"}, time.Minute); spec != nil {
		t.Fatal("runner b claimed runner a's job")
	}
	if _, err := s.Claim(ctx, a.RunnerID, []string{"projA"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, b.RunnerID, j.ID, FinishRequest{ExitCode: 0}); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("Finish by b err = %v, want conflict", err)
	}
	if _, err := s.Heartbeat(ctx, b.RunnerID, j.ID, time.Minute); apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("Heartbeat by b err = %v, want conflict", err)
	}
}

func TestFinishIsIdempotentAndSettles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	j := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if _, err := s.Claim(ctx, rn.RunnerID, []string{"projA"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	res := FinishRequest{ExitCode: 3, Pushed: []PushedBranch{{ProjectID: "projA", Branch: "octopus/task1", Commit: "abc"}}}
	for i := 0; i < 2; i++ {
		if err := s.Finish(ctx, rn.RunnerID, j.ID, res); err != nil {
			t.Fatalf("Finish #%d: %v", i+1, err)
		}
	}
	settled, err := s.Settled(ctx)
	if err != nil || len(settled) != 1 {
		t.Fatalf("Settled = %+v, %v", settled, err)
	}
	got := settled[0]
	if got.ExitCode == nil || *got.ExitCode != 3 || len(got.Result.Pushed) != 1 || got.Result.Pushed[0].Commit != "abc" {
		t.Errorf("settled job = %+v", got)
	}
	if err := s.MarkReaped(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if settled, _ := s.Settled(ctx); len(settled) != 0 {
		t.Errorf("a reaped job is still settled: %+v", settled)
	}
}

func TestCancel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")

	queued := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if err := s.RequestCancel(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetJob(ctx, queued.ID)
	if !got.Done() || got.ExitCode == nil || *got.ExitCode != ExitCanceled {
		t.Errorf("canceled queued job = %+v, want done with %d", got, ExitCanceled)
	}

	running := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if _, err := s.Claim(ctx, rn.RunnerID, []string{"projA"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestCancel(ctx, running.ID); err != nil {
		t.Fatal(err)
	}
	cancel, err := s.Heartbeat(ctx, rn.RunnerID, running.ID, time.Minute)
	if err != nil || !cancel {
		t.Errorf("Heartbeat after cancel = %v, %v; want cancel", cancel, err)
	}
}

func TestExpireLosesSilentAndUnclaimedJobs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	silent := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if _, err := s.Claim(ctx, rn.RunnerID, []string{"projA"}, time.Second); err != nil {
		t.Fatal(err)
	}
	unclaimed := enqueue(t, s, rn.RunnerID, "ws1", "projZ")

	s.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	n, err := s.Expire(ctx, 10*time.Minute)
	if err != nil || n != 2 {
		t.Fatalf("Expire = %d, %v; want 2", n, err)
	}
	for _, id := range []string{silent.ID, unclaimed.ID} {
		j, _ := s.GetJob(ctx, id)
		if j.State != StateLost {
			t.Errorf("job %s state = %s, want lost", id, j.State)
		}
	}
	if err := s.Finish(ctx, rn.RunnerID, silent.ID, FinishRequest{}); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("Finish of a lost job err = %v, want conflict", err)
	}
}

func TestRevokeEndsJobsAndCredential(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	j := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if err := s.Revoke(ctx, "ws2", rn.RunnerID); err == nil {
		t.Fatal("another workspace revoked the runner")
	}
	if err := s.Revoke(ctx, "ws1", rn.RunnerID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, rn.Credential, ""); err == nil {
		t.Error("a revoked credential still authenticates")
	}
	got, _ := s.GetJob(ctx, j.ID)
	if got.State != StateLost {
		t.Errorf("job of a revoked runner = %s, want lost", got.State)
	}
	list, err := s.List(ctx, "ws1")
	if err != nil || len(list) != 1 || list[0].RevokedAt == nil {
		t.Errorf("List = %+v, %v", list, err)
	}
}

func TestGetIsScopedToWorkspace(t *testing.T) {
	s := newTestStore(t)
	rn := register(t, s, "ws1", "laptop")
	if _, err := s.Get(context.Background(), "ws2", rn.RunnerID); apperr.KindOf(err) != apperr.KindNotFound {
		t.Errorf("Get from ws2 err = %v, want not found", err)
	}
}
