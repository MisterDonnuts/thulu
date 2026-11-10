package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func askBody() map[string]any {
	return map[string]any{
		"kind": "question", "blocking": true, "context": "Two would work.", "default": "",
		"questions": []map[string]any{{
			"header": "Database", "question": "Which database?", "multi_select": false, "allow_other": true,
			"options": []map[string]any{{"label": "Postgres", "description": ""}, {"label": "SQLite", "description": ""}},
		}},
	}
}

func runningA(t *testing.T, rig *runScopeRig) {
	t.Helper()
	stage, status := task.Stage("in_progress"), task.StatusRunning
	run, inst := "ctr-a", "developer-task-"+rig.a+"-in_progress-1"
	if _, err := rig.srv.taskStore.Update(t.Context(), rig.a, task.Patch{
		Stage: &stage, Status: &status, CurrentRunID: &run, CurrentInstance: &inst,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
}

type askResponse struct {
	Question    task.Question `json:"question"`
	CommentID   string        `json:"comment_id"`
	Instruction string        `json:"instruction"`
}

func askAsA(t *testing.T, rig *runScopeRig) askResponse {
	t.Helper()
	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/questions", rig.keyA, askBody())
	if w.Code != http.StatusCreated {
		t.Fatalf("ask = %d: %s", w.Code, w.Body.String())
	}
	var got askResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestQuestions_WhoMayAsk(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	path := "/tasks/" + rig.a + "/questions"

	if w := keyDo(t, rig.h, http.MethodPost, path, rig.keyA, askBody()); w.Code != http.StatusConflict {
		t.Errorf("ask on an idle task = %d, want 409: %s", w.Code, w.Body.String())
	}
	runningA(t, rig)

	refused := map[string]string{
		"a person":               rig.token,
		"a subtask's run":        rig.keyA1,
		"an unrelated run":       rig.keyB1,
		"the auditor":            rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: auth.AuditorAgent, Stage: "in_progress"}),
		"a run of another stage": rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: "developer", Stage: "review"}),
		"another agent":          rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: "tester", Stage: "in_progress"}),
		"an operator key":        mintKey(t, rig.srv, rig.ws, auth.RoleDeveloper, auth.PermTasksRead, auth.PermTasksWrite),
	}
	for who, cred := range refused {
		var w = keyDo(t, rig.h, http.MethodPost, path, cred, askBody())
		if cred == rig.token {
			w = postJSONAuth(t, rig.h, path, cred, askBody())
		}
		if w.Code != http.StatusForbidden && w.Code != http.StatusConflict {
			t.Errorf("%s asked: %d, want 403 or 409: %s", who, w.Code, w.Body.String())
		}
	}

	got := askAsA(t, rig)
	if got.Question.ID == "" || got.CommentID != got.Question.CommentID || got.Instruction == "" {
		t.Errorf("response = %+v, want the question, its comment and an instruction", got)
	}
	if got.Question.RunID != "ctr-a" || got.Question.Agent != "developer" || !got.Question.Blocking {
		t.Errorf("question = %+v, want the run, agent and blocking taken from the task and key", got.Question)
	}

	var full task.Task
	if err := json.Unmarshal(getJSON(t, rig.h, "/tasks/"+rig.a, rig.token).Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Questions) != 1 || full.Questions[0].ID != got.Question.ID {
		t.Errorf("GET /tasks/{id} questions = %+v, want the question", full.Questions)
	}
}

func TestQuestions_AskLimitAndValidation(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	runningA(t, rig)
	path := "/tasks/" + rig.a + "/questions"

	bad := askBody()
	bad["questions"] = []map[string]any{}
	if w := keyDo(t, rig.h, http.MethodPost, path, rig.keyA, bad); w.Code != http.StatusBadRequest {
		t.Errorf("no questions = %d, want 400", w.Code)
	}
	for range task.MaxQuestionsPerRun {
		askAsA(t, rig)
	}
	if w := keyDo(t, rig.h, http.MethodPost, path, rig.keyA, askBody()); w.Code != http.StatusTooManyRequests {
		t.Errorf("past the per-run limit = %d, want 429: %s", w.Code, w.Body.String())
	}
}

func TestQuestions_AnswerAndCancel(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	runningA(t, rig)
	q1 := askAsA(t, rig).Question
	q2 := askAsA(t, rig).Question
	answer := map[string]any{"answers": []map[string]any{{"question_index": 0, "selected": []string{"Postgres"}}}}
	answerPath := "/tasks/" + rig.a + "/questions/" + q1.ID + "/answer"
	cancelPath := "/tasks/" + rig.a + "/questions/" + q2.ID + "/cancel"

	for _, p := range []string{answerPath, cancelPath} {
		if w := keyDo(t, rig.h, http.MethodPost, p, rig.keyA, answer); w.Code != http.StatusForbidden {
			t.Errorf("the run's own key on %s = %d, want 403", p, w.Code)
		}
		if w := keyDo(t, rig.h, http.MethodPost, p, mintKey(t, rig.srv, rig.ws, auth.RoleViewer, auth.PermTasksRead, auth.PermTasksWrite), answer); w.Code != http.StatusForbidden {
			t.Errorf("a viewer on %s = %d, want 403", p, w.Code)
		}
	}
	if w := postJSONAuth(t, rig.h, answerPath, rig.token, map[string]any{"answers": []map[string]any{{"question_index": 0, "selected": []string{"MySQL"}}}}); w.Code != http.StatusBadRequest {
		t.Errorf("an option that was not offered = %d, want 400", w.Code)
	}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.a+"/questions/nope/answer", rig.token, answer); w.Code != http.StatusNotFound {
		t.Errorf("unknown question = %d, want 404", w.Code)
	}
	if w := postJSONAuth(t, rig.h, "/tasks/"+rig.b+"/questions/"+q1.ID+"/answer", rig.token, answer); w.Code != http.StatusNotFound {
		t.Errorf("question under another task = %d, want 404", w.Code)
	}

	w := postJSONAuth(t, rig.h, answerPath, rig.token, answer)
	var got task.Question
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("answer = %d: %s", w.Code, w.Body.String())
	}
	if got.Status != task.QuestionAnswered || got.AnsweredBy != "run-scope@test.com" {
		t.Errorf("answered = %+v, want answered and signed by the person", got)
	}
	if w := postJSONAuth(t, rig.h, answerPath, rig.token, answer); w.Code != http.StatusConflict {
		t.Errorf("answering twice = %d, want 409", w.Code)
	}

	w = postJSONAuth(t, rig.h, cancelPath, rig.token, nil)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Status != task.QuestionCanceled {
		t.Fatalf("cancel = %d: %s", w.Code, w.Body.String())
	}
}

func TestQuestions_ThreadReplyAnswers(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	runningA(t, rig)
	q := askAsA(t, rig).Question

	if _, code := postComment(t, rig, rig.a, map[string]any{"body": "Postgres, please", "parent_id": q.CommentID}); code != http.StatusCreated {
		t.Fatalf("reply = %d", code)
	}
	got, err := rig.srv.taskStore.GetQuestion(t.Context(), rig.a, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.QuestionAnswered || got.Answers == nil || got.Answers.Answers[0].Other != "Postgres, please" {
		t.Errorf("question = %+v, want answered with the reply", got)
	}
}

func TestQuestions_StageMoveCancels(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	runningA(t, rig)
	q := askAsA(t, rig).Question

	req := map[string]any{"stage": "review"}
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a, mintKey(t, rig.srv, rig.ws, auth.RoleDeveloper, auth.PermTasksRead, auth.PermTasksWrite), req); w.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", w.Code, w.Body.String())
	}
	got, _ := rig.srv.taskStore.GetQuestion(t.Context(), rig.a, q.ID)
	if got.Status != task.QuestionCanceled {
		t.Errorf("status = %s, want canceled by the move", got.Status)
	}
}

func TestQuestions_Brief(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	runningA(t, rig)
	askAsA(t, rig)

	asRun := keyDo(t, rig.h, http.MethodGet, "/tasks/"+rig.a+"/brief", rig.keyA, nil).Body.String()
	asPerson := getJSON(t, rig.h, "/tasks/"+rig.a+"/brief", rig.token).Body.String()
	for name, brief := range map[string]string{"run": asRun, "person": asPerson} {
		if !strings.Contains(brief, "Which database?") || !strings.Contains(brief, "## Open questions") {
			t.Errorf("%s's brief does not list the open question:\n%s", name, brief)
		}
	}
	if !strings.Contains(asRun, "task_ask") {
		t.Errorf("the stage run's brief does not say how to ask:\n%s", asRun)
	}
	if strings.Contains(asPerson, "## Asking a person") {
		t.Errorf("a person's brief tells them how to ask:\n%s", asPerson)
	}
}

func TestQuestions_ContinuationRunCannotAsk(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	runningA(t, rig)
	inst := "developer-task-" + rig.a + "-in_progress-1"
	if err := rig.srv.taskStore.RecordEvent(t.Context(), rig.a, task.EventAgentRunStarted, inst, "octopus", "run",
		map[string]string{"launch_kind": string(task.LaunchContinuation)}); err != nil {
		t.Fatal(err)
	}
	if w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/questions", rig.keyA, askBody()); w.Code != http.StatusConflict {
		t.Errorf("continuation asks = %d, want 409: %s", w.Code, w.Body.String())
	}
}
