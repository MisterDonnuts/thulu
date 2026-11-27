package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Starts every launch at once and never reports an exit, so a relaunch returns 200.
type acceptingLauncher struct {
	agent    *types.AgentType
	launches atomic.Int32
}

func (l *acceptingLauncher) LaunchForStage(_ context.Context, t *task.Task, _ task.Stage, _ *types.AgentType,
	env map[string]string, _, _ map[string]string, _ bool,
) (string, string, string, error) {
	l.launches.Add(1)
	return "ctr-" + t.ID, env["INSTANCE_ID"], "", nil
}
func (l *acceptingLauncher) OnExit(context.Context, string, func(int, string, string)) {}
func (l *acceptingLauncher) FindAgentType(_, name string) *types.AgentType {
	if name == l.agent.Name {
		return l.agent
	}
	return nil
}
func (l *acceptingLauncher) ContainerAlive(string) bool { return false }
func (l *acceptingLauncher) ScanExitedTaskAgents(context.Context) ([]task.ExitedTaskAgent, error) {
	return nil, nil
}

func seedStop(t *testing.T, srv *Server, taskID string) *task.Comment {
	t.Helper()
	c, err := srv.taskStore.PostComment(t.Context(), task.NewComment{TaskID: taskID, Source: task.SourceAgent,
		Author: "developer", Body: "Need a QA account to log in.", Kind: task.CommentBlock})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decisionsOf(t *testing.T, srv *Server, taskID string) []task.Comment {
	t.Helper()
	cs, err := srv.taskStore.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []task.Comment
	for _, c := range cs {
		if c.Kind == task.CommentDecision {
			out = append(out, c)
		}
	}
	return out
}

func eventsOf(t *testing.T, srv *Server, taskID string, kind task.EventKind) []task.WorkflowEvent {
	t.Helper()
	all, err := srv.taskStore.ListEvents(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	var out []task.WorkflowEvent
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func payloadOf(t *testing.T, e task.WorkflowEvent) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
		t.Fatalf("payload of %s: %v", e.Kind, err)
	}
	return p
}

func codeOf(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Code
}

// A person moving a task back must say why; the reason becomes a decision answering the open stop
// and the stage_entered event points at it. Forward moves take a reason but do not need one.
func TestDecisions_PersonMovingBackNeedsAReason(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "decide-back@test.com", "Ana")
	id := taskIDFor(t, h, token, "login")

	if w := moveTask(t, h, token, id, "in_progress"); w.Code != http.StatusOK {
		t.Fatalf("forward without a reason = %d: %s", w.Code, w.Body)
	}
	if d := decisionsOf(t, srv, id); len(d) != 0 {
		t.Errorf("a forward move without a reason posted %+v", d)
	}
	stop := seedStop(t, srv, id)

	w := moveTask(t, h, token, id, "backlog")
	if w.Code != http.StatusBadRequest || codeOf(t, w.Body.Bytes()) != "api.tasks.errors.reason_required_send_back" {
		t.Fatalf("back without a reason = %d: %s", w.Code, w.Body)
	}
	w = patchJSONAuth(t, h, "/tasks/"+id, token, map[string]string{"stage": "backlog", "reason": "   "})
	if w.Code != http.StatusBadRequest {
		t.Errorf("back with a blank reason = %d, want 400", w.Code)
	}
	w = patchJSONAuth(t, h, "/tasks/"+id, token, map[string]string{"stage": "backlog", "reason": strings.Repeat("x", maxDecisionReasonRunes+1)})
	if w.Code != http.StatusBadRequest || codeOf(t, w.Body.Bytes()) != "api.tasks.errors.reason_too_long" {
		t.Errorf("back with a reason too long = %d: %s", w.Code, w.Body)
	}
	if tk, _ := srv.taskStore.Get(t.Context(), id); tk.Stage != "in_progress" {
		t.Fatalf("a refused move changed the stage to %s", tk.Stage)
	}

	w = patchJSONAuth(t, h, "/tasks/"+id, token, map[string]string{"stage": "backlog", "reason": "the spec is missing the error states"})
	if w.Code != http.StatusOK {
		t.Fatalf("back with a reason = %d: %s", w.Code, w.Body)
	}
	ds := decisionsOf(t, srv, id)
	if len(ds) != 1 {
		t.Fatalf("decisions = %+v, want one", ds)
	}
	d := ds[0]
	if d.ParentID != stop.ID || d.Source != task.SourceUser || d.Body != "the spec is missing the error states" ||
		d.Meta == nil || d.Meta.Action != task.DecisionSendBack || d.Meta.FromStage != "in_progress" || d.Meta.ToStage != "backlog" {
		t.Errorf("decision = %+v meta %+v", d, d.Meta)
	}
	entered := eventsOf(t, srv, id, task.EventStageEntered)
	if p := payloadOf(t, entered[len(entered)-1]); p["comment_id"] != d.ID {
		t.Errorf("stage_entered payload = %v, want comment_id %s", p, d.ID)
	}
	if open, _ := srv.taskStore.OpenStopThread(t.Context(), id); open != "" {
		t.Errorf("the stop is still open after the task left its column")
	}

	w = patchJSONAuth(t, h, "/tasks/"+id, token, map[string]string{"stage": "in_progress", "reason": "spec fixed"})
	if w.Code != http.StatusOK {
		t.Fatalf("forward with a reason = %d: %s", w.Code, w.Body)
	}
	ds = decisionsOf(t, srv, id)
	if last := ds[len(ds)-1]; len(ds) != 2 || last.ParentID != "" || last.Meta.Action != task.DecisionMove {
		t.Errorf("forward decision = %+v, want a root decision with action move", last)
	}
}

// Keys (automations, agents) move tasks back without a reason; old agents keep working.
func TestDecisions_KeysMoveBackWithoutAReason(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "decide-key@test.com", "Key")
	id := taskIDFor(t, h, token, "by key")
	if w := moveTask(t, h, token, id, "in_progress"); w.Code != http.StatusOK {
		t.Fatalf("forward = %d: %s", w.Code, w.Body)
	}
	key := mintKey(t, srv, ws, auth.RoleAdmin, auth.PermAll)
	if w := keyDo(t, h, http.MethodPatch, "/tasks/"+id, key, map[string]string{"stage": "backlog"}); w.Code != http.StatusOK {
		t.Fatalf("key moving back without a reason = %d: %s", w.Code, w.Body)
	}
	if d := decisionsOf(t, srv, id); len(d) != 0 {
		t.Errorf("a move without a reason posted %+v", d)
	}

	// An agent's run key sends a subtask of its task back without a reason, as old agents do (a run
	// never moves its own task).
	parent := taskIDFor(t, h, token, "the agent's own task")
	child := subtaskIDFor(t, h, token, parent, "a subtask")
	if w := moveTask(t, h, token, child, "in_progress"); w.Code != http.StatusOK {
		t.Fatalf("subtask forward = %d: %s", w.Code, w.Body)
	}
	runKey, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: parent, Agent: "developer", Stage: "backlog"}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if w := keyDo(t, h, http.MethodPatch, "/tasks/"+child, runKey, map[string]string{"stage": "backlog"}); w.Code != http.StatusOK {
		t.Fatalf("agent moving back without a reason = %d: %s", w.Code, w.Body)
	}
}

// A relaunch without a reason is only a timeline event; with one, a decision in the stop.
func TestDecisions_RelaunchReasonIsOptional(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "decide-relaunch@test.com", "Rel")
	id := taskIDFor(t, h, token, "relaunch me")
	if _, err := srv.taskStore.Update(t.Context(), id, task.Patch{
		StageAssignments: map[task.Stage]string{"backlog": "developer"},
	}); err != nil {
		t.Fatal(err)
	}
	l := &acceptingLauncher{agent: &types.AgentType{Name: "developer"}}
	srv.taskDispatcher = task.NewDispatcher(srv.taskStore, l, srv.projectStore, nil, nil).WithBoards(srv.boards)
	before, _ := srv.taskStore.ListComments(t.Context(), id)

	if w := postJSONAuth(t, h, "/tasks/"+id+"/relaunch", token, map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("relaunch = %d: %s", w.Code, w.Body)
	}
	after, _ := srv.taskStore.ListComments(t.Context(), id)
	if len(after) != len(before) {
		t.Errorf("a relaunch without a reason posted %+v", after[len(before):])
	}
	ev := eventsOf(t, srv, id, task.EventRelaunchRequested)
	if len(ev) != 1 {
		t.Fatalf("relaunch_requested events = %d, want 1", len(ev))
	}
	if p := payloadOf(t, ev[0]); p["comment_id"] != nil || p["stage"] != "backlog" {
		t.Errorf("payload = %v", p)
	}

	stop := seedStop(t, srv, id)
	waitForIdle(t, srv, id)
	if w := postJSONAuth(t, h, "/tasks/"+id+"/relaunch", token, map[string]any{"reason": "the QA account is in Secrets now"}); w.Code != http.StatusOK {
		t.Fatalf("relaunch with a reason = %d: %s", w.Code, w.Body)
	}
	ds := decisionsOf(t, srv, id)
	if len(ds) != 1 || ds[0].ParentID != stop.ID || ds[0].Meta.Action != task.DecisionRelaunch {
		t.Fatalf("decisions = %+v", ds)
	}
	ev = eventsOf(t, srv, id, task.EventRelaunchRequested)
	if p := payloadOf(t, ev[len(ev)-1]); p["comment_id"] != ds[0].ID {
		t.Errorf("payload = %v, want comment_id %s", p, ds[0].ID)
	}
}

// waitForIdle waits for the relaunch's launch goroutine to settle the task's status, so the next
// relaunch is not refused as a run in flight.
func waitForIdle(t *testing.T, srv *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := srv.taskStore.Get(t.Context(), id)
		if err == nil && tk.Status != task.StatusQueued {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A waiver with a reason is a decision in the stop that names the cases it covered.
func TestDecisions_WaiveAnswersTheStop(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "decide-waive@test.com", "Wai")
	id := taskIDFor(t, h, token, "waive")
	ctx := t.Context()
	cases, err := srv.taskStore.AddTestCases(ctx, id, "tester", []task.TestCase{{Title: "signup with Google"}, {Title: "logout"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.taskStore.RecordTestCaseRun(ctx, id, cases[0].Key, task.TestCaseRun{Verdict: task.TestCaseBlocked, Author: "tester"}); err != nil {
		t.Fatal(err)
	}
	stop := seedStop(t, srv, id)

	w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases/waive", token, map[string]any{"cases": []string{cases[0].Key}, "reason": "no Google account in staging"})
	if w.Code != http.StatusOK {
		t.Fatalf("waive = %d: %s", w.Code, w.Body)
	}
	ds := decisionsOf(t, srv, id)
	if len(ds) != 1 || ds[0].ParentID != stop.ID || ds[0].Meta.Action != task.DecisionWaive ||
		len(ds[0].Meta.Cases) != 1 || ds[0].Meta.Cases[0] != cases[0].Key || ds[0].Body != "no Google account in staging" {
		t.Fatalf("decisions = %+v", ds)
	}
	// The last blocked case settled: the stop resolved itself.
	if open, _ := srv.taskStore.OpenStopThread(ctx, id); open != "" {
		t.Error("the stop is still open with no case blocked")
	}
}

// An approval opens its own thread; deciding it answers there and attention points at it.
func TestDecisions_ApprovalDecisionRepliesInItsThread(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "decide-approval@test.com", "App")
	id := taskIDFor(t, h, token, "approve")
	a := &task.Approval{TaskID: id, Label: "Copy reviewed", Reason: "new onboarding texts", Stage: "backlog", CreatedBy: "agent:writer"}
	if err := srv.taskStore.AddApproval(t.Context(), a); err != nil {
		t.Fatal(err)
	}

	var got struct {
		Attention *task.Attention `json:"attention"`
		Approvals []task.Approval `json:"approvals"`
		Comments  []task.Comment  `json:"comments"`
	}
	w := getJSON(t, h, "/tasks/"+id, token)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	thread := task.ApprovalThreadOf(got.Comments, a.ID)
	if thread == "" || got.Attention == nil || got.Attention.ThreadID != thread || got.Approvals[0].ThreadID != thread {
		t.Fatalf("thread %q, attention %+v, approvals %+v", thread, got.Attention, got.Approvals)
	}
	for _, c := range got.Comments {
		if c.ID == thread && (c.Kind != task.CommentApproval || c.Meta == nil || c.Meta.ApprovalID != a.ID) {
			t.Errorf("thread root = %+v", c)
		}
	}

	if w := decide(t, h, token, id, a.ID, map[string]string{"decision": "approved", "note": "reads well"}); w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body)
	}
	ds := decisionsOf(t, srv, id)
	if len(ds) != 1 || ds[0].ParentID != thread || ds[0].Meta.Action != task.DecisionApprove ||
		!strings.Contains(ds[0].Body, "reads well") || ds[0].Source != task.SourceUser {
		t.Fatalf("decisions = %+v", ds)
	}
}

func TestWorkspaceSettings_AgentTurnsPerThread(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, ws := mintWorkspaceUserAs(t, srv, "wss-turns@test.com", "WT", auth.RoleAdmin)
	path := "/workspace-settings/" + settings.AgentTurnsPerThreadKey
	turnsOf := func(body []byte) settings.RunLimit {
		t.Helper()
		var view map[string]settings.RunLimit
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
		lim, ok := view[settings.AgentTurnsPerThreadKey]
		if !ok {
			t.Fatalf("no %s in %s", settings.AgentTurnsPerThreadKey, body)
		}
		return lim
	}
	if lim := turnsOf(getJSON(t, h, "/workspace-settings", admin).Body.Bytes()); lim.Value != nil || lim.Default != 5 ||
		lim.Ceiling != 20 || lim.Effective != 5 {
		t.Errorf("default = %+v", lim)
	}
	for _, bad := range []any{0, 21, -1, 2.5, "3"} {
		w := putJSON(t, h, path, bad, admin)
		if w.Code != http.StatusBadRequest || codeOf(t, w.Body.Bytes()) != "api.workspace_settings.errors.agent_turns_range" {
			t.Errorf("PUT %v = %d: %s", bad, w.Code, w.Body)
		}
	}
	w := putJSON(t, h, path, 3, admin)
	if lim := turnsOf(w.Body.Bytes()); w.Code != http.StatusOK || lim.Value == nil || *lim.Value != 3 || lim.Effective != 3 {
		t.Fatalf("PUT 3 = %d: %s", w.Code, w.Body)
	}
	if got := srv.agentTurnsPerThread(t.Context(), ws); got != 3 {
		t.Errorf("cap in effect = %d, want 3", got)
	}
	if got := srv.agentTurnsPerThread(t.Context(), "another"); got != 5 {
		t.Errorf("another workspace's cap = %d, want 5", got)
	}
	if w := putJSON(t, h, path, nil, admin); w.Code != http.StatusOK || turnsOf(w.Body.Bytes()).Value != nil {
		t.Errorf("PUT null = %d: %s", w.Code, w.Body)
	}
	viewer := reissueAs(t, admin, auth.RoleViewer)
	if w := putJSON(t, h, path, 4, viewer); w.Code != http.StatusForbidden {
		t.Errorf("viewer PUT = %d, want 403", w.Code)
	}
}
