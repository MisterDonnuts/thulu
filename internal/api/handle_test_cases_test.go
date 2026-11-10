package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func decodeCases(t *testing.T, w *httptest.ResponseRecorder) []task.TestCase {
	t.Helper()
	var out []task.TestCase
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode cases: %v (body %s)", err, w.Body.String())
	}
	return out
}

func addCases(t *testing.T, h http.Handler, token, taskID string, body any) []task.TestCase {
	t.Helper()
	w := postJSONAuth(t, h, "/tasks/"+taskID+"/test-cases", token, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST test-cases = %d, want 201: %s", w.Code, w.Body.String())
	}
	return decodeCases(t, w)
}

func listCases(t *testing.T, h http.Handler, token, taskID string) []task.TestCase {
	t.Helper()
	w := getJSON(t, h, "/tasks/"+taskID+"/test-cases", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET test-cases = %d: %s", w.Code, w.Body.String())
	}
	return decodeCases(t, w)
}

func taskEvents(t *testing.T, h http.Handler, token, taskID string) []task.WorkflowEvent {
	t.Helper()
	var out []task.WorkflowEvent
	w := getJSON(t, h, "/tasks/"+taskID+"/events", token)
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode events: %v (body %s)", err, w.Body.String())
	}
	return out
}

func hasEvent(events []task.WorkflowEvent, kind task.EventKind, refID, actor string) bool {
	for i := range events {
		if events[i].Kind == kind && events[i].RefID == refID && events[i].Actor == actor {
			return true
		}
	}
	return false
}

func uploadText(t *testing.T, h http.Handler, token, taskID string) string {
	t.Helper()
	w := uploadBase64(t, h, token, taskID, map[string]any{
		"filename": "take.json", "mime_type": "application/json",
		"content_base64": base64.StdEncoding.EncodeToString([]byte(`{"steps":[]}`)),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", w.Code, w.Body.String())
	}
	return decodeAttachment(t, w).ID
}

func TestIntegration_TestCasesCRUD(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	const email = "tc-crud@test.com"
	token := registerUser(t, h, email, "TC", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "login flow")

	one := addCases(t, h, token, id, map[string]any{"title": "Valid login", "expected": "lands on the board"})
	if len(one) != 1 || one[0].Key != "TC-1" || one[0].Position != 1 || one[0].CreatedBy != email {
		t.Fatalf("single add = %+v", one)
	}
	batch := addCases(t, h, token, id, map[string]any{"cases": []map[string]any{
		{"title": "Wrong password"}, {"title": "Locked account", "key": "AUTH-1", "priority": "high"},
	}})
	if len(batch) != 2 || batch[0].Key != "TC-2" || batch[1].Key != "AUTH-1" {
		t.Fatalf("batch add = %+v", batch)
	}

	if w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases", token, map[string]any{"title": "x", "key": "TC-1"}); w.Code != http.StatusConflict {
		t.Errorf("taken key = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases", token, map[string]any{"title": " "}); w.Code != http.StatusBadRequest {
		t.Errorf("empty title = %d, want 400", w.Code)
	}
	if w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases", token, map[string]any{"title": "x", "priority": "urgent"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad priority = %d, want 400", w.Code)
	}

	w := patchJSONAuth(t, h, "/tasks/"+id+"/test-cases/TC-2", token, map[string]any{"status": "fail", "position": 1})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH by key = %d: %s", w.Code, w.Body.String())
	}
	if w = patchJSONAuth(t, h, "/tasks/"+id+"/test-cases/"+one[0].ID, token, map[string]any{"covered": true}); w.Code != http.StatusOK {
		t.Fatalf("PATCH by id = %d: %s", w.Code, w.Body.String())
	}
	if w = patchJSONAuth(t, h, "/tasks/"+id+"/test-cases/TC-99", token, map[string]any{"status": "pass"}); w.Code != http.StatusNotFound {
		t.Errorf("PATCH missing case = %d, want 404", w.Code)
	}
	if w = patchJSONAuth(t, h, "/tasks/"+id+"/test-cases/TC-1", token, map[string]any{"status": "done"}); w.Code != http.StatusBadRequest {
		t.Errorf("PATCH bad status = %d, want 400", w.Code)
	}
	got := listCases(t, h, token, id)
	if len(got) != 3 || got[0].Key != "TC-2" || got[0].Status != "fail" || !got[1].Covered {
		t.Fatalf("after patches = %+v", got)
	}

	if w = deleteRequest(t, h, "/tasks/"+id+"/test-cases/AUTH-1", token); w.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", w.Code, w.Body.String())
	}
	if w = deleteRequest(t, h, "/tasks/"+id+"/test-cases/AUTH-1", token); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", w.Code)
	}
	var full task.Task
	json.Unmarshal(getJSON(t, h, "/tasks/"+id, token).Body.Bytes(), &full)
	if len(full.TestCases) != 2 {
		t.Errorf("GET /tasks/{id} test_cases = %d, want 2", len(full.TestCases))
	}

	events := taskEvents(t, h, token, id)
	for _, want := range []struct {
		kind task.EventKind
		ref  string
	}{
		{task.EventTestCaseAdded, one[0].ID},
		{task.EventTestCaseAdded, batch[1].ID},
		{task.EventTestCaseUpdated, batch[0].ID},
		{task.EventTestCaseDeleted, batch[1].ID},
	} {
		if !hasEvent(events, want.kind, want.ref, email) {
			t.Errorf("no %s event for %s signed %s", want.kind, want.ref, email)
		}
	}
}

func TestIntegration_TestCaseRuns(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	const email = "tc-runs@test.com"
	token := registerUser(t, h, email, "TR", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "checkout")
	other := createTaskViaAPI(t, h, token, "someone else's evidence")
	c := addCases(t, h, token, id, map[string]any{"title": "Pays with a card"})[0]
	video, take := uploadText(t, h, token, id), uploadText(t, h, token, id)
	foreign := uploadText(t, h, token, other)

	runPath := "/tasks/" + id + "/test-cases/TC-1/runs"
	w := postJSONAuth(t, h, runPath, token, map[string]any{
		"verdict": "pass", "observed": "receipt shown",
		"video_attachment_id": video, "take_attachment_id": take, "capture_attachment_ids": []string{video},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST run = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Run      task.TestCaseRun `json:"run"`
		TestCase task.TestCase    `json:"test_case"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Run.Author != email || resp.Run.VideoAttachmentID != video || resp.Run.TakeAttachmentID != take {
		t.Errorf("run = %+v", resp.Run)
	}
	if resp.TestCase.Status != "pass" || !resp.TestCase.Covered || len(resp.TestCase.Runs) != 1 {
		t.Errorf("case after run = %+v", resp.TestCase)
	}

	if w = postJSONAuth(t, h, runPath, token, map[string]any{"verdict": "fail", "video_attachment_id": foreign}); w.Code != http.StatusNotFound {
		t.Errorf("foreign attachment = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w = postJSONAuth(t, h, runPath, token, map[string]any{"verdict": "skipped"}); w.Code != http.StatusBadRequest {
		t.Errorf("skipped verdict = %d, want 400", w.Code)
	}
	if w = postJSONAuth(t, h, "/tasks/"+id+"/test-cases/TC-9/runs", token, map[string]any{"verdict": "pass"}); w.Code != http.StatusNotFound {
		t.Errorf("run of a missing case = %d, want 404", w.Code)
	}
	if got := listCases(t, h, token, id); len(got[0].Runs) != 1 || got[0].Status != "pass" {
		t.Errorf("refused runs changed the case: %+v", got[0])
	}
	if !hasEvent(taskEvents(t, h, token, id), task.EventTestCaseRun, c.ID, email) {
		t.Error("no test_case_run event for the recorded run")
	}
}

func TestIntegration_TestCasesImportBody(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tc-import@test.com", "TI", "SecurePassword1")
	w := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "imported", "body": "Intro.\n\n## Test Cases\n\n- [ ] Opens the form\n- [x] Saves the draft\n",
	})
	var created map[string]any
	json.NewDecoder(w.Body).Decode(&created)
	id := created["id"].(string)

	w = postJSONAuth(t, h, "/tasks/"+id+"/test-cases/import-body", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", w.Code, w.Body.String())
	}
	got := decodeCases(t, w)
	if len(got) != 2 || got[0].Title != "Opens the form" || got[1].Status != "pass" || !got[1].Covered {
		t.Fatalf("imported = %+v", got)
	}
	w = postJSONAuth(t, h, "/tasks/"+id+"/test-cases/import-body", token, nil)
	if again := decodeCases(t, w); w.Code != http.StatusOK || len(again) != 0 {
		t.Errorf("second import = %d %+v, want 200 []", w.Code, again)
	}
}

func TestIntegration_TestCasesInBrief(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tc-brief@test.com", "TB", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "briefed")
	if b := getJSON(t, h, "/tasks/"+id+"/brief", token).Body.String(); strings.Contains(b, "## Test Cases") {
		t.Errorf("brief of a task without cases has the section:\n%s", b)
	}
	addCases(t, h, token, id, map[string]any{"title": "Exports a PDF", "expected": "A PDF\n  with every column"})

	b := getJSON(t, h, "/tasks/"+id+"/brief", token).Body.String()
	if !strings.Contains(b, "## Test Cases") ||
		!strings.Contains(b, "- **TC-1** (pending) — Exports a PDF · expected: A PDF with every column\n") {
		t.Errorf("brief does not list the case on one line:\n%s", b)
	}
}

func TestIntegration_TestCasesWorkspaceIsolation(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "tc-ws-a@test.com", "A")
	tokenB, _ := mintWorkspaceUser(t, srv, "tc-ws-b@test.com", "B")
	idA := createTaskViaAPI(t, h, tokenA, "A's task")
	addCases(t, h, tokenA, idA, map[string]any{"cases": []map[string]any{{"title": "A one"}, {"title": "A two"}}})
	patchJSONAuth(t, h, "/tasks/"+idA+"/test-cases/TC-2", tokenA, map[string]any{"status": "fail"})
	idB := createTaskViaAPI(t, h, tokenB, "B's task")
	addCases(t, h, tokenB, idB, map[string]any{"title": "B one"})

	for _, c := range []scopedCall{
		{http.MethodGet, "/tasks/" + idA + "/test-cases", nil},
		{http.MethodPost, "/tasks/" + idA + "/test-cases", map[string]any{"title": "planted"}},
		{http.MethodPost, "/tasks/" + idA + "/test-cases/import-body", nil},
		{http.MethodPatch, "/tasks/" + idA + "/test-cases/TC-1", map[string]any{"status": "pass"}},
		{http.MethodDelete, "/tasks/" + idA + "/test-cases/TC-1", nil},
		{http.MethodPost, "/tasks/" + idA + "/test-cases/TC-1/runs", map[string]any{"verdict": "pass"}},
	} {
		if w := sendJSON(t, h, c.method, c.path, tokenB, c.body); w.Code != http.StatusNotFound {
			t.Errorf("B %s %s = %d, want 404", c.method, c.path, w.Code)
		}
	}
	if got := listCases(t, h, tokenA, idA); len(got) != 2 || got[0].Status != "pending" {
		t.Errorf("A's cases changed by B: %+v", got)
	}

	var qa struct {
		TestCases []task.TestCaseRow `json:"test_cases"`
		Counts    map[string]int     `json:"counts"`
	}
	json.Unmarshal(getJSON(t, h, "/test-cases", tokenB).Body.Bytes(), &qa)
	if len(qa.TestCases) != 1 || qa.TestCases[0].TaskID != idB || qa.Counts["pending"] != 1 || qa.Counts["fail"] != 0 {
		t.Errorf("B's QA view = %+v", qa)
	}
	w := getJSON(t, h, "/test-cases?status=fail", tokenA)
	json.Unmarshal(w.Body.Bytes(), &qa)
	if w.Code != http.StatusOK || len(qa.TestCases) != 1 || qa.TestCases[0].Key != "TC-2" || qa.TestCases[0].TaskTitle != "A's task" {
		t.Errorf("A's QA view filtered by fail = %d %+v", w.Code, qa)
	}
	if w := getJSON(t, h, "/test-cases?status=nope", tokenA); w.Code != http.StatusBadRequest {
		t.Errorf("unknown status = %d, want 400", w.Code)
	}
	if w := getJSON(t, h, "/test-cases?limit=x", tokenA); w.Code != http.StatusBadRequest {
		t.Errorf("bad limit = %d, want 400", w.Code)
	}
}

func TestRunKey_TestCases(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/test-cases", rig.keyA, map[string]any{"title": "by the run"})
	if w.Code != http.StatusCreated {
		t.Fatalf("run adding a case to its task = %d: %s", w.Code, w.Body.String())
	}
	if c := decodeCases(t, w); c[0].CreatedBy != "agent:developer" {
		t.Errorf("created_by = %q, want agent:developer", c[0].CreatedBy)
	}
	w = keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/test-cases/TC-1/runs", rig.keyA, map[string]any{"verdict": "fail"})
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"author":"agent:developer"`) {
		t.Errorf("run recording a run = %d: %s", w.Code, w.Body.String())
	}
	if w = keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.b+"/test-cases", rig.keyA, map[string]any{"title": "planted"}); w.Code != http.StatusForbidden {
		t.Errorf("run adding a case to an unrelated task = %d, want 403", w.Code)
	}

	outsider, _ := mintWorkspaceUser(t, rig.srv, "tc-outsider@test.com", "Out")
	elsewhere := createTaskViaAPI(t, rig.h, outsider, "another workspace")
	addCases(t, rig.h, outsider, elsewhere, map[string]any{"title": "not yours"})
	w = keyDo(t, rig.h, http.MethodGet, "/test-cases", rig.keyA, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "not yours") || !strings.Contains(w.Body.String(), "by the run") {
		t.Errorf("run key QA view = %d: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_CloneCopiesTestCases(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "tc-clone@test.com", "TCl", "SecurePassword1")
	id := createTaskViaAPI(t, h, token, "original")
	addCases(t, h, token, id, map[string]any{"cases": []map[string]any{{"title": "first"}, {"title": "second", "key": "X-1"}}})
	postJSONAuth(t, h, "/tasks/"+id+"/test-cases/TC-1/runs", token, map[string]any{"verdict": "pass"})

	w := postJSONAuth(t, h, "/tasks/"+id+"/clone", token, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("clone = %d: %s", w.Code, w.Body.String())
	}
	var clone task.Task
	json.Unmarshal(w.Body.Bytes(), &clone)
	if len(clone.TestCases) != 2 || clone.TestCases[1].Key != "X-1" {
		t.Fatalf("clone cases = %+v", clone.TestCases)
	}
	if c := clone.TestCases[0]; c.Status != "pending" || c.Covered || len(c.Runs) != 0 {
		t.Errorf("cloned case not reset: %+v", c)
	}
}
