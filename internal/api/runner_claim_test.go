package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

func queueJob(t *testing.T, f *runnerFixture) *runner.Job {
	t.Helper()
	j := &runner.Job{RunnerID: f.reg.RunnerID, WorkspaceID: "ws1", TaskID: "task1", ProjectID: f.project.ID,
		InstanceID: "dev-task-task1-in_progress-1", AgentName: "dev", Stage: "in_progress",
		Spec: runner.JobSpec{InstanceID: "dev-task-task1-in_progress-1", TaskID: "task1",
			Env: map[string]string{"OCTOPUS_API_KEY": "okey_secret"}}}
	if err := f.store.Enqueue(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}

func claimRequest(t *testing.T, f *runnerFixture, ctx context.Context) (*http.Request, *runner.Runner) {
	t.Helper()
	rn, err := f.store.Authenticate(context.Background(), f.reg.Credential, "test", runner.ProtocolVersion)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/runner/v1/claim",
		strings.NewReader(`{"projects":["`+f.project.ID+`"]}`)).WithContext(ctx)
	return r.WithContext(context.WithValue(r.Context(), runnerCtxKey{}, rn)), rn
}

// A claim that commits just as the long poll's 25 s run out must hand the job over, not answer
// empty while the job stays taken.
func TestClaimOutlivesTheLongPollsWait(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{runnerStore: f.store}
	j := queueJob(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, rn := claimRequest(t, f, context.Background())

	spec, err := srv.claimJob(ctx, rn, runner.ClaimRequest{Projects: []string{f.project.ID}})
	if err != nil || spec == nil || spec.ID != j.ID {
		t.Fatalf("claimJob with the wait over = %+v, %v; want the job", spec, err)
	}
	if spec.Env["OCTOPUS_API_KEY"] != "okey_secret" {
		t.Errorf("claimed spec lost its env: %v", spec.Env)
	}
}

func TestClaimHandsTheQueuedJob(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{runnerStore: f.store}
	j := queueJob(t, f)
	r, _ := claimRequest(t, f, context.Background())
	w := httptest.NewRecorder()
	srv.handleRunnerClaim(w, r)

	var resp runner.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK {
		t.Fatalf("claim = %d %s", w.Code, w.Body.String())
	}
	if resp.Job == nil || resp.Job.ID != j.ID || resp.LeaseSeconds == 0 {
		t.Errorf("claim response = %+v, want the job with its lease", resp)
	}
}

// A runner that already hung up is not handed a job it can't receive: the job stays queued.
func TestClaimFromAGoneRunnerLeavesTheJobQueued(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{runnerStore: f.store}
	j := queueJob(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ := claimRequest(t, f, ctx)
	srv.handleRunnerClaim(httptest.NewRecorder(), r)

	stored, err := f.store.GetJob(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != runner.StateQueued {
		t.Errorf("job state = %q after a claim from a runner that hung up, want queued", stored.State)
	}
}
