package api

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const givenValue = "value-that-never-leaks"

// Giving access stores the secret in the project's evidence environment, queues the cases, answers
// the stop with a decision that names the secret, relaunches Testing and briefs it. The value shows
// up nowhere but the environment's store. Not parallel: it captures the process log.
func TestGiveTestCaseAccess(t *testing.T) {
	var logs bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	srv, h, evStore := evidenceServer(t)
	token, ws := registerUserInWorkspace(t, h, "access-owner@example.com", "Owner", "")
	pid := createProject(t, h, token, "shop")
	other := createProject(t, h, token, "blog")
	envOf := func(projectID, name string) evidence.Environment {
		w := postJSONAuth(t, h, "/evidence/environments", token, map[string]any{"project_id": projectID, "name": name,
			"base_url": "https://" + name + ".example.com"})
		if w.Code != http.StatusCreated {
			t.Fatalf("create environment %s = %d: %s", name, w.Code, w.Body)
		}
		return decode[evidence.Environment](t, w)
	}
	staging, foreign := envOf(pid, "staging"), envOf(other, "blog-staging")

	id := taskIDFor(t, h, token, "checkout")
	if _, err := srv.taskStore.Update(t.Context(), id, task.Patch{ProjectIDs: &[]string{pid},
		StageAssignments: map[task.Stage]string{"backlog": "developer"}}); err != nil {
		t.Fatal(err)
	}
	l := &acceptingLauncher{agent: &types.AgentType{Name: "developer"}}
	srv.taskDispatcher = task.NewDispatcher(srv.taskStore, l, srv.projectStore, nil, nil).WithBoards(srv.boards)
	ctx := t.Context()
	if _, err := srv.taskStore.AddTestCases(ctx, id, "tester", []task.TestCase{{Title: "pays as admin"}, {Title: "logout"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.taskStore.RecordTestCaseRun(ctx, id, "TC-1", task.TestCaseRun{Verdict: task.TestCaseBlocked,
		Needs: []task.Need{{Kind: "account", Label: "Admin login", Secrets: []task.NeedSecret{{Name: "QA_PASSWORD"}}}}, Author: "tester"}); err != nil {
		t.Fatal(err)
	}
	stop := seedStop(t, srv, id)
	path := "/tasks/" + id + "/test-cases/access"
	body := map[string]any{"cases": []string{"TC-1"}, "environment_id": staging.ID, "secrets": map[string]string{"QA_PASSWORD": givenValue}}

	viewer := mintKey(t, srv, ws, auth.RoleViewer, auth.PermAll)
	if w := keyDo(t, h, http.MethodPost, path, viewer, body); w.Code != http.StatusForbidden {
		t.Errorf("viewer = %d, want 403", w.Code)
	}
	runKey, _, err := srv.authStore.CreateRunScopedAPIKey(ctx, auth.RunScope{TaskID: id, Agent: "developer", Stage: "backlog"}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if w := keyDo(t, h, http.MethodPost, path, runKey, body); w.Code != http.StatusForbidden || codeOf(t, w.Body.Bytes()) != "api.tasks.errors.access_person_only" {
		t.Errorf("run key = %d %s, want 403 access_person_only", w.Code, w.Body)
	}
	// An operator or automation key with the developer role is not a person either.
	operator := mintKey(t, srv, ws, auth.RoleDeveloper, auth.PermAll)
	if w := keyDo(t, h, http.MethodPost, path, operator, body); w.Code != http.StatusForbidden || codeOf(t, w.Body.Bytes()) != "api.tasks.errors.access_person_only" {
		t.Errorf("operator key = %d %s, want 403 access_person_only", w.Code, w.Body)
	}
	for name, tc := range map[string]struct {
		body map[string]any
		code int
	}{
		"unknown case":    {map[string]any{"cases": []string{"TC-9"}, "environment_id": staging.ID, "secrets": map[string]string{"QA_PASSWORD": "x"}}, http.StatusNotFound},
		"no cases":        {map[string]any{"cases": []string{}, "environment_id": staging.ID}, http.StatusBadRequest},
		"other project":   {map[string]any{"cases": []string{"TC-1"}, "environment_id": foreign.ID, "secrets": map[string]string{"QA_PASSWORD": "x"}}, http.StatusBadRequest},
		"secrets, no env": {map[string]any{"cases": []string{"TC-1"}, "secrets": map[string]string{"QA_PASSWORD": "x"}}, http.StatusBadRequest},
		"empty value":     {map[string]any{"cases": []string{"TC-1"}, "environment_id": staging.ID, "secrets": map[string]string{"QA_PASSWORD": " "}}, http.StatusBadRequest},
		"invalid name":    {map[string]any{"cases": []string{"TC-1"}, "environment_id": staging.ID, "secrets": map[string]string{"qa pass": "x"}}, http.StatusBadRequest},
		"missing env":     {map[string]any{"cases": []string{"TC-1"}, "environment_id": "nope", "secrets": map[string]string{"QA_PASSWORD": "x"}}, http.StatusNotFound},
	} {
		if w := postJSONAuth(t, h, path, token, tc.body); w.Code != tc.code {
			t.Errorf("%s = %d, want %d: %s", name, w.Code, tc.code, w.Body)
		}
	}
	if c, _ := srv.taskStore.GetTestCase(ctx, id, "TC-1"); c.Status != task.TestCaseBlocked {
		t.Fatalf("a refused request changed TC-1 to %s", c.Status)
	}
	if secrets, _ := evStore.Secrets(ctx, ws, staging.ID); len(secrets) != 0 {
		t.Fatalf("a refused request stored secrets: %v", secrets)
	}

	w := postJSONAuth(t, h, path, token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("access = %d: %s", w.Code, w.Body)
	}
	res := decode[AccessResult](t, w)
	if !slices.Equal(res.Cases, []string{"TC-1"}) || !slices.Equal(res.Secrets, []string{"QA_PASSWORD"}) || res.CommentID == "" {
		t.Errorf("result = %+v", res)
	}
	if l.launches.Load() == 0 {
		t.Error("Testing was not relaunched")
	}
	if secrets, err := evStore.Secrets(ctx, ws, staging.ID); err != nil || secrets["QA_PASSWORD"] != givenValue {
		t.Errorf("stored secrets = %v %v", secrets, err)
	}
	if c, _ := srv.taskStore.GetTestCase(ctx, id, "TC-1"); c.Status != task.TestCaseQueued {
		t.Errorf("TC-1 = %s, want queued", c.Status)
	}
	ds := decisionsOf(t, srv, id)
	if len(ds) != 1 || ds[0].ID != res.CommentID || ds[0].ParentID != stop.ID || ds[0].Meta.Action != task.DecisionAccess ||
		!slices.Equal(ds[0].Meta.Secrets, []string{"QA_PASSWORD"}) || !slices.Equal(ds[0].Meta.Cases, []string{"TC-1"}) ||
		!strings.Contains(ds[0].Body, "QA_PASSWORD") {
		t.Errorf("decisions = %+v", ds)
	}
	if open, _ := srv.taskStore.OpenStopThread(ctx, id); open != "" {
		t.Error("the stop is still open with no case blocked")
	}
	given := eventsOf(t, srv, id, task.EventTestCasesAccessGiven)
	if len(given) != 1 || payloadOf(t, given[0])["environment_id"] != staging.ID || payloadOf(t, given[0])["comment_id"] != res.CommentID {
		t.Errorf("access events = %+v", given)
	}
	if ev := eventsOf(t, srv, id, task.EventRelaunchRequested); len(ev) != 1 {
		t.Errorf("relaunch_requested events = %d, want 1", len(ev))
	}

	waitForIdle(t, srv, id)
	brief := getJSON(t, h, "/tasks/"+id+"/brief", token)
	for _, want := range []string{"## Access given", "TC-1", "QA_PASSWORD", "staging"} {
		if !strings.Contains(brief.Body.String(), want) {
			t.Errorf("brief lacks %q:\n%s", want, brief.Body)
		}
	}

	// The access belongs to the stage it was given in: a run of another stage is not told to run
	// only these cases.
	setStage := func(stage task.Stage) {
		t.Helper()
		if _, err := srv.taskStore.Update(ctx, id, task.Patch{Stage: &stage}); err != nil {
			t.Fatal(err)
		}
	}
	setStage("in_progress")
	if other := getJSON(t, h, "/tasks/"+id+"/brief", token); strings.Contains(other.Body.String(), "## Access given") {
		t.Error("the brief of another stage lists the access")
	}
	setStage("backlog")

	// The value appears in no answer, comment, event, brief or log line.
	comments, _ := srv.taskStore.ListComments(ctx, id)
	events, _ := srv.taskStore.ListEvents(ctx, id)
	everything, _ := json.Marshal(map[string]any{"comments": comments, "events": events})
	detail := getJSON(t, h, "/tasks/"+id, token)
	envs := getJSON(t, h, "/evidence/environments/"+staging.ID, token)
	for where, text := range map[string]string{
		"response": w.Body.String(), "store": string(everything), "brief": brief.Body.String(),
		"task": detail.Body.String(), "environment": envs.Body.String(), "log": logs.String(),
	} {
		if strings.Contains(text, givenValue) {
			t.Errorf("the secret value leaked into the %s", where)
		}
	}

	// The next run of the case takes it out of the queue, and the brief stops naming it.
	if w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases/TC-1/runs", token, map[string]any{"verdict": "pass"}); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("record run = %d: %s", w.Code, w.Body)
	}
	if c, _ := srv.taskStore.GetTestCase(ctx, id, "TC-1"); c.Status != task.TestCasePass {
		t.Errorf("TC-1 after a run = %s, want pass", c.Status)
	}
	if brief := getJSON(t, h, "/tasks/"+id+"/brief", token); strings.Contains(brief.Body.String(), "## Access given") {
		t.Error("the brief still lists access once no case is queued")
	}
}

// Access without secrets (a device handed over, an environment fixed) only queues and relaunches.
func TestGiveTestCaseAccess_WithoutSecrets(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "access-plain@test.com", "Plain")
	id := taskIDFor(t, h, token, "device")
	if _, err := srv.taskStore.Update(t.Context(), id, task.Patch{StageAssignments: map[task.Stage]string{"backlog": "developer"}}); err != nil {
		t.Fatal(err)
	}
	l := &acceptingLauncher{agent: &types.AgentType{Name: "developer"}}
	srv.taskDispatcher = task.NewDispatcher(srv.taskStore, l, srv.projectStore, nil, nil).WithBoards(srv.boards)
	if _, err := srv.taskStore.AddTestCases(t.Context(), id, "tester", []task.TestCase{{Title: "on a phone"}}); err != nil {
		t.Fatal(err)
	}
	w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases/access", token, map[string]any{"cases": []string{"TC-1"}, "reason": "plugged in the Pixel"})
	if w.Code != http.StatusOK {
		t.Fatalf("access = %d: %s", w.Code, w.Body)
	}
	ds := decisionsOf(t, srv, id)
	if len(ds) != 1 || ds[0].Body != "plugged in the Pixel" || len(ds[0].Meta.Secrets) != 0 {
		t.Errorf("decisions = %+v", ds)
	}
}

func TestCriteriaEndpoints(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "criteria@test.com", "Crit")
	id := taskIDFor(t, h, token, "criteria")
	path := "/tasks/" + id + "/criteria"

	if w := putJSON(t, h, path, map[string]any{"criteria": []map[string]string{{"key": "AC-1", "text": "a"}, {"key": "AC-1", "text": "b"}}}, token); w.Code != http.StatusBadRequest {
		t.Errorf("repeated key = %d, want 400: %s", w.Code, w.Body)
	}
	w := putJSON(t, h, path, map[string]any{"criteria": []map[string]string{{"key": "AC-1", "text": "logs in"}, {"key": "AC-2", "text": "logs out"}}}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", w.Code, w.Body)
	}
	got := decode[[]task.Criterion](t, getJSON(t, h, path, token))
	if len(got) != 2 || got[1].Key != "AC-2" {
		t.Fatalf("get = %+v", got)
	}
	sections := getJSON(t, h, "/tasks/"+id+"/sections", token).Body.String()
	if !strings.Contains(sections, "AC-1  logs in") {
		t.Errorf("section not rewritten: %s", sections)
	}

	// Writing the section as markdown fills the table.
	if w := putJSON(t, h, "/tasks/"+id+"/sections/acceptance-criteria", map[string]any{"content": "- **AC-1**: logs in\n- **AC-3**: resets the password"}, token); w.Code != http.StatusOK {
		t.Fatalf("set section = %d: %s", w.Code, w.Body)
	}
	got = decode[[]task.Criterion](t, getJSON(t, h, path, token))
	if len(got) != 2 || got[1].Key != "AC-3" || got[1].Text != "resets the password" {
		t.Errorf("rows after a markdown section = %+v", got)
	}

	// Covers must name a criterion the task has.
	w = postJSONAuth(t, h, "/tasks/"+id+"/test-cases", token, map[string]any{"title": "reset", "covers": []string{"AC-2"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "AC-1, AC-3") {
		t.Errorf("unknown covers = %d: %s", w.Code, w.Body)
	}
	if w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases", token, map[string]any{"title": "reset", "covers": []string{"AC-3"}, "preconditions": "a user"}); w.Code != http.StatusCreated {
		t.Errorf("valid covers = %d: %s", w.Code, w.Body)
	}

	approve := postJSONAuth(t, h, path+"/approve", token, map[string]any{"keys": []string{"AC-1"}})
	if approve.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", approve.Code, approve.Body)
	}
	if got := decode[[]task.Criterion](t, approve); got[0].ApprovedBy == "" || got[1].ApprovedBy != "" {
		t.Errorf("approved = %+v", got)
	}
	runKey, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(), auth.RunScope{TaskID: id, Agent: "developer", Stage: "backlog"}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if w := keyDo(t, h, http.MethodPost, path+"/approve", runKey, map[string]any{}); w.Code != http.StatusForbidden {
		t.Errorf("run key approving = %d, want 403", w.Code)
	}
	operator := mintKey(t, srv, ws, auth.RoleDeveloper, auth.PermAll)
	if w := keyDo(t, h, http.MethodPost, path+"/approve", operator, map[string]any{}); w.Code != http.StatusForbidden ||
		codeOf(t, w.Body.Bytes()) != "api.tasks.errors.criteria_approve_person_only" {
		t.Errorf("operator key approving = %d %s, want 403 criteria_approve_person_only", w.Code, w.Body)
	}
}

func TestNeedKindsEndpoint(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "needs@test.com", "Needs")
	w := getJSON(t, h, "/test-cases/need-kinds", token)
	if w.Code != http.StatusOK {
		t.Fatalf("need-kinds = %d: %s", w.Code, w.Body)
	}
	kinds := decode[[]needKindView](t, w)
	if len(kinds) != len(task.NeedKinds()) {
		t.Fatalf("kinds = %d, want %d", len(kinds), len(task.NeedKinds()))
	}
	for _, k := range kinds {
		if k.Title == "" || k.Title == k.TitleKey || k.Help == "" || k.Help == k.HelpKey {
			t.Errorf("kind %s not localized: %+v", k.Kind, k)
		}
	}
	id := taskIDFor(t, h, token, "needs")
	if w := postJSONAuth(t, h, "/tasks/"+id+"/test-cases", token, map[string]any{"title": "a"}); w.Code != http.StatusCreated {
		t.Fatal(w.Body)
	}
	run := "/tasks/" + id + "/test-cases/TC-1/runs"
	if w := postJSONAuth(t, h, run, token, map[string]any{"verdict": "blocked", "needs": []map[string]any{{"kind": "spaceship"}}}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown need kind = %d, want 400: %s", w.Code, w.Body)
	}
	if w := postJSONAuth(t, h, run, token, map[string]any{"verdict": "fail", "observed": strings.Repeat("x", 281)}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "note") {
		t.Errorf("long observed = %d, want 400 naming note: %s", w.Code, w.Body)
	}
	w = postJSONAuth(t, h, run, token, map[string]any{"verdict": "blocked", "tried": "partial", "tried_result": "stuck at 2FA", "writes_data": true,
		"needs": []map[string]any{{"kind": "account", "label": "2FA user", "secrets": []map[string]string{{"name": "OTP_SEED", "label": "seed"}}}}})
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("blocked run = %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"OTP_SEED"`) || !strings.Contains(w.Body.String(), `"round":1`) {
		t.Errorf("run answer lacks the needs or the round: %s", w.Body)
	}
}

// A task run reads a secret only when a person gave it to that task with "Give the agent access",
// and only with a key of the stage the task is in. Another secret of the same environment, a key of
// another column and a key of another task all get the same 404.
func TestTaskRunReadsGivenSecret(t *testing.T) {
	srv, h, evStore := evidenceServer(t)
	token, ws := registerUserInWorkspace(t, h, "secret-reader@example.com", "Owner", "")
	pid := createProject(t, h, token, "shop")
	w := postJSONAuth(t, h, "/evidence/environments", token, map[string]any{"project_id": pid, "name": "staging",
		"base_url": "https://staging.example.com"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create environment = %d: %s", w.Code, w.Body)
	}
	staging := decode[evidence.Environment](t, w)
	ctx := t.Context()
	other := "not-given"
	if _, err := evStore.SetSecrets(ctx, ws, staging.ID, map[string]*string{"ADMIN_TOKEN": &other}); err != nil {
		t.Fatal(err)
	}

	id := taskIDFor(t, h, token, "checkout")
	otherTask := taskIDFor(t, h, token, "refunds")
	for _, tid := range []string{id, otherTask} {
		if _, err := srv.taskStore.Update(ctx, tid, task.Patch{ProjectIDs: &[]string{pid},
			StageAssignments: map[task.Stage]string{"backlog": "developer"}}); err != nil {
			t.Fatal(err)
		}
	}
	l := &acceptingLauncher{agent: &types.AgentType{Name: "developer"}}
	srv.taskDispatcher = task.NewDispatcher(srv.taskStore, l, srv.projectStore, nil, nil).WithBoards(srv.boards)
	if _, err := srv.taskStore.AddTestCases(ctx, id, "tester", []task.TestCase{{Title: "pays as admin"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.taskStore.RecordTestCaseRun(ctx, id, "TC-1", task.TestCaseRun{Verdict: task.TestCaseBlocked,
		Needs: []task.Need{{Kind: "account", Label: "Admin login", Secrets: []task.NeedSecret{{Name: "QA_PASSWORD"}}}}, Author: "tester"}); err != nil {
		t.Fatal(err)
	}
	keyOf := func(taskID, stage string) string {
		k, _, err := srv.authStore.CreateRunScopedAPIKey(ctx, auth.RunScope{TaskID: taskID, Agent: "developer", Stage: stage}, ws, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	read := func(key, name string) *httptest.ResponseRecorder {
		return keyDo(t, h, http.MethodGet, "/evidence/recording/secrets/"+name, key, nil)
	}
	run := keyOf(id, "backlog")
	if w := read(run, "QA_PASSWORD"); w.Code != http.StatusNotFound {
		t.Fatalf("before access = %d %s, want 404", w.Code, w.Body)
	}

	w = postJSONAuth(t, h, "/tasks/"+id+"/test-cases/access", token, map[string]any{"cases": []string{"TC-1"},
		"environment_id": staging.ID, "secrets": map[string]string{"QA_PASSWORD": givenValue}})
	if w.Code != http.StatusOK {
		t.Fatalf("access = %d: %s", w.Code, w.Body)
	}
	waitForIdle(t, srv, id)

	w = read(run, "QA_PASSWORD")
	if w.Code != http.StatusOK || decode[map[string]string](t, w)["value"] != givenValue || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("given secret = %d %s", w.Code, w.Body)
	}
	for name, tc := range map[string]struct{ key, secret string }{
		"secret nobody gave":  {run, "ADMIN_TOKEN"},
		"key of other column": {keyOf(id, "testing"), "QA_PASSWORD"},
		"key of other task":   {keyOf(otherTask, "backlog"), "QA_PASSWORD"},
	} {
		if w := read(tc.key, tc.secret); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), givenValue) ||
			strings.Contains(w.Body.String(), other) {
			t.Errorf("%s = %d %s, want 404", name, w.Code, w.Body)
		}
	}
}
