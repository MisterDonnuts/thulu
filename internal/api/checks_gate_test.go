package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// checksGitHub answers one open PR whose base requires "build" and "lint". build fails until green
// is set, and runs while running is set; protection off leaves main without requirements.
type checksGitHub struct {
	green      atomic.Bool
	running    atomic.Bool
	protection atomic.Bool
	merges     atomic.Int32
}

func (g *checksGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	build := `"status":"completed","conclusion":"failure"`
	switch {
	case g.running.Load():
		build = `"status":"in_progress","conclusion":null`
	case g.green.Load():
		build = `"status":"completed","conclusion":"success"`
	}
	switch p := r.URL.Path; {
	case p == "/repos/acme/widgets/pulls/7":
		fmt.Fprint(w, `{"number":7,"state":"open","mergeable_state":"blocked",
			"head":{"ref":"octopus/t1","sha":"abc123"},"base":{"ref":"main"}}`)
	case p == "/repos/acme/widgets/pulls/7/reviews":
		fmt.Fprint(w, `[]`)
	case p == "/repos/acme/widgets/pulls/7/merge":
		g.merges.Add(1)
		fmt.Fprint(w, `{"merged":true,"sha":"m1","message":"merged"}`)
	case strings.HasSuffix(p, "/check-runs"):
		fmt.Fprintf(w, `{"total_count":2,"check_runs":[
			{"name":"build",%s,"app":{"id":15368}},
			{"name":"lint","status":"completed","conclusion":"success","app":{"id":15368}}]}`, build)
	case p == "/repos/acme/widgets/branches/main":
		if !g.protection.Load() {
			fmt.Fprint(w, `{"protected":false,"protection":{"enabled":false}}`)
			return
		}
		fmt.Fprint(w, `{"protected":true,"protection":{"enabled":true,
			"required_status_checks":{"contexts":["build","lint"],"checks":[]}}}`)
	case p == "/repos/acme/widgets/rules/branches/main":
		fmt.Fprint(w, `[]`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}
}

type checksRig struct {
	srv   *Server
	h     http.Handler
	gh    *checksGitHub
	token string
	ws    string
	task  string
	merge string
}

// newChecksRig puts a task with a GitHub PR in handoff, whose "merge" approval waits for the
// required checks.
func newChecksRig(t *testing.T) *checksRig {
	t.Helper()
	gh := &checksGitHub{}
	gh.protection.Store(true)
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	configureTestGithubApp(t)
	srv, h := newIntegrationServer(t)
	srv.gh.tokens = &sourcedTokens{source: githubapp.SourceStored}
	if w := patchJSONAuth(t, h, "/stages/handoff", "", map[string]any{
		"required_approvals": []map[string]any{{"key": "merge", "label": "Merge", "required_checks": true}},
	}); w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/handoff = %d: %s", w.Code, w.Body.String())
	}
	token, ws := registerUserInWorkspace(t, h, "checks@test.com", "Checks", "")
	id := createTaskViaAPI(t, h, token, "ship it")
	if _, err := srv.taskStore.Update(t.Context(), id, task.Patch{PRURLs: map[string]string{
		"p_widgets": "https://github.com/acme/widgets/pull/7",
	}}); err != nil {
		t.Fatal(err)
	}
	if w := moveTask(t, h, token, id, "handoff"); w.Code != http.StatusOK {
		t.Fatalf("into handoff = %d: %s", w.Code, w.Body.String())
	}
	rig := &checksRig{srv: srv, h: h, gh: gh, token: token, ws: ws, task: id}
	for _, a := range listApprovals(t, h, token, id) {
		if a.Key == "merge" {
			rig.merge = a.ID
		}
	}
	if rig.merge == "" {
		t.Fatal("entering handoff opened no merge approval")
	}
	return rig
}

type checksRefusalView struct {
	Code   string `json:"code"`
	Error  string `json:"error"`
	Checks []struct {
		Number  int      `json:"number"`
		State   string   `json:"state"`
		Failing []string `json:"failing"`
	} `json:"required_checks"`
}

func TestChecksGate_ApproveWaitsForGreenRequiredChecks(t *testing.T) {
	rig := newChecksRig(t)
	w := decide(t, rig.h, rig.token, rig.task, rig.merge, map[string]string{"decision": "approved"})
	if w.Code != http.StatusConflict {
		t.Fatalf("approve with build red = %d, want 409: %s", w.Code, w.Body.String())
	}
	var got checksRefusalView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "api.approvals.errors.required_checks" || len(got.Checks) != 1 || got.Checks[0].Number != 7 ||
		got.Checks[0].State != "failing" || strings.Join(got.Checks[0].Failing, ",") != "build" ||
		!strings.Contains(got.Error, "build") {
		t.Errorf("refusal = %s", w.Body.String())
	}
	if a, _ := rig.srv.taskStore.GetApproval(t.Context(), rig.merge); a.Status != task.ApprovalPending {
		t.Errorf("a refused approval became %s", a.Status)
	}

	// The PR panel shows the same state the gate read.
	code, rows := getPullRequests(t, rig.h, rig.task, rig.token)
	if code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("GET pull-requests = %d, %d rows", code, len(rows))
	}

	rig.gh.green.Store(true)
	if w := decide(t, rig.h, rig.token, rig.task, rig.merge, map[string]string{"decision": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve with checks green = %d: %s", w.Code, w.Body.String())
	}
}

func TestChecksGate_PullRequestRowCarriesRequiredChecks(t *testing.T) {
	rig := newChecksRig(t)
	w := getJSON(t, rig.h, "/tasks/"+rig.task+"/pull-requests", rig.token)
	var out struct {
		PullRequests []struct {
			Required struct {
				State    string   `json:"state"`
				Required []string `json:"required"`
				Failing  []string `json:"failing"`
			} `json:"required_checks"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.PullRequests) != 1 {
		t.Fatalf("decode %v: %s", err, w.Body.String())
	}
	got := out.PullRequests[0].Required
	if got.State != "failing" || strings.Join(got.Required, ",") != "build,lint" || strings.Join(got.Failing, ",") != "build" {
		t.Errorf("required_checks = %+v", got)
	}
}

func TestChecksGate_UnprotectedBranchDoesNotHold(t *testing.T) {
	rig := newChecksRig(t)
	rig.gh.protection.Store(false)
	if w := decide(t, rig.h, rig.token, rig.task, rig.merge, map[string]string{"decision": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve on a branch without protection = %d: %s", w.Code, w.Body.String())
	}
}

func TestChecksGate_NoCredentialDoesNotHold(t *testing.T) {
	rig := newChecksRig(t)
	rig.srv.gh.tokens = nil
	if w := decide(t, rig.h, rig.token, rig.task, rig.merge, map[string]string{"decision": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve without a GitHub credential = %d: %s", w.Code, w.Body.String())
	}
}

func TestChecksGate_OnlyAnAdminWaivesPastRedChecks(t *testing.T) {
	rig := newChecksRig(t)
	u, err := rig.srv.authStore.CreateUser(t.Context(), "dev-checks@test.com", "Dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.srv.authStore.AddMember(t.Context(), rig.ws, u.ID, auth.RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	dev, err := auth.IssueJWT(auth.Claims{UserID: u.ID, Email: u.Email, WorkspaceID: rig.ws, Role: auth.RoleDeveloper})
	if err != nil {
		t.Fatal(err)
	}
	waive := map[string]string{"decision": "waived", "note": "hotfix, CI is down"}
	w := decide(t, rig.h, dev, rig.task, rig.merge, waive)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "api.approvals.errors.required_checks_waive") {
		t.Fatalf("developer waive = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := decide(t, rig.h, rig.token, rig.task, rig.merge, waive); w.Code != http.StatusOK {
		t.Fatalf("owner waive = %d: %s", w.Code, w.Body.String())
	}
}

func TestChecksGate_ApprovalWithoutTheFlagIgnoresChecks(t *testing.T) {
	rig := newChecksRig(t)
	if w := patchJSONAuth(t, rig.h, "/stages/handoff", "", map[string]any{
		"required_approvals": []map[string]any{{"key": "merge", "label": "Merge"}},
	}); w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/handoff = %d: %s", w.Code, w.Body.String())
	}
	if w := decide(t, rig.h, rig.token, rig.task, rig.merge, map[string]string{"decision": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve an approval without required_checks = %d: %s", w.Code, w.Body.String())
	}
}

func TestChecksGate_StageNeedsAPullRequestFirst(t *testing.T) {
	_, h := newIntegrationServer(t)
	w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{
		"required_approvals": []map[string]any{{"key": "merge", "label": "Merge", "required_checks": true}},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "required_checks_without_pr") {
		t.Fatalf("required checks before any PR stage = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// A confirmed pr_merge from a delegated chat answers 409 while the required checks are red, and
// GitHub never sees the merge.
func TestDelegatedChatGithub_MergeRefusedWhileRequiredChecksAreRed(t *testing.T) {
	gh := &checksGitHub{}
	gh.protection.Store(true)
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	rig := newDelegatedGithubRig(t, "thulu-github@read+pr_merge")

	id := rig.held(t, "/internal/github/acme/widgets/pulls/7/merge", map[string]string{"merge_method": "squash"})
	a := rig.confirm(t, id)
	if a.ResultCode != http.StatusConflict || !strings.Contains(a.ResultError, "build") {
		t.Fatalf("confirmed merge with build red = %d %s, want 409", a.ResultCode, a.ResultError)
	}
	if n := gh.merges.Load(); n != 0 {
		t.Errorf("GitHub saw %d merges while a required check was red", n)
	}
}

func TestHeldMerge_RefusedWhileRequiredChecksAreRed(t *testing.T) {
	gh := &checksGitHub{}
	gh.protection.Store(true)
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	hold := heldMergeHold(req.Context(), "ghs_test", &mergeReqFor7)
	if hold == nil || hold.State != "failing" {
		t.Fatalf("hold = %+v, want failing", hold)
	}
	gh.green.Store(true)
	if hold := heldMergeHold(req.Context(), "ghs_test", &mergeReqFor7); hold != nil {
		t.Errorf("green checks still hold: %+v", hold)
	}
}

var mergeReqFor7 = github.MergeReq{Owner: "acme", Repo: "widgets", Number: 7}
