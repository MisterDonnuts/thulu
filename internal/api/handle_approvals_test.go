package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func requireOnReview(t *testing.T, h http.Handler) {
	t.Helper()
	w := patchJSONAuth(t, h, "/stages/review", "", map[string]any{
		"required_approvals": []map[string]any{{"key": "qa", "label": "QA sign-off"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/review = %d: %s", w.Code, w.Body.String())
	}
}

func moveTask(t *testing.T, h http.Handler, token, id, stage string) *httptest.ResponseRecorder {
	t.Helper()
	return patchJSONAuth(t, h, "/tasks/"+id, token, map[string]string{"stage": stage})
}

func listApprovals(t *testing.T, h http.Handler, token, id string) []task.Approval {
	t.Helper()
	w := getJSON(t, h, "/tasks/"+id+"/approvals", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET approvals = %d: %s", w.Code, w.Body.String())
	}
	var out []task.Approval
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func decide(t *testing.T, h http.Handler, token, id, aid string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSONAuth(t, h, "/tasks/"+id+"/approvals/"+aid+"/decide", token, body)
}

func TestApprovals_GateOnPatch(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "gate@test.com", "Gate", "SecurePassword1")
	requireOnReview(t, h)
	id := createTaskViaAPI(t, h, token, "gated")

	if w := moveTask(t, h, token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("into review = %d: %s", w.Code, w.Body.String())
	}
	opened := listApprovals(t, h, token, id)
	if len(opened) != 1 || opened[0].Key != "qa" || opened[0].Source != task.ApprovalFromStage {
		t.Fatalf("entering review opened %+v, want qa", opened)
	}

	w := moveTask(t, h, token, id, "handoff")
	if w.Code != http.StatusConflict {
		t.Fatalf("forward with qa pending = %d, want 409: %s", w.Code, w.Body.String())
	}
	var refusal struct {
		Code      string           `json:"code"`
		Approvals []task.Approval  `json:"blocking_approvals"`
		Flags     []map[string]any `json:"blocking_flags"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != "api.approvals.errors.gate_closed" || len(refusal.Approvals) != 1 || len(refusal.Flags) != 1 {
		t.Errorf("refusal = %s", w.Body.String())
	}

	if w := moveTask(t, h, token, id, "refinement"); w.Code != http.StatusOK {
		t.Fatalf("backward = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := moveTask(t, h, token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("back into review = %d: %s", w.Code, w.Body.String())
	}
	if again := listApprovals(t, h, token, id); len(again) != 1 {
		t.Errorf("re-entering review opened another: %+v", again)
	}

	if w := decide(t, h, token, id, opened[0].ID, map[string]string{"decision": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	if w := moveTask(t, h, token, id, "handoff"); w.Code == http.StatusConflict {
		t.Errorf("forward after approval still refused: %s", w.Body.String())
	}
}

func TestApprovals_GateNamesASeededColumnInTheCallersLanguage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "gate-es@test.com", "Gate", "SecurePassword1")
	requireOnReview(t, h)
	id := createTaskViaAPI(t, h, token, "gated")
	if w := moveTask(t, h, token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("into review = %d: %s", w.Code, w.Body.String())
	}
	for _, locale := range []string{"es", "en"} {
		r := httptest.NewRequest(http.MethodPatch, "/tasks/"+id, strings.NewReader(`{"stage":"handoff"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Accept-Language", locale)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: forward with qa pending = %d, want 409: %s", locale, w.Code, w.Body.String())
		}
		var refusal struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &refusal); err != nil {
			t.Fatal(err)
		}
		if want := i18n.T(locale, "stage.labels.review"); !strings.HasPrefix(refusal.Error, want+" ") {
			t.Errorf("%s refusal = %q, want it to name the column as %q", locale, refusal.Error, want)
		}
	}
}

func TestApprovals_GateOnBoardMoveKeepingTheStage(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "boardgate@test.com", "BoardGate", "SecurePassword1")
	requireOnReview(t, h)
	if w := createBoardAs(t, h, token, map[string]any{
		"id": "other", "label": "Other", "clone_from": stagepkg.DefaultBoardID,
	}); w.Code != http.StatusCreated {
		t.Fatalf("create board = %d: %s", w.Code, w.Body.String())
	}
	id := createTaskViaAPI(t, h, token, "traveller")
	if w := moveTask(t, h, token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("into review = %d: %s", w.Code, w.Body.String())
	}
	opened := listApprovals(t, h, token, id)
	if len(opened) != 1 {
		t.Fatalf("entering review opened %+v, want qa", opened)
	}

	w := patchJSONAuth(t, h, "/tasks/"+id, token, map[string]any{"board_id": "other"})
	if w.Code != http.StatusConflict {
		t.Fatalf("board move with qa pending = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := decide(t, h, token, id, opened[0].ID, map[string]string{"decision": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, h, "/tasks/"+id, token, map[string]any{"board_id": "other"}); w.Code != http.StatusOK {
		t.Errorf("board move after approval = %d: %s", w.Code, w.Body.String())
	}
}

func TestApprovals_DecideValidation(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "decide@test.com", "Decide", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "t")
	w := postJSONAuth(t, h, "/tasks/"+id+"/approvals", token, map[string]any{"label": "Pick the database", "reason": "two would work"})
	if w.Code != http.StatusCreated {
		t.Fatalf("request = %d: %s", w.Code, w.Body.String())
	}
	var a task.Approval
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Source != task.ApprovalFromPerson || a.Stage != "backlog" || a.Status != task.ApprovalPending {
		t.Errorf("requested approval = %+v", a)
	}

	for _, body := range []map[string]string{
		{"decision": "changes_requested"},
		{"decision": "waived", "note": "  "},
		{"decision": "pending"},
		{"decision": "maybe"},
	} {
		if w := decide(t, h, token, id, a.ID, body); w.Code != http.StatusBadRequest {
			t.Errorf("decide %v = %d, want 400: %s", body, w.Code, w.Body.String())
		}
	}
	w = decide(t, h, token, id, a.ID, map[string]string{"decision": "waived", "note": "decided in standup"})
	if w.Code != http.StatusOK {
		t.Fatalf("waive = %d: %s", w.Code, w.Body.String())
	}
	if got := listApprovals(t, h, token, id); got[0].Status != task.ApprovalWaived || got[0].DecidedNote != "decided in standup" {
		t.Errorf("after waiving: %+v", got[0])
	}

	other := createTaskViaAPI(t, h, token, "other")
	if w := decide(t, h, token, other, a.ID, map[string]string{"decision": "approved"}); w.Code != http.StatusNotFound {
		t.Errorf("deciding through another task = %d, want 404", w.Code)
	}
}

func TestApprovals_FlagsAlias(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "alias@test.com", "Alias", "SecurePassword1")
	requireOnReview(t, h)
	id := createTaskViaAPI(t, h, token, "t")
	if w := moveTask(t, h, token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("into review = %d", w.Code)
	}
	fw := postJSONAuth(t, h, "/tasks/"+id+"/flags", token, map[string]any{"label": "needs decision", "kind": "custom"})
	if fw.Code != http.StatusCreated {
		t.Fatalf("add flag = %d: %s", fw.Code, fw.Body.String())
	}
	var f task.Flag
	if err := json.Unmarshal(fw.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	list := listApprovals(t, h, token, id)
	if len(list) != 2 {
		t.Fatalf("approvals = %+v, want qa and the flag", list)
	}
	var stageOne string
	for i := range list {
		if list[i].ID == f.ID && list[i].Stage != "" {
			t.Errorf("the flag belongs to stage %q, want none", list[i].Stage)
		}
		if list[i].Key == "qa" {
			stageOne = list[i].ID
		}
	}

	if w := rawDo(t, h, http.MethodDelete, "/tasks/"+id+"/flags/"+stageOne, "", token, nil); w.Code != http.StatusConflict {
		t.Errorf("deleting a stage requirement via flags = %d, want 409", w.Code)
	}
	if w := rawDo(t, h, http.MethodDelete, "/tasks/"+id+"/approvals/"+stageOne, "", token, nil); w.Code != http.StatusConflict {
		t.Errorf("deleting a stage requirement = %d, want 409", w.Code)
	}
	if w := rawDo(t, h, http.MethodDelete, "/tasks/"+id+"/approvals/"+f.ID, "", token, nil); w.Code != http.StatusOK {
		t.Errorf("deleting the flag's approval = %d: %s", w.Code, w.Body.String())
	}
	if got := listApprovals(t, h, token, id); len(got) != 1 {
		t.Errorf("after delete: %+v", got)
	}
}

func TestApprovals_FlagsAliasDecisionIsOneEvent(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "once@test.com", "Once", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "t")
	fw := postJSONAuth(t, h, "/tasks/"+id+"/flags", token, map[string]any{"label": "needs decision", "kind": "custom"})
	if fw.Code != http.StatusCreated {
		t.Fatalf("add flag = %d: %s", fw.Code, fw.Body.String())
	}
	var f task.Flag
	if err := json.Unmarshal(fw.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if w := patchJSONAuth(t, h, "/tasks/"+id+"/flags/"+f.ID, token, map[string]string{"status": "approved"}); w.Code != http.StatusOK {
		t.Fatalf("approve flag = %d: %s", w.Code, w.Body.String())
	}

	ew := getJSON(t, h, "/tasks/"+id+"/events", token)
	var events []struct {
		Kind string `json:"kind"`
	}
	if ew.Code != http.StatusOK || json.Unmarshal(ew.Body.Bytes(), &events) != nil {
		t.Fatalf("events = %d: %s", ew.Code, ew.Body.String())
	}
	count := map[string]int{}
	for _, e := range events {
		count[e.Kind]++
	}
	if count[string(task.EventApprovalDecided)] != 1 || count[string(task.EventFlagResolved)] != 0 {
		t.Errorf("decision events = %v, want one approval_decided and no flag_resolved", count)
	}
}

func TestApprovals_RunKeyAsksButNeverDecides(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/approvals", rig.keyA, map[string]any{"label": "Ship the migration?"})
	if w.Code != http.StatusCreated {
		t.Fatalf("run asks = %d: %s", w.Code, w.Body.String())
	}
	var a task.Approval
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Source != task.ApprovalFromAgent {
		t.Errorf("source = %q, want agent", a.Source)
	}
	base := "/tasks/" + rig.a + "/approvals/" + a.ID
	if w := keyDo(t, rig.h, http.MethodPost, base+"/decide", rig.keyA, map[string]string{"decision": "approved"}); w.Code != http.StatusForbidden {
		t.Errorf("run decides = %d, want 403", w.Code)
	}
	if w := keyDo(t, rig.h, http.MethodDelete, base, rig.keyA, nil); w.Code != http.StatusForbidden {
		t.Errorf("run deletes = %d, want 403", w.Code)
	}
	if w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.b+"/approvals", rig.keyA, map[string]any{"label": "x"}); w.Code != http.StatusForbidden {
		t.Errorf("run asks on another task = %d, want 403", w.Code)
	}
}

func TestApprovals_CreateIntoColumnIsGated(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "create@test.com", "Create", "SecurePassword1")
	w := patchJSONAuth(t, h, "/stages/backlog", "", map[string]any{
		"required_approvals": []map[string]any{{"key": "triage", "label": "Triage"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/backlog = %d: %s", w.Code, w.Body.String())
	}
	w = postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "skip triage", "stage": "review"})
	if w.Code != http.StatusConflict {
		t.Errorf("create into review = %d, want 409: %s", w.Code, w.Body.String())
	}
	var refusal struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != "api.approvals.errors.gate_closed_create" {
		t.Errorf("refusal code = %q, want the create one: %s", refusal.Code, w.Body.String())
	}
}

func TestApprovals_StageSettingsValidated(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	for name, body := range map[string]map[string]any{
		"unknown mode":      {"on_changes_requested": "panic"},
		"kick back nowhere": {"changes_kick_back_to": "ghost"},
		"key with spaces":   {"required_approvals": []map[string]any{{"key": "q a", "label": "QA"}}},
		"no label":          {"required_approvals": []map[string]any{{"key": "qa"}}},
		"unknown type":      {"required_approvals": []map[string]any{{"key": "qa", "label": "QA", "task_types": []string{"ghost"}}}},
	} {
		if w := patchJSONAuth(t, h, "/stages/review", "", body); w.Code < 400 || w.Code >= 500 {
			t.Errorf("%s: PATCH = %d, want a 4xx: %s", name, w.Code, w.Body.String())
		}
	}
	ok := map[string]any{"on_changes_requested": "kick_back", "changes_kick_back_to": "refinement"}
	if w := patchJSONAuth(t, h, "/stages/review", "", ok); w.Code != http.StatusOK {
		t.Errorf("valid settings = %d: %s", w.Code, w.Body.String())
	}
}

func TestApprovals_FlagDecisionIsRecordedLikeADecide(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagdecide@test.com", "FlagDecide", "SecurePassword1")
	requireOnReview(t, h)
	id := createTaskViaAPI(t, h, token, "t")
	if w := moveTask(t, h, token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("into review = %d", w.Code)
	}
	add := func(label string) string {
		w := postJSONAuth(t, h, "/tasks/"+id+"/flags", token, map[string]any{"label": label, "kind": "custom"})
		if w.Code != http.StatusCreated {
			t.Fatalf("add flag = %d: %s", w.Code, w.Body.String())
		}
		var f task.Flag
		if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	ok, no := add("ship it?"), add("rename it?")
	for fid, status := range map[string]string{ok: "approved", no: "rejected"} {
		if w := patchJSONAuth(t, h, "/tasks/"+id+"/flags/"+fid, token, map[string]any{"status": status, "resolved_note": "because"}); w.Code != http.StatusOK {
			t.Fatalf("PATCH flag %s = %d: %s", status, w.Code, w.Body.String())
		}
	}
	for _, a := range listApprovals(t, h, token, id) {
		switch a.ID {
		case ok:
			if a.Status != task.ApprovalApproved || a.DecidedBy == "" || a.DecidedAt == nil || a.DecidedNote != "because" {
				t.Errorf("approved flag = %+v", a)
			}
		case no:
			if a.Status != task.ApprovalChangesRequested {
				t.Errorf("rejected flag = %q, want changes_requested", a.Status)
			}
		}
	}
	if ev := getJSON(t, h, "/tasks/"+id+"/events", token).Body.String(); strings.Count(ev, "approval_decided") < 2 {
		t.Errorf("events lack the two approval_decided: %s", ev)
	}
	if w := getJSON(t, h, "/tasks/"+id, token); !strings.Contains(w.Body.String(), `"stage":"review"`) {
		t.Errorf("a rejected flag moved the task: %s", w.Body.String())
	}
}

func TestApprovals_AgentRequestLimits(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	path := "/tasks/" + rig.a + "/approvals"
	for i := range maxOpenAgentApprovals {
		if w := keyDo(t, rig.h, http.MethodPost, path, rig.keyA, map[string]any{"label": "ask " + string(rune('a'+i))}); w.Code != http.StatusCreated {
			t.Fatalf("ask %d = %d: %s", i, w.Code, w.Body.String())
		}
	}
	if w := keyDo(t, rig.h, http.MethodPost, path, rig.keyA, map[string]any{"label": "one more"}); w.Code != http.StatusTooManyRequests {
		t.Errorf("ask past the limit = %d, want 429", w.Code)
	}
	if w := postJSONAuth(t, rig.h, path, rig.token, map[string]any{"label": "from a person"}); w.Code != http.StatusCreated {
		t.Errorf("person asks = %d: %s", w.Code, w.Body.String())
	}
	huge := map[string]any{"label": "big", "payload": map[string]string{"blob": strings.Repeat("x", maxApprovalRequestBody)}}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.b+"/approvals", rig.token, huge); w.Code != http.StatusBadRequest {
		t.Errorf("oversized body = %d, want 400", w.Code)
	}
}

func TestApprovals_StageDeletionWaivesWhatItSkips(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "stagedel@test.com", "StageDel", "SecurePassword1")
	requireOnReview(t, h)
	id := createTaskViaAPI(t, h, token, "t")
	if w := moveTask(t, h, token, id, "review"); w.Code != http.StatusOK {
		t.Fatalf("into review = %d", w.Code)
	}
	if w := rawDo(t, h, http.MethodDelete, "/stages/review?move_to=done", "", "", nil); w.Code != http.StatusOK {
		t.Fatalf("DELETE /stages/review = %d: %s", w.Code, w.Body.String())
	}
	list := listApprovals(t, h, token, id)
	if len(list) != 1 || list[0].Status != task.ApprovalWaived || list[0].DecidedBy != "octopus" || list[0].DecidedNote == "" {
		t.Errorf("after the stage went: %+v, want qa waived by octopus with a note", list)
	}
}
