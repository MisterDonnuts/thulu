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
	r, err := s.Authenticate(ctx, resp.Credential, "1.0", ProtocolVersion)
	if err != nil || r.ID != resp.RunnerID {
		t.Fatalf("Authenticate = %+v, %v", r, err)
	}
	if _, err := s.Authenticate(ctx, "orn_nope", "", ProtocolVersion); apperr.KindOf(err) != apperr.KindUnauthenticated {
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

	spec, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projB"}, time.Minute)
	if err != nil || spec != nil {
		t.Fatalf("Claim(projB) = %+v, %v; want nothing", spec, err)
	}
	spec, err = s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projB", "projA"}, time.Minute)
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
	if again, _ := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); again != nil {
		t.Errorf("a claimed job was handed out twice")
	}
}

func TestOtherRunnerCannotClaimOrFinish(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := register(t, s, "ws1", "a")
	b := register(t, s, "ws1", "b")
	j := enqueue(t, s, a.RunnerID, "ws1", "projA")
	if spec, _ := s.Claim(ctx, b.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); spec != nil {
		t.Fatal("runner b claimed runner a's job")
	}
	if _, err := s.Claim(ctx, a.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); err != nil {
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
	if _, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); err != nil {
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
	if _, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); err != nil {
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
	if _, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Second); err != nil {
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

// A daemon that was down past a run's lease adopts it as it starts: the runner that still holds the
// run renews it with its next heartbeat, and only a runner that stays silent through the grace loses
// it. Jobs that ended, were never claimed or belong to the runner's lease alone are left as they are.
func TestAdoptRunningKeepsARunThroughADaemonRestart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	alive := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if _, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	silent := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if _, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	done := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if _, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, rn.RunnerID, done.ID, FinishRequest{ExitCode: 0}); err != nil {
		t.Fatal(err)
	}

	// The daemon comes back five minutes later: every lease ran out while nobody answered.
	start := time.Now().UTC().Add(5 * time.Minute)
	s.now = func() time.Time { return start }
	n, err := s.AdoptRunning(ctx, time.Minute)
	if err != nil || n != 2 {
		t.Fatalf("AdoptRunning = %d, %v; want the two running jobs", n, err)
	}
	if n, err := s.Expire(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("Expire right after adoption = %d, %v; want nothing lost", n, err)
	}
	if _, err := s.Heartbeat(ctx, rn.RunnerID, alive.ID, DefaultLease); err != nil {
		t.Fatalf("the runner's heartbeat after the restart: %v", err)
	}
	if _, err := s.Heartbeat(ctx, "other-runner", alive.ID, time.Minute); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("another runner's heartbeat err = %v, want conflict: adoption keeps the job its runner's", err)
	}

	s.now = func() time.Time { return start.Add(50 * time.Second) }
	if _, err := s.Heartbeat(ctx, rn.RunnerID, alive.ID, DefaultLease); err != nil {
		t.Fatalf("a later heartbeat: %v", err)
	}
	s.now = func() time.Time { return start.Add(90 * time.Second) }
	if n, err := s.Expire(ctx, time.Hour); err != nil || n != 1 {
		t.Fatalf("Expire after the grace = %d, %v; want the silent job alone", n, err)
	}
	for id, want := range map[string]string{alive.ID: StateRunning, silent.ID: StateLost, done.ID: StateFinished} {
		if j, _ := s.GetJob(ctx, id); j.State != want {
			t.Errorf("job %s = %s, want %s", id, j.State, want)
		}
	}
	// A second adoption, as a later restart would run it, never shortens a lease.
	if n, err := s.AdoptRunning(ctx, time.Second); err != nil || n != 0 {
		t.Errorf("AdoptRunning with a renewed lease = %d, %v; want nothing touched", n, err)
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
	if _, err := s.Authenticate(ctx, rn.Credential, "", ProtocolVersion); err == nil {
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

// Case 56: a protocol-1 runner keeps taking folder jobs and never gets one that needs 2.
func TestClaimHandsOnlyJobsTheRunnerUnderstands(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	folders := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	newer := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	if _, err := s.db.ExecContext(ctx, `UPDATE runner_jobs SET min_protocol = 2 WHERE id = $1`, newer.ID); err != nil {
		t.Fatal(err)
	}

	spec, err := s.Claim(ctx, rn.RunnerID, ProtocolFolders, []string{"projA"}, time.Minute)
	if err != nil || spec == nil || spec.ID != folders.ID {
		t.Fatalf("protocol 1 claim = %+v, %v; want the folder job", spec, err)
	}
	if spec, _ := s.Claim(ctx, rn.RunnerID, ProtocolFolders, []string{"projA"}, time.Minute); spec != nil {
		t.Fatalf("protocol 1 runner got job %s, which needs protocol 2", spec.ID)
	}
	if spec, err := s.Claim(ctx, rn.RunnerID, 2, []string{"projA"}, time.Minute); err != nil || spec == nil || spec.ID != newer.ID {
		t.Fatalf("protocol 2 claim = %+v, %v", spec, err)
	}
}

func TestAuthenticateRecordsTheDeclaredProtocol(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	got, err := s.Get(ctx, "ws1", rn.RunnerID)
	if err != nil || got.Protocol != ProtocolFolders {
		t.Fatalf("a new runner = %+v, %v; want protocol %d until it says otherwise", got, err, ProtocolFolders)
	}
	if _, err := s.Authenticate(ctx, rn.Credential, "", 2); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(ctx, "ws1", rn.RunnerID); got.Protocol != 2 {
		t.Errorf("protocol = %d after a call declaring 2", got.Protocol)
	}
	if _, err := s.Authenticate(ctx, rn.Credential, "", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(ctx, "ws1", rn.RunnerID); got.Protocol != 2 {
		t.Errorf("a call declaring nothing reset the protocol to %d", got.Protocol)
	}
}

func TestAcceptedProtocol(t *testing.T) {
	for in, want := range map[string]int{"1": 1, "2": 2, " 2 ": 2, "": 0, "0": 0, "3": 0, "x": 0} {
		if got := AcceptedProtocol(in); got != want {
			t.Errorf("AcceptedProtocol(%q) = %d, want %d", in, got, want)
		}
	}
}

func enqueueChatTurn(t *testing.T, s *Store, runnerID, ws string) *Job {
	t.Helper()
	id := NewJobID()
	j := &Job{ID: id, RunnerID: runnerID, WorkspaceID: ws, InstanceID: id, AgentName: "manager", Stage: "chat",
		LaunchKind: LaunchKindChatTurn,
		Spec: JobSpec{InstanceID: id, Stage: "chat", LaunchKind: LaunchKindChatTurn, ChatID: "chat1",
			Stdin: "the person's message", Env: map[string]string{"OCTOPUS_API_KEY": "okey_secret"}}}
	if err := s.Enqueue(context.Background(), j); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return j
}

// Case 46: each pool takes only its own jobs, so a turn is claimed while every run slot is busy and a
// run never fills the chat slots; a runner that names no pool takes both, as before pools existed.
func TestClaimTakesOnlyThePoolsJobs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	run := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	turn := enqueueChatTurn(t, s, rn.RunnerID, "ws1")
	claim := func(kinds ...string) string {
		spec, err := s.Claim(ctx, rn.RunnerID, ProtocolVersion, []string{"projA"}, time.Minute, kinds...)
		if err != nil {
			t.Fatal(err)
		}
		if spec == nil {
			return ""
		}
		return spec.ID
	}
	if got := claim(ClaimChat); got != turn.ID {
		t.Fatalf("chat pool claimed %q, want the turn %s", got, turn.ID)
	}
	if got := claim(ClaimChat); got != "" {
		t.Fatalf("chat pool claimed %q, a run", got)
	}
	if got := claim(ClaimRun); got != run.ID {
		t.Fatalf("run pool claimed %q, want the run %s", got, run.ID)
	}

	again := enqueueChatTurn(t, s, rn.RunnerID, "ws1")
	if got := claim(); got != again.ID {
		t.Errorf("a claim with no pool got %q, want the turn %s", got, again.ID)
	}
}

// The person's message leaves the database with the claim, like the env; a runner too old for chat
// turns never gets one.
func TestClaimOfAChatTurn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rn := register(t, s, "ws1", "laptop")
	turn := enqueueChatTurn(t, s, rn.RunnerID, "ws1")
	if spec, _ := s.Claim(ctx, rn.RunnerID, ProtocolFolders, nil, time.Minute); spec != nil {
		t.Fatalf("a protocol %d runner got the chat turn", ProtocolFolders)
	}
	spec, err := s.Claim(ctx, rn.RunnerID, ProtocolChatTurn, nil, time.Minute, ClaimChat)
	if err != nil || spec == nil {
		t.Fatalf("Claim = %+v, %v", spec, err)
	}
	if spec.Stdin != "the person's message" || spec.ChatID != "chat1" {
		t.Errorf("claimed spec = %+v, want the message and the chat", spec)
	}
	stored, err := s.GetJob(ctx, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Stdin != "" || stored.Spec.Env != nil {
		t.Errorf("stored spec keeps stdin %q and env %v after the claim", stored.Spec.Stdin, stored.Spec.Env)
	}
}

// The caller's copy of a queued job names the same log file as the stored row: the time it keeps is
// the one the database stores, even across a second boundary.
func TestEnqueueKeepsTheStoredCreationTime(t *testing.T) {
	s := newTestStore(t)
	rn := register(t, s, "ws1", "laptop")
	s.now = func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 999_999_600, time.UTC) }
	j := enqueue(t, s, rn.RunnerID, "ws1", "projA")
	got, err := s.GetJob(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(j.CreatedAt) {
		t.Errorf("stored %s, the caller keeps %s", got.CreatedAt, j.CreatedAt)
	}
}
