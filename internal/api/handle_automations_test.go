package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

const testSpec = `name: nightly
trigger:
  schedule: "0 3 * * *"
steps:
  - id: ticket
    task:
      title: "nightly check"
      fingerprint: nightly
`

func automationServer(t *testing.T) *Server {
	t.Helper()
	store, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatalf("new automation store: %v", err)
	}
	srv := &Server{}
	engine := automation.NewEngine(store, automation.Deps{})
	srv.SetAutomations(store, engine, automation.NewScheduler(store, engine))
	return srv
}

func autoRequest(method, path, wid, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	return r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{
		UserID: "u-" + wid, Email: "admin@" + wid + ".test",
		WorkspaceID: wid, Role: auth.RoleAdmin,
	}))
}

func TestAutomationStaysInsideItsWorkspace(t *testing.T) {
	t.Parallel()
	srv := automationServer(t)

	w := httptest.NewRecorder()
	srv.handleCreateAutomation(w, autoRequest("POST", "/automations", "ws-a",
		`{"spec":`+strconv.Quote(testSpec)+`}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", w.Code, w.Body.String())
	}
	var created automation.Automation
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	for _, tc := range []struct {
		name    string
		method  string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"get", "GET", "", srv.handleGetAutomation},
		{"update", "PUT", `{"spec":"name: x\ntrigger:\n  manual: true\nsteps: []"}`, srv.handleUpdateAutomation},
		{"enabled", "PUT", `{"enabled":false}`, srv.handleSetAutomationEnabled},
		{"delete", "DELETE", "", srv.handleDeleteAutomation},
		{"run", "POST", "", srv.handleRunAutomation},
		{"runs", "GET", "", srv.handleListAutomationRuns},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := autoRequest(tc.method, "/automations/"+created.ID, "ws-b", tc.body)
			r.SetPathValue("id", created.ID)
			rec := httptest.NewRecorder()
			tc.handler(rec, r)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s from the other workspace = %d %s, want 404",
					tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	r := autoRequest("GET", "/automations/"+created.ID, "ws-a", "")
	r.SetPathValue("id", created.ID)
	rec := httptest.NewRecorder()
	srv.handleGetAutomation(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner get = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var back automation.Automation
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !back.Enabled || back.Name != "nightly" {
		t.Fatalf("owner get returned %+v, want the enabled nightly recipe", back)
	}

	listRec := httptest.NewRecorder()
	srv.handleListAutomations(listRec, autoRequest("GET", "/automations", "ws-b", ""))
	var list struct {
		Automations []automation.Automation `json:"automations"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Automations) != 0 {
		t.Fatalf("other workspace sees %d automations, want 0", len(list.Automations))
	}
}

func TestValidateAnswersWithoutStoring(t *testing.T) {
	t.Parallel()
	srv := automationServer(t)

	rec := httptest.NewRecorder()
	srv.handleValidateAutomation(rec, autoRequest("POST", "/automations/validate", "ws-a",
		`{"spec":`+strconv.Quote(testSpec)+`}`))
	var ok struct {
		Valid    bool     `json:"valid"`
		Name     string   `json:"name"`
		Trigger  string   `json:"trigger"`
		NextRuns []string `json:"next_runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok.Valid || ok.Name != "nightly" || ok.Trigger != automation.TriggerSchedule {
		t.Fatalf("validate = %+v, want a valid scheduled recipe", ok)
	}
	if len(ok.NextRuns) != 5 {
		t.Fatalf("next_runs = %d entries, want 5 — the cron preview is the point", len(ok.NextRuns))
	}

	bad := httptest.NewRecorder()
	srv.handleValidateAutomation(bad, autoRequest("POST", "/automations/validate", "ws-a",
		`{"spec":"name: broken\ntrigger:\n  schedule: \"not a cron\"\nsteps: []"}`))
	if bad.Code != http.StatusOK {
		t.Fatalf("invalid spec answered %d, want 200 with valid:false", bad.Code)
	}
	var nope struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bad.Body.Bytes(), &nope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if nope.Valid || nope.Error == "" {
		t.Fatalf("broken spec = %+v, want valid:false with a reason", nope)
	}

	listRec := httptest.NewRecorder()
	srv.handleListAutomations(listRec, autoRequest("GET", "/automations", "ws-a", ""))
	var list struct {
		Automations []automation.Automation `json:"automations"`
	}
	_ = json.Unmarshal(listRec.Body.Bytes(), &list)
	if len(list.Automations) != 0 {
		t.Fatalf("validate stored %d automations, want 0", len(list.Automations))
	}
}

func TestAutomationRoutesAnswer503WhenNotWired(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	srv.handleListAutomations(rec, autoRequest("GET", "/automations", "ws-a", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("list without a store = %d, want 503", rec.Code)
	}
}

func TestAutomationsInvisibleWithoutAWorkspace(t *testing.T) {
	t.Parallel()
	srv := automationServer(t)
	w := httptest.NewRecorder()
	srv.handleCreateAutomation(w, autoRequest("POST", "/automations", "ws-a",
		`{"spec":`+strconv.Quote(testSpec)+`}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", w.Code, w.Body.String())
	}
	var created automation.Automation
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	w = httptest.NewRecorder()
	srv.handleListAutomations(w, autoRequest("GET", "/automations", "", ""))
	var list struct {
		Automations []automation.Automation `json:"automations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Automations) != 0 {
		t.Errorf("a caller with no workspace listed %d automations, want 0", len(list.Automations))
	}

	r := autoRequest("GET", "/automations/"+created.ID, "", "")
	r.SetPathValue("id", created.ID)
	w = httptest.NewRecorder()
	srv.handleGetAutomation(w, r)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Errorf("get from a caller with no workspace = %d, want 403 or 404", w.Code)
	}
}

func TestValidateChecksWhatTheRecipeNames(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	store, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(store, automation.Deps{})
	srv.SetAutomations(store, engine, automation.NewScheduler(store, engine))

	validate := func(spec string) (bool, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handleValidateAutomation(rec, autoRequest("POST", "/automations/validate", "ws-a",
			`{"spec":`+strconv.Quote(spec)+`}`))
		var out struct {
			Valid bool   `json:"valid"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return out.Valid, out.Error
	}

	for _, tc := range []struct{ name, spec, want string }{
		{"board", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    task: {title: x, board: soporte}\n", "soporte"},
		{"stage", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    task: {title: x, stage: revison}\n", "revison"},
		{"agent", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    mention: {task_id: t-1, agent: fantasma, body: hola}\n", "fantasma"},
		{"environment", "name: n\ntrigger:\n  metric: {environment: prod, metric: error_rate, op: \">\", threshold: 0.05, window: 10m}\nsteps:\n  - id: t\n    task: {title: x}\n", "prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid, msg := validate(tc.spec)
			if valid || !strings.Contains(msg, tc.want) {
				t.Errorf("validate = %v %q, want invalid naming %q", valid, msg, tc.want)
			}
		})
	}

	// The engine hands these fields to the task store unrendered, so a template there would fail
	// every run with "unknown board".
	for _, tc := range []struct{ name, spec, field string }{
		{"task board", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    task: {title: x, board: \"{{trigger.payload.board}}\"}\n", "board"},
		{"task stage", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    task: {title: x, stage: \"{{ vars.stage }}\"}\n", "stage"},
		{"task project", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    task: {title: x, project_ids: [\"{{ vars.p }}\"]}\n", "project_ids"},
		{"mention agent", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    mention: {task_id: \"{{ trigger.payload.task_id }}\", agent: \"{{ vars.a }}\", body: hola}\n", "agent"},
		{"query board", "name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    query: {source: tasks, board: \"{{ vars.b }}\"}\n", "board"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid, msg := validate(tc.spec)
			if valid || !strings.Contains(msg, tc.field+" does not take {{ }}") {
				t.Errorf("validate = %v %q, want a refusal naming %s as not templated", valid, msg, tc.field)
			}
		})
	}

	for _, spec := range []string{
		"name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    task: {title: x, board: soporte}\n",
		"name: n\ntrigger: {manual: true}\nsteps:\n  - id: t\n    task: {title: x, board: \"{{ }}\"}\n",
	} {
		rec := httptest.NewRecorder()
		srv.handleCreateAutomation(rec, autoRequest("POST", "/automations", "ws-a",
			`{"spec":`+strconv.Quote(spec)+`}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %q = %d %s, want 400", spec, rec.Code, rec.Body.String())
		}
	}
}

func TestDevModeAutomationLandsInTheFloorWithoutWorkspaces(t *testing.T) {
	srv, _ := newIntegrationServer(t)
	store, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(store, automation.Deps{})
	srv.SetAutomations(store, engine, automation.NewScheduler(store, engine))
	if !srv.noWorkspaces(t.Context()) {
		t.Fatal("the integration server starts with a workspace; this test needs none")
	}

	create := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/automations", strings.NewReader(`{"spec":`+strconv.Quote(testSpec)+`}`))
		w := httptest.NewRecorder()
		srv.handleCreateAutomation(w, asDevOperator(r))
		return w
	}
	w := create()
	if w.Code != http.StatusCreated {
		t.Fatalf("create with no workspaces = %d %s, want 201", w.Code, w.Body.String())
	}
	var a automation.Automation
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.WorkspaceID != defaultWorkspaceID {
		t.Errorf("workspace = %q, want the %q floor the board lives in", a.WorkspaceID, defaultWorkspaceID)
	}

	mintWorkspaceUser(t, srv, "auto-one@test.com", "AutoOne")
	mintWorkspaceUser(t, srv, "auto-two@test.com", "AutoTwo")
	if w := create(); w.Code != http.StatusBadRequest {
		t.Errorf("create with two workspaces = %d %s, want 400", w.Code, w.Body.String())
	}
}

func TestValidateReturnsTheTriggerWhenOnlyARefIsWrong(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	store, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(store, automation.Deps{})
	srv.SetAutomations(store, engine, automation.NewScheduler(store, engine))

	spec := "name: guard\ntrigger:\n  schedule: \"*/15 * * * *\"\n  tz: UTC\nsteps:\n" +
		"  - id: errs\n    query: {source: metric, environment: ENVIRONMENT_ID, metric: error_rate, window: 15m}\n"
	rec := httptest.NewRecorder()
	srv.handleValidateAutomation(rec, autoRequest("POST", "/automations/validate", "ws-a",
		`{"spec":`+strconv.Quote(spec)+`}`))
	var out struct {
		Valid   bool   `json:"valid"`
		Error   string `json:"error"`
		Trigger string `json:"trigger"`
		Parsed  struct {
			Trigger automation.Trigger `json:"trigger"`
		} `json:"parsed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if out.Valid || !strings.Contains(out.Error, "ENVIRONMENT_ID") {
		t.Fatalf("validate = %+v, want invalid naming the environment", out)
	}
	if out.Trigger != automation.TriggerSchedule || out.Parsed.Trigger.Schedule != "*/15 * * * *" {
		t.Errorf("trigger = %q parsed %+v, want the schedule back", out.Trigger, out.Parsed.Trigger)
	}
}
