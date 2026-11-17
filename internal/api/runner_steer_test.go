package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/task/launchercontract"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// launchOnRunner queues a run of the fixture's project with env on top of the placement.
func launchOnRunner(t *testing.T, f *runnerFixture, taskID string, env map[string]string) (runID string, job *runner.Job) {
	t.Helper()
	ctx := context.Background()
	full := map[string]string{"INSTANCE_ID": "dev-task-" + taskID + "-1", "TASK_ID": taskID,
		runner.RunnerIDEnv: f.reg.RunnerID}
	for k, v := range env {
		full[k] = v
	}
	cid, _, _, err := f.launcher.LaunchForStage(ctx, &task.Task{ID: taskID, WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}},
		types.StageInProgress, &types.AgentType{Name: "dev", Type: "dev"}, full, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	job, err = f.store.GetJob(ctx, runner.JobIDFromRun(cid))
	if err != nil {
		t.Fatal(err)
	}
	return cid, job
}

func claimJob(t *testing.T, f *runnerFixture) {
	t.Helper()
	spec, err := f.store.Claim(context.Background(), f.reg.RunnerID, runner.ProtocolVersion, []string{f.project.ID}, time.Minute)
	if err != nil || spec == nil {
		t.Fatalf("claim = %v, %v", spec, err)
	}
}

// Case 33, as hub tests: a line goes pending, then taken, then answered; an unanswered one expires
// and no poll hands it out late.
func TestSteerHubHandsOutAndAnswersEachLineOnce(t *testing.T) {
	h := newSteerHub()
	first := h.push("j1", "a\n")
	other := h.push("j2", "b\n")

	got := h.take(t.Context(), "j1")
	if len(got) != 1 || got[0].Line != "a\n" || got[0].ID != first.id {
		t.Fatalf("take(j1) = %+v, want only j1's line", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if again := h.take(ctx, "j1"); len(again) != 0 {
		t.Errorf("a taken line was handed out again: %+v", again)
	}
	h.ack("j1", []runner.SteerAck{{ID: first.id, Result: runner.SteerAppended}, {ID: "unknown", Result: runner.SteerAppended}})
	if res := <-first.result; res != runner.SteerAppended {
		t.Errorf("result = %q, want appended", res)
	}
	if _, left := h.jobs["j1"]; left {
		t.Error("an answered job stays in the hub")
	}

	if !h.expire("j2", other) {
		t.Fatal("expire found no pending line")
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel2()
	if late := h.take(ctx2, "j2"); len(late) != 0 {
		t.Errorf("an expired line was handed out: %+v", late)
	}
}

func TestSteerHubWakesAWaitingPoll(t *testing.T) {
	h := newSteerHub()
	got := make(chan []runner.SteerMessage, 1)
	go func() { got <- h.take(t.Context(), "j1") }()
	time.Sleep(20 * time.Millisecond)
	h.push("j1", "late\n")
	select {
	case msgs := <-got:
		if len(msgs) != 1 || msgs[0].Line != "late\n" {
			t.Errorf("woken poll got %+v", msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting poll never saw the new line")
	}
}

func TestSteerHubEndSealsWhatIsStillWaiting(t *testing.T) {
	h := newSteerHub()
	first := h.push("j1", "a\n")
	second := h.push("j1", "b\n")
	h.take(t.Context(), "j1")
	pending := h.push("j1", "c\n")
	h.end("j1")
	for _, l := range []*steerLine{first, second, pending} {
		if res := <-l.result; res != runner.SteerSealed {
			t.Errorf("result = %q, want sealed for a job that ended", res)
		}
	}
	if h.expire("j1", pending) {
		t.Error("expire found a line of an ended job")
	}
}

// Case 34: SteerRun returns once the machine answers; only appended is a success.
func TestRunnerSteerRunReportsWhatTheMachineDid(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	cid, job := launchOnRunner(t, f, "t1", nil)
	ctx := t.Context()

	if err := f.launcher.SteerRun(ctx, cid, []byte("{}\n")); apperr.Code(err) != "api.runners.errors.steer_not_running" {
		t.Fatalf("steer a queued job: err = %v, want refused at once", err)
	}
	if _, ok := f.launcher.steer.jobs[job.ID]; ok {
		t.Error("a refused message was queued in the hub")
	}
	claimJob(t, f)

	machine := func(result string) {
		msgs := f.launcher.steer.take(ctx, job.ID)
		acks := make([]runner.SteerAck, 0, len(msgs))
		for _, m := range msgs {
			acks = append(acks, runner.SteerAck{ID: m.ID, Result: result})
		}
		f.launcher.steer.ack(job.ID, acks)
	}
	cases := []struct {
		result string
		want   string
	}{
		{runner.SteerAppended, ""},
		{runner.SteerSealed, "api.runners.errors.steer_sealed"},
		{runner.SteerLocked, "api.runners.errors.steer_locked"},
		{runner.SteerFailed, "api.runners.errors.steer_failed"},
	}
	for _, c := range cases {
		go machine(c.result)
		if err := f.launcher.SteerRun(ctx, cid, []byte("{}\n")); apperr.Code(err) != c.want {
			t.Errorf("%s: err = %v, want %q", c.result, err, c.want)
		}
	}

	big := bytes.Repeat([]byte("x"), runner.MaxSteerLine+1)
	if err := f.launcher.SteerRun(ctx, cid, big); apperr.Code(err) != "api.runners.errors.steer_too_large" {
		t.Errorf("oversized line: err = %v, want refused", err)
	}

	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := f.launcher.SteerRun(short, cid, []byte("{}\n")); apperr.Code(err) != "api.runners.errors.steer_timeout" {
		t.Errorf("no answer: err = %v, want a timeout", err)
	}
	late, cancel2 := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel2()
	if msgs := f.launcher.steer.take(late, job.ID); len(msgs) != 0 {
		t.Errorf("a timed out message was handed out later: %+v", msgs)
	}
}

func steerCall(t *testing.T, url, credential, jobID string, req runner.SteerRequest) (int, runner.SteerResponse) {
	t.Helper()
	body, _ := json.Marshal(req)
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/runner/v1/jobs/"+jobID+"/steer", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set(runner.ProtocolHeader, strconv.Itoa(runner.ProtocolVersion))
	r.Header.Set("Authorization", "Bearer "+credential)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out runner.SteerResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func runnerDaemon(t *testing.T, f *runnerFixture, route *routedLauncher) *httptest.Server {
	t.Helper()
	srv := &Server{runnerStore: f.store, runnerRoute: route, runsDir: f.runsDir}
	mux := http.NewServeMux()
	srv.registerRunnerRoutes(mux, func(_ auth.Role, h http.HandlerFunc) http.HandlerFunc { return h })
	daemon := httptest.NewServer(mux)
	t.Cleanup(daemon.Close)
	return daemon
}

// Case 33 through the endpoint: only the machine holding the running job polls its messages, and a
// finished job answers done.
func TestRunnerSteerEndpointServesOnlyTheHoldingMachine(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	daemon := runnerDaemon(t, f, &routedLauncher{runner: f.launcher})
	cid, job := launchOnRunner(t, f, "t1", nil)

	if code, _ := steerCall(t, daemon.URL, f.reg.Credential, job.ID, runner.SteerRequest{}); code != http.StatusConflict {
		t.Errorf("poll of a queued job: %d, want 409", code)
	}
	claimJob(t, f)

	tok, _, err := f.store.CreateRegistration(t.Context(), "ws1", "other", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.store.Register(t.Context(), runner.RegisterRequest{Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	f.launcher.steer.push(job.ID, "{}\n")
	if code, resp := steerCall(t, daemon.URL, other.Credential, job.ID, runner.SteerRequest{}); code != http.StatusConflict || len(resp.Messages) != 0 {
		t.Errorf("another machine polled the job: %d %+v, want 409 and nothing", code, resp)
	}

	errs := make(chan error, 1)
	go func() { errs <- f.launcher.SteerRun(t.Context(), cid, []byte(`{"id":"m1"}`+"\n")) }()
	var msgs []runner.SteerMessage
	for len(msgs) < 2 {
		code, resp := steerCall(t, daemon.URL, f.reg.Credential, job.ID, runner.SteerRequest{})
		if code != http.StatusOK {
			t.Fatalf("poll: %d", code)
		}
		msgs = append(msgs, resp.Messages...)
	}
	acks := []runner.SteerAck{}
	for _, m := range msgs {
		acks = append(acks, runner.SteerAck{ID: m.ID, Result: runner.SteerAppended})
	}
	if err := f.store.Finish(t.Context(), f.reg.RunnerID, job.ID, runner.FinishRequest{}); err != nil {
		t.Fatal(err)
	}
	if code, resp := steerCall(t, daemon.URL, f.reg.Credential, job.ID, runner.SteerRequest{Acks: acks}); code != http.StatusOK || !resp.Done {
		t.Errorf("poll of a finished job: %d %+v, want done", code, resp)
	}
	if err := <-errs; err != nil {
		t.Errorf("a message appended right before the job ended: %v, want delivered", err)
	}
}

// steeringBase is a base launcher that steers its own runs, as Docker does.
type steeringBase struct {
	*launchercontract.FakeLauncher
	steered []string
}

func (b *steeringBase) SteerRun(_ context.Context, runID string, _ []byte) error {
	b.steered = append(b.steered, runID)
	return nil
}

// Case 38 and its Kubernetes variant: the routed launcher sends runner runs to the machine whether or
// not its base launcher steers, and arms by where each run goes.
func TestRoutedLauncherSteersRunnerRunsOnTheMachine(t *testing.T) {
	fake, _ := launchercontract.NewFake()
	docker := &steeringBase{FakeLauncher: fake}
	onMachine := map[string]string{runner.RunnerIDEnv: "r1"}
	for _, c := range []struct {
		name      string
		base      task.AgentLauncher
		baseSteer bool
	}{
		{"docker", docker, true},
		{"kubernetes", fake, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRunnerFixture(t, "ws1")
			route := newRoutedLauncher(c.base, f.projects)
			if route.CanSteer(onMachine) {
				t.Error("a machine run is armed with runners off")
			}
			route.runner = f.launcher
			if !route.CanSteer(onMachine) {
				t.Error("a machine run is not armed")
			}
			if got := route.CanSteer(map[string]string{}); got != c.baseSteer {
				t.Errorf("container run armed = %v, want %v", got, c.baseSteer)
			}

			cid, job := launchOnRunner(t, f, "t1", nil)
			claimJob(t, f)
			go func() {
				msgs := f.launcher.steer.take(t.Context(), job.ID)
				f.launcher.steer.ack(job.ID, []runner.SteerAck{{ID: msgs[0].ID, Result: runner.SteerAppended}})
			}()
			if err := route.SteerRun(t.Context(), cid, []byte("{}\n")); err != nil {
				t.Fatalf("steer a runner run: %v", err)
			}

			err := route.SteerRun(t.Context(), "ctr-1", []byte("{}\n"))
			if c.baseSteer && err != nil {
				t.Errorf("steer a container run: %v", err)
			}
			if !c.baseSteer && apperr.Code(err) != "task.errors.launcher_cannot_steer" {
				t.Errorf("steer a pod: err = %v, want not implemented", err)
			}
		})
	}
	if len(docker.steered) != 1 || docker.steered[0] != "ctr-1" {
		t.Errorf("base steered %v, want only the container run", docker.steered)
	}
}

// A runner older than the steer protocol still takes the run, without steering; the dispatcher sees
// the run disarmed in the env it passed.
func TestOldRunnerRunsWithoutSteering(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	env := map[string]string{runner.SteerEnabledEnv: "1"}
	_, job := launchOnRunner(t, f, "t1", env)
	if job.Spec.Env[runner.SteerEnabledEnv] != "" || job.Spec.MinProtocol() != runner.ProtocolFolders {
		t.Errorf("job on a protocol 1 runner: steer %q, min protocol %d; want no steering, protocol 1",
			job.Spec.Env[runner.SteerEnabledEnv], job.Spec.MinProtocol())
	}

	if _, err := f.store.Authenticate(t.Context(), f.reg.Credential, "test", runner.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	_, job = launchOnRunner(t, f, "t2", map[string]string{runner.SteerEnabledEnv: "1"})
	if job.Spec.Env[runner.SteerEnabledEnv] != "1" || job.Spec.MinProtocol() != runner.ProtocolSteer {
		t.Errorf("job on a current runner: steer %q, min protocol %d; want steering, protocol %d",
			job.Spec.Env[runner.SteerEnabledEnv], job.Spec.MinProtocol(), runner.ProtocolSteer)
	}
}

// Cases 34 and 38 end to end: a message sent through the routed launcher reaches the agent's queue on
// the machine while it runs.
func TestRunnerSteeringEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	f := newRunnerFixture(t, "ws1")
	fake, _ := launchercontract.NewFake()
	route := newRoutedLauncher(fake, f.projects)
	route.runner = f.launcher
	daemon := runnerDaemon(t, f, route)
	if _, err := f.store.Authenticate(t.Context(), f.reg.Credential, "test", runner.ProtocolVersion); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	checkout := filepath.Join(tmp, "app")
	gitT(t, tmp, "init", "--quiet", "--bare", "-b", "main", remote)
	gitT(t, tmp, "clone", "--quiet", remote, checkout)
	gitT(t, checkout, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--quiet", "--allow-empty", "-m", "root")
	gitT(t, checkout, "push", "--quiet", "origin", "HEAD:main")
	machineEnv(t)
	entry := filepath.Join(tmp, "agent.sh")
	// The agent waits for one queued message and leaves it as its output.
	script := `set -eu
q="$OCTOPUS_ROOT/.steer/queue.jsonl"
for _ in $(seq 1 200); do
  if [ -s "$q" ]; then
    mkdir -p "$OCTOPUS_ROOT/runs/$INSTANCE_ID"
    cp "$q" "$OCTOPUS_ROOT/runs/$INSTANCE_ID/steered.jsonl"
    exit 0
  fi
  sleep 0.1
done
exit 7
`
	if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &hostrun.Config{DaemonURL: daemon.URL, RunnerID: f.reg.RunnerID, Credential: f.reg.Credential,
		Projects: map[string]string{f.project.ID: checkout}, WorkDir: filepath.Join(tmp, "work"),
		Concurrency: 1, Entrypoint: entry}

	cid, _ := launchOnRunner(t, f, "t1", map[string]string{runner.SteerEnabledEnv: "1"})
	outDir := runOutputDir(f.runsDir, "dev-task-t1-1")
	exits := make(chan int, 1)
	route.OnExit(t.Context(), cid, func(code int, _, _ string) { exits <- code })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = hostrun.New(cfg, "test").Run(ctx) }()

	line := `{"id":"m1","kind":"message","text":"also add tests"}` + "\n"
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := route.SteerRun(t.Context(), cid, []byte(line))
		if err == nil {
			break
		}
		if apperr.Code(err) != "api.runners.errors.steer_not_running" || time.Now().After(deadline) {
			t.Fatalf("steer: %v; log:\n%s", err, f.launcher.RunLogs(cid))
		}
		time.Sleep(100 * time.Millisecond)
	}
	end := time.After(30 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case code := <-exits:
			if code != 0 {
				t.Fatalf("exit %d; log:\n%s", code, f.launcher.RunLogs(cid))
			}
			b, err := os.ReadFile(filepath.Join(outDir, "steered.jsonl"))
			if err != nil || string(b) != line {
				t.Errorf("queue the agent read = %q, %v; want the message once", b, err)
			}
			return
		case <-tick.C:
			f.launcher.settle(context.Background())
		case <-end:
			t.Fatalf("the run never ended; log:\n%s", f.launcher.RunLogs(cid))
		}
	}
}
