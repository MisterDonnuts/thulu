package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const stageVerdictBody = "---\nverdict: kick_back\nreason: gates_failed\n---\nTC-2 fails\n"

func postStageVerdict(t *testing.T, h http.Handler, taskID, instance, key, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost,
		"/tasks/"+taskID+"/stage-verdict?instance="+instance, strings.NewReader(stageVerdictBody))
	r.Header.Set("Content-Type", "text/markdown")
	if key != "" {
		r.Header.Set("X-API-Key", key)
	} else {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestStageVerdict_OnlyTheStageAgentFilesIt(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	auditor := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: auth.AuditorAgent, Stage: "in_progress"})
	own := "developer-task-" + rig.a + "-in_progress-17"

	cases := []struct {
		name, key, task, instance string
		want                      int
	}{
		{"the auditor's key", auditor, rig.a, own, http.StatusForbidden},
		{"a person", "", rig.a, own, http.StatusForbidden},
		{"the agent, another stage", rig.keyA, rig.a, "developer-task-" + rig.a + "-review-17", http.StatusBadRequest},
		{"the agent, the auditor's instance", rig.keyA, rig.a, "developer-audit-" + rig.a + "-in_progress-17", http.StatusBadRequest},
		{"the agent, another task", rig.keyA, rig.b, "developer-task-" + rig.b + "-in_progress-17", http.StatusForbidden},
		{"the agent, its own run", rig.keyA, rig.a, own, http.StatusCreated},
	}
	for _, c := range cases {
		w := postStageVerdict(t, rig.h, c.task, c.instance, c.key, rig.token)
		if w.Code != c.want {
			t.Errorf("%s = %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
	}

	reports, err := rig.srv.taskStore.ListReports(t.Context(), rig.a)
	if err != nil {
		t.Fatal(err)
	}
	filed := 0
	for _, r := range reports {
		if r.Kind == task.ReportKindStageVerdict {
			filed++
			if r.Source != task.SourceAgent || r.Author != "developer" || r.InstanceID != own || r.Body != stageVerdictBody {
				t.Errorf("verdict filed as %s/%s on %s: %q", r.Source, r.Author, r.InstanceID, r.Body)
			}
		}
	}
	if filed != 1 {
		t.Errorf("%d verdict reports, want only the agent's", filed)
	}
}

func lastStageEntered(t *testing.T, h http.Handler, token, id string) (string, map[string]string) {
	t.Helper()
	w := getJSON(t, h, "/tasks/"+id+"/events", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET events = %d: %s", w.Code, w.Body.String())
	}
	var evs []task.WorkflowEvent
	if err := json.Unmarshal(w.Body.Bytes(), &evs); err != nil {
		t.Fatalf("decode events: %v (%s)", err, w.Body.String())
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == task.EventStageEntered {
			var p map[string]string
			_ = json.Unmarshal(evs[i].Payload, &p)
			return evs[i].Actor, p
		}
	}
	t.Fatal("no stage_entered event")
	return "", nil
}

// Only a person's move resets the send-back count, so the event must say who moved the task.
func TestStageMove_RecordsWhetherAPersonMovedIt(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)

	if w := moveTask(t, rig.h, rig.token, rig.a, "review"); w.Code != http.StatusOK {
		t.Fatalf("person move = %d: %s", w.Code, w.Body.String())
	}
	actor, p := lastStageEntered(t, rig.h, rig.token, rig.a)
	if p["via"] != task.ViaManual || p["by_person"] != "true" || actor == "agent:developer" {
		t.Errorf("person move recorded as %s %v", actor, p)
	}

	// A run moves one of its subtasks, never the task it works on (run_moves_own_task).
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a1, rig.keyA, map[string]string{"stage": "review"}); w.Code != http.StatusOK {
		t.Fatalf("agent move = %d: %s", w.Code, w.Body.String())
	}
	actor, p = lastStageEntered(t, rig.h, rig.token, rig.a1)
	if p["via"] != task.ViaManual || p["by_person"] != "false" || actor != "agent:developer" {
		t.Errorf("agent move recorded as %s %v, want agent:developer and by_person false", actor, p)
	}
}

func TestStagePatch_FlowFields(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)

	// A column's default agent must exist and run in that column.
	if w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{"default_agent": "reviewer"}); w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with an unknown agent = %d: %s", w.Code, w.Body.String())
	}
	setupChatAgent(t, srv, "reviewer")
	if w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{"default_agent": "reviewer"}); w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with an agent outside review = %d: %s", w.Code, w.Body.String())
	}
	setupStageAgent(t, srv, "reviewer", "review")

	w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{
		"agent_verdict": true, "default_agent": "reviewer", "skip_unless": []string{"ui", "api"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/review = %d: %s", w.Code, w.Body.String())
	}
	var cols []types.StageInfo
	if err := json.Unmarshal(getJSON(t, h, "/boards/default/stages", "").Body.Bytes(), &cols); err != nil {
		t.Fatal(err)
	}
	var review types.StageInfo
	for _, c := range cols {
		if c.ID == types.StageReview {
			review = c
		}
	}
	if !review.AgentVerdict || review.DefaultAgent != "reviewer" ||
		!reflect.DeepEqual(review.SkipUnless, []string{"ui", "api"}) {
		t.Errorf("review after PATCH = %+v", review)
	}

	// An empty list clears skip_unless; omitting a field leaves it alone.
	if w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{"skip_unless": []string{}, "pr_ready": true}); w.Code != http.StatusOK {
		t.Fatalf("clear skip_unless = %d: %s", w.Code, w.Body.String())
	}
	var after []types.StageInfo
	_ = json.Unmarshal(getJSON(t, h, "/boards/default/stages", "").Body.Bytes(), &after)
	for _, c := range after {
		if c.ID == types.StageReview && (len(c.SkipUnless) != 0 || !c.AgentVerdict || !c.PRReady) {
			t.Errorf("review after clearing skip_unless = %+v", c)
		}
	}

	for name, body := range map[string]map[string]any{
		"bad skip value":             {"skip_unless": []string{"Not Valid"}},
		"merge target with no PR":    {"merge_target": true},
		"skippable stage pushes":     {"push_branch": true, "skip_unless": []string{"ui"}},
		"verdict on the first stage": nil,
	} {
		path := "/stages/review"
		if body == nil {
			path, body = "/stages/backlog", map[string]any{"agent_verdict": true}
		}
		if w := patchJSONAuth(t, h, path, "", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: PATCH = %d, want 400 (%s)", name, w.Code, w.Body.String())
		}
	}
}

func TestBoardPatch_MaxKickBacks(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := patchJSONAuth(t, h, "/boards/default", "", map[string]any{"max_kick_backs": 2}); w.Code != http.StatusOK {
		t.Fatalf("PATCH max_kick_backs = %d: %s", w.Code, w.Body.String())
	}
	got := decodeBoards(t, getJSON(t, h, "/boards", ""))
	for _, b := range got.Boards {
		if b.ID == "default" && b.MaxKickBacks != 2 {
			t.Errorf("max_kick_backs = %d, want 2", b.MaxKickBacks)
		}
	}
	if w := patchJSONAuth(t, h, "/boards/default", "", map[string]any{"max_kick_backs": 500}); w.Code != http.StatusBadRequest {
		t.Errorf("max_kick_backs 500 = %d, want 400", w.Code)
	}
	if w := createBoard(t, h, map[string]any{
		"id": "capped", "label": "Capped", "stages": twoStageFlow("capped"), "max_kick_backs": 3,
	}); w.Code != http.StatusCreated {
		t.Fatalf("create with max_kick_backs = %d: %s", w.Code, w.Body.String())
	}
	for _, b := range decodeBoards(t, getJSON(t, h, "/boards", "")).Boards {
		if b.ID == "capped" && b.MaxKickBacks != 3 {
			t.Errorf("created board max_kick_backs = %d, want 3", b.MaxKickBacks)
		}
	}
}

func TestBrief_SaysWhenTheStageTakesAVerdict(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	h, id := rig.h, rig.a
	if w := moveTask(t, h, rig.token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", w.Code, w.Body.String())
	}
	brief := func() string {
		w := getJSON(t, h, "/tasks/"+id+"/brief", rig.token)
		if w.Code != http.StatusOK {
			t.Fatalf("brief = %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if strings.Contains(brief(), "verdict.md") {
		t.Error("a stage without agent_verdict asks for verdict.md")
	}
	if w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{"agent_verdict": true}); w.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(brief(), "verdict.md") {
		t.Error("the brief does not tell the agent its stage takes a verdict")
	}
}

// A verdict stage's brief carries the latest agent report of each earlier verdict stage: agents
// have no tool that reads reports, and a gate checks the gates before it.
func TestBrief_CarriesTheEarlierVerdictStagesReports(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	h, id := rig.h, rig.a
	ctx := t.Context()
	for _, st := range []string{"refinement", "review"} {
		if w := patchJSONAuth(t, h, "/stages/"+st, "", map[string]any{"agent_verdict": true}); w.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d: %s", st, w.Code, w.Body.String())
		}
	}
	add := func(stage, kind string, src task.Source, body string) {
		if _, err := rig.srv.taskStore.AddRunReport(ctx, id, task.Stage(stage), src, "dev", kind, "", body, ""); err != nil {
			t.Fatal(err)
		}
	}
	add("refinement", task.ReportKindAgentIndex, task.SourceAgent, "old development report")
	add("refinement", task.ReportKindAgentIndex, task.SourceAgent, "Start: 1a2b3c. Criteria: AC-1 in cart.go.")
	add("refinement", task.ReportKindStageVerdict, task.SourceAgent, "---\nverdict: continue\n---\n")
	add("refinement", task.ReportKindAuditorFeedback, task.SourceAuditor, "auditor notes")
	brief := func(query string) string {
		w := getJSON(t, h, "/tasks/"+id+"/brief"+query, rig.token)
		if w.Code != http.StatusOK {
			t.Fatalf("brief = %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if w := moveTask(t, h, rig.token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", w.Code, w.Body.String())
	}
	got := brief("")
	if !strings.Contains(got, "Reports of the earlier checks") || !strings.Contains(got, "Start: 1a2b3c. Criteria: AC-1 in cart.go.") {
		t.Errorf("review's brief lacks Refinement's latest report:\n%s", got)
	}
	for _, not := range []string{"old development report", "---\nverdict: continue", "auditor notes"} {
		if strings.Contains(got, not) {
			t.Errorf("review's brief carries %q", not)
		}
	}
	if strings.Contains(brief("?audit=x-task-"+id+"-review-1"), "Reports of the earlier checks") {
		t.Error("the auditor's brief carries the earlier reports")
	}
	if w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{"agent_verdict": false}); w.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(brief(""), "Reports of the earlier checks") {
		t.Error("a stage without agent_verdict got the earlier reports")
	}
}

// The handoff between stages travels in the brief: the verdict of the stage that sent the task back,
// this stage's own last block, and the delta and next of the earlier stages. System notes stay out.
func TestBrief_CarriesTheHandoffAndNoSystemNotes(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	h, id := rig.h, rig.a
	ctx := t.Context()
	store := rig.srv.taskStore
	for _, st := range []string{"refinement", "in_progress", "review"} {
		if w := patchJSONAuth(t, h, "/stages/"+st, "", map[string]any{"agent_verdict": true}); w.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d: %s", st, w.Code, w.Body.String())
		}
	}
	add := func(stage, author, body string) {
		if _, err := store.AddRunReport(ctx, id, task.Stage(stage), task.SourceAgent, author,
			task.ReportKindStageVerdict, "", body, ""); err != nil {
			t.Fatal(err)
		}
	}
	add("refinement", "refiner", "---\nverdict: continue\nnext: Development covers AC-3, the empty cart.\n"+
		"delta:\n  - {id: AC-3, status: new}\n  - AC-1 reworded\n---\nSpec ready.")
	add("in_progress", "developer", "---\nverdict: block\nreason: other\n---\nNeeds a decision on the currency.")
	if w := moveTask(t, h, rig.token, id, "in_progress"); w.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", w.Code, w.Body.String())
	}
	add("review", "reviewer", "---\nverdict: kick_back\nkick_back_to: in_progress\nreason: review_blocking\n"+
		"next: Fix the race in debit() and add its test.\n---\nF1 race in internal/cart/debit.go:42")
	if err := store.RecordEvent(ctx, id, task.EventStageEntered, "", "agent:reviewer", "review → in_progress",
		map[string]string{"from": "review", "to": "in_progress", "via": task.ViaVerdict}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, id, task.SourceSystem, "", "Stage In progress, launched developer"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, id, task.SourceUser, "p@x", "Keep the old endpoint working."); err != nil {
		t.Fatal(err)
	}

	w := getJSON(t, h, "/tasks/"+id+"/brief", rig.token)
	if w.Code != http.StatusOK {
		t.Fatalf("brief = %d: %s", w.Code, w.Body.String())
	}
	got := w.Body.String()
	for _, want := range []string{
		"## Handoff",
		"Sent back from", "F1 race in internal/cart/debit.go:42", "Fix the race in debit() and add its test.",
		"This stage's last verdict: `block`", "Needs a decision on the currency.",
		"Development covers AC-3, the empty cart.", "- id: AC-3; status: new", "- AC-1 reworded",
		"Keep the old endpoint working.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("brief lacks %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"launched developer", "kick_back_to:", "Spec ready."} {
		if strings.Contains(got, not) {
			t.Errorf("brief carries %q:\n%s", not, got)
		}
	}
}

// An auditor that sends the task back to its own stage hands its notes to the new run in the brief.
func TestBrief_CarriesTheAuditorsSendBack(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	h, id := rig.h, rig.a
	ctx := t.Context()
	store := rig.srv.taskStore
	if w := moveTask(t, h, rig.token, id, "in_progress"); w.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", w.Code, w.Body.String())
	}
	feedback := "---\nverdict: kick_back\nkick_back_to: in_progress\nscore: 6/10\n---\n" +
		"# Notes\nC4 FAIL discount_test.go:42 asserts only NotNil"
	if _, err := store.AddRunReport(ctx, id, "in_progress", task.SourceAuditor, "auditor",
		task.ReportKindAuditorFeedback, "", feedback, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvent(ctx, id, task.EventStageEntered, "", "auditor", "in_progress (kick_back)",
		map[string]string{"from": "in_progress", "to": "in_progress", "via": task.ViaKickBack}); err != nil {
		t.Fatal(err)
	}
	got := getJSON(t, h, "/tasks/"+id+"/brief", rig.token).Body.String()
	if !strings.Contains(got, "## Handoff") || !strings.Contains(got, "C4 FAIL discount_test.go:42 asserts only NotNil") {
		t.Errorf("brief lacks the auditor's notes:\n%s", got)
	}
}
