package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const checksPR = "https://github.com/acme/widgets/pull/7"

func (rig *checksRig) noteCount(t *testing.T, key string) int {
	t.Helper()
	cs, err := rig.srv.taskStore.ListComments(t.Context(), rig.task)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range cs {
		if cs[i].Key == key {
			n++
		}
	}
	return n
}

func (rig *checksRig) mergeApproval(t *testing.T) *task.Approval {
	t.Helper()
	a, err := rig.srv.taskStore.GetApproval(t.Context(), rig.merge)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (rig *checksRig) approve(t *testing.T) {
	t.Helper()
	if w := decide(t, rig.h, rig.token, rig.task, rig.merge, map[string]string{"decision": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
}

// install attributes the webhooks of installation 100 to the rig's workspace.
func (rig *checksRig) install(t *testing.T) {
	t.Helper()
	if err := rig.srv.gh.store.UpsertInstallation(t.Context(), githubapp.Installation{
		InstallationID: 100, WorkspaceID: rig.ws, AccountLogin: "acme",
	}); err != nil {
		t.Fatal(err)
	}
}

func checkSuiteCompleted(number int) map[string]any {
	return map[string]any{
		"action":       "completed",
		"installation": map[string]any{"id": 100},
		"repository":   map[string]any{"full_name": "acme/widgets"},
		"check_suite": map[string]any{
			"head_sha": "abc123", "head_branch": "octopus/t1",
			"pull_requests": []map[string]any{{"number": number}},
		},
	}
}

func synchronized(sha string, at time.Time) map[string]any {
	return map[string]any{
		"action":       "synchronize",
		"installation": map[string]any{"id": 100},
		"repository":   map[string]any{"full_name": "acme/widgets"},
		"sender":       map[string]any{"login": "ana"},
		"pull_request": map[string]any{
			"number": 7, "html_url": checksPR, "updated_at": at.UTC().Format(time.RFC3339),
			"head": map[string]any{"ref": "octopus/t1", "sha": sha},
			"base": map[string]any{"ref": "main"},
		},
	}
}

func deliverOK(t *testing.T, h http.Handler, event string, payload any) {
	t.Helper()
	if code := deliverWebhook(t, h, event, payload); code != http.StatusNoContent {
		t.Fatalf("%s delivery = %d, want 204", event, code)
	}
}

// Approving while build still runs is taken, with a note; the task cannot be dragged on until the
// checks pass.
func TestChecksGate_ApproveAheadOfRunningChecks(t *testing.T) {
	rig := newChecksRig(t)
	rig.gh.running.Store(true)
	rig.approve(t)
	if a := rig.mergeApproval(t); a.Status != task.ApprovalApproved {
		t.Fatalf("merge = %s, want approved", a.Status)
	}
	if n := rig.noteCount(t, "api.approvals.comments.approved_ahead_of_checks"); n != 1 {
		t.Errorf("approved-ahead notes = %d, want 1", n)
	}

	w := moveTask(t, rig.h, rig.token, rig.task, "release")
	if w.Code != http.StatusConflict {
		t.Fatalf("move with build running = %d, want 409: %s", w.Code, w.Body.String())
	}
	var got checksRefusalView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "api.approvals.errors.gate_checks" || len(got.Checks) != 1 || got.Checks[0].State != "pending" ||
		!strings.Contains(got.Error, "build still running") {
		t.Errorf("refusal = %s", w.Body.String())
	}

	rig.gh.running.Store(false)
	rig.gh.green.Store(true)
	if w := moveTask(t, rig.h, rig.token, rig.task, "release"); w.Code != http.StatusOK {
		t.Fatalf("move with checks green = %d: %s", w.Code, w.Body.String())
	}
}

// Checks that fail after the approval hold the move too, and say which check failed.
func TestChecksGate_MoveRefusedWhenChecksFailAfterApproval(t *testing.T) {
	rig := newChecksRig(t)
	rig.gh.green.Store(true)
	rig.approve(t)
	if n := rig.noteCount(t, "api.approvals.comments.approved_ahead_of_checks"); n != 0 {
		t.Errorf("approving on green left %d approved-ahead notes", n)
	}
	rig.gh.green.Store(false)
	w := moveTask(t, rig.h, rig.token, rig.task, "release")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "api.approvals.errors.gate_checks") ||
		!strings.Contains(w.Body.String(), "build failed") {
		t.Fatalf("move with build red = %d: %s", w.Code, w.Body.String())
	}
	if got, _ := rig.srv.taskStore.Get(t.Context(), rig.task); got.Stage != types.StageHandoff {
		t.Errorf("stage = %s, want handoff", got.Stage)
	}
}

func TestChecksGate_DeveloperCannotWaiveRunningChecks(t *testing.T) {
	rig := newChecksRig(t)
	rig.gh.running.Store(true)
	u, err := rig.srv.authStore.CreateUser(t.Context(), "dev-running@test.com", "Dev")
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
	w := decide(t, rig.h, dev, rig.task, rig.merge, map[string]string{"decision": "waived", "note": "skip CI"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("developer waive with build running = %d, want 403: %s", w.Code, w.Body.String())
	}
}

// A task parked in an auto-advancing stage moves on when the webhook reports the checks finished
// green, once, however many deliveries arrive.
func TestChecksGate_WebhookReleasesParkedTaskOnce(t *testing.T) {
	rig := newChecksRig(t)
	rig.install(t)
	if w := patchJSONAuth(t, rig.h, "/stages/handoff", "", map[string]any{"auto_advance": true}); w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/handoff = %d: %s", w.Code, w.Body.String())
	}
	rig.gh.running.Store(true)
	rig.approve(t)
	parked := task.StatusAwaitingApproval
	if _, err := rig.srv.taskStore.Update(t.Context(), rig.task, task.Patch{Status: &parked}); err != nil {
		t.Fatal(err)
	}

	// lint finished; build still runs.
	deliverOK(t, rig.h, "check_suite", checkSuiteCompleted(7))
	if got, _ := rig.srv.taskStore.Get(t.Context(), rig.task); got.Stage != types.StageHandoff || got.Status != task.StatusAwaitingApproval {
		t.Fatalf("with build running: %s/%s, want handoff/awaiting_approval", got.Stage, got.Status)
	}

	rig.gh.running.Store(false)
	rig.gh.green.Store(true)
	deliverOK(t, rig.h, "check_suite", checkSuiteCompleted(7))
	deliverOK(t, rig.h, "check_suite", checkSuiteCompleted(7))
	deliverOK(t, rig.h, "check_run", map[string]any{
		"action": "completed", "installation": map[string]any{"id": 100},
		"repository": map[string]any{"full_name": "acme/widgets"},
		"check_run":  map[string]any{"head_sha": "abc123", "pull_requests": []map[string]any{{"number": 7}}},
	})
	got, _ := rig.srv.taskStore.Get(t.Context(), rig.task)
	if got.Stage != types.StageRelease {
		t.Fatalf("after green checks: stage %s, want release", got.Stage)
	}
	events, err := rig.srv.taskStore.ListEvents(t.Context(), rig.task)
	if err != nil {
		t.Fatal(err)
	}
	moves := 0
	for i := range events {
		if events[i].Kind == task.EventStageEntered && events[i].Summary == "handoff → release" {
			moves++
		}
	}
	if moves != 1 {
		t.Errorf("handoff → release recorded %d times, want 1", moves)
	}
}

// A check_suite of another workspace's installation, or for a PR no task carries, moves nothing.
func TestChecksGate_WebhookOfAnotherInstallationMovesNothing(t *testing.T) {
	rig := newChecksRig(t)
	rig.install(t)
	if err := rig.srv.gh.store.UpsertInstallation(t.Context(), githubapp.Installation{
		InstallationID: 200, WorkspaceID: "ws-other", AccountLogin: "other",
	}); err != nil {
		t.Fatal(err)
	}
	if w := patchJSONAuth(t, rig.h, "/stages/handoff", "", map[string]any{"auto_advance": true}); w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/handoff = %d: %s", w.Code, w.Body.String())
	}
	rig.gh.green.Store(true)
	rig.approve(t)
	parked := task.StatusAwaitingApproval
	if _, err := rig.srv.taskStore.Update(t.Context(), rig.task, task.Patch{Status: &parked}); err != nil {
		t.Fatal(err)
	}
	other := checkSuiteCompleted(7)
	other["installation"] = map[string]any{"id": 200}
	deliverOK(t, rig.h, "check_suite", other)
	deliverOK(t, rig.h, "check_suite", checkSuiteCompleted(8))
	if got, _ := rig.srv.taskStore.Get(t.Context(), rig.task); got.Stage != types.StageHandoff {
		t.Errorf("stage = %s, want handoff", got.Stage)
	}
}

// New commits on the PR send the approved merge back to pending with a note naming the new head;
// the same delivery again, or one that arrives after the person approved again, changes nothing.
func TestChecksGate_PushResetsApprovedMerge(t *testing.T) {
	rig := newChecksRig(t)
	rig.install(t)
	rig.gh.green.Store(true)
	rig.approve(t)
	// Date the approval an hour back, so the push below falls after it.
	if _, err := rig.srv.taskStore.DB().ExecContext(t.Context(),
		`UPDATE task_approvals SET decided_at = $1 WHERE id = $2`, time.Now().Add(-time.Hour), rig.merge); err != nil {
		t.Fatal(err)
	}
	push := synchronized("def4567890abcdef", time.Now().Add(-30*time.Minute))

	opened := map[string]any{}
	for k, v := range push {
		opened[k] = v
	}
	opened["action"] = "opened"
	deliverOK(t, rig.h, "pull_request", opened)
	if a := rig.mergeApproval(t); a.Status != task.ApprovalApproved {
		t.Fatalf("a pull_request opened reset the merge to %s", a.Status)
	}

	deliverOK(t, rig.h, "pull_request", push)
	a := rig.mergeApproval(t)
	if a.Status != task.ApprovalPending || a.DecidedBy != "" {
		t.Fatalf("after the push: %s by %q, want pending", a.Status, a.DecidedBy)
	}
	if n := rig.noteCount(t, "api.approvals.comments.reset_pushed"); n != 1 {
		t.Errorf("reset notes = %d, want 1", n)
	}
	cs, _ := rig.srv.taskStore.ListComments(t.Context(), rig.task)
	if last := cs[len(cs)-1]; !strings.Contains(last.Body, "def4567") || !strings.Contains(last.Body, "Merge") {
		t.Errorf("reset note = %q, want the new head and the approval", last.Body)
	}
	events, _ := rig.srv.taskStore.ListEvents(t.Context(), rig.task)
	resets := 0
	for i := range events {
		if events[i].Kind == task.EventApprovalReset && events[i].Actor == "github:ana" {
			resets++
		}
	}
	if resets != 1 {
		t.Errorf("approval_reset events by github:ana = %d, want 1", resets)
	}

	deliverOK(t, rig.h, "pull_request", push)
	if n := rig.noteCount(t, "api.approvals.comments.reset_pushed"); n != 1 {
		t.Errorf("a repeated delivery left %d reset notes, want 1", n)
	}

	rig.approve(t)
	deliverOK(t, rig.h, "pull_request", push)
	if a := rig.mergeApproval(t); a.Status != task.ApprovalApproved {
		t.Errorf("a late redelivery reset the new approval to %s", a.Status)
	}
}

// An admin's waiver past the checks is the admin's call: a push leaves it alone.
func TestChecksGate_PushLeavesAWaiverAlone(t *testing.T) {
	rig := newChecksRig(t)
	rig.install(t)
	if w := decide(t, rig.h, rig.token, rig.task, rig.merge, map[string]string{"decision": "waived", "note": "CI is down"}); w.Code != http.StatusOK {
		t.Fatalf("owner waive = %d: %s", w.Code, w.Body.String())
	}
	deliverOK(t, rig.h, "pull_request", synchronized("def4567890abcdef", time.Now().Add(time.Minute)))
	if a := rig.mergeApproval(t); a.Status != task.ApprovalWaived {
		t.Errorf("a push turned a waiver into %s", a.Status)
	}
}

// In a task with an uploaded project and a GitHub one, merging the uploaded half from Thulu waits
// for the required checks of the GitHub half's pull request.
func TestTaskMerge_RefusedWhileRequiredChecksHold(t *testing.T) {
	rig := newChecksRig(t)
	w := postUpload(t, rig.h, "/projects/upload", rig.token, map[string]any{"name": "Folder App"},
		uploadTarGz(t, map[string]string{"README.md": "# folder\n"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /projects/upload = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	ids := []string{created.Project.ID, "p_widgets"}
	if _, err := rig.srv.taskStore.Update(t.Context(), rig.task, task.Patch{ProjectIDs: &ids}); err != nil {
		t.Fatal(err)
	}

	rig.gh.running.Store(true)
	w = postJSONAuth(t, rig.h, "/tasks/"+rig.task+"/merge", rig.token, map[string]string{"project_id": created.Project.ID})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "api.tasks.errors.merge_required_checks") ||
		!strings.Contains(w.Body.String(), "build still running") {
		t.Fatalf("merge with build running = %d: %s", w.Code, w.Body.String())
	}

	rig.gh.running.Store(false)
	rig.gh.green.Store(true)
	w = postJSONAuth(t, rig.h, "/tasks/"+rig.task+"/merge", rig.token, map[string]string{"project_id": created.Project.ID})
	if strings.Contains(w.Body.String(), "merge_required_checks") {
		t.Fatalf("merge with checks green still waits for them = %d: %s", w.Code, w.Body.String())
	}
	// The pull request is still open: the task moves when it merges, not on the uploaded half.
	if got, _ := rig.srv.taskStore.Get(t.Context(), rig.task); got.Stage != types.StageHandoff {
		t.Errorf("after the uploaded half merged: stage %s, want handoff", got.Stage)
	}
}

func TestHeldMerge_RefusedWhileRequiredChecksRun(t *testing.T) {
	gh := &checksGitHub{}
	gh.protection.Store(true)
	gh.running.Store(true)
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	t.Setenv("OCTOPUS_GITHUB_API_URL", fake.URL)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	hold := heldMergeHold(req.Context(), "ghs_test", &mergeReqFor7)
	if hold == nil || hold.State != "pending" {
		t.Fatalf("hold = %+v, want pending", hold)
	}
}

// GitHub dates a push to the second. An approval decided earlier within that same second came
// before the push (here at 12:00:00.300, the push at 12:00:00.700 reads 12:00:00Z) and approved
// code that is no longer the head.
func TestChecksGate_PushInTheSameSecondResetsTheApproval(t *testing.T) {
	rig := newChecksRig(t)
	rig.install(t)
	rig.gh.green.Store(true)
	rig.approve(t)
	second := time.Now().Add(-time.Hour).Truncate(time.Second)
	if _, err := rig.srv.taskStore.DB().ExecContext(t.Context(),
		`UPDATE task_approvals SET decided_at = $1 WHERE id = $2`, second.Add(300*time.Millisecond), rig.merge); err != nil {
		t.Fatal(err)
	}
	deliverOK(t, rig.h, "pull_request", synchronized("def4567890abcdef", second))
	if a := rig.mergeApproval(t); a.Status != task.ApprovalPending {
		t.Errorf("an approval from the push's own second stayed %s, want pending", a.Status)
	}
}
