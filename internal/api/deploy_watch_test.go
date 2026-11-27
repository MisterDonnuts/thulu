package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type watchEnv struct {
	batchEnv
	auto    *automation.Store
	project string
	release *task.Task
}

// newWatchEnv is the batch board with a release type, a GitHub project and a task in Release on it,
// the shape of the release agent's run.
func newWatchEnv(t *testing.T) watchEnv {
	t.Helper()
	e := newBatchEnv(t)
	autoStore, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(autoStore, e.srv.AutomationDeps())
	e.srv.SetAutomations(autoStore, engine, automation.NewScheduler(autoStore, engine))
	w := putJSON(t, e.h, "/boards/"+rbBoard+"/types", map[string]any{"types": []map[string]any{
		{"id": "feature", "label": "Feature", "default": true},
		{"id": "release", "label": "Release"},
	}}, e.token)
	if w.Code != http.StatusOK {
		t.Fatalf("set board types = %d %s", w.Code, w.Body)
	}
	pid := importWatchProject(t, e.srv, e.ws, "shop", "https://github.com/Acme/Shop.git")
	tk := e.task(t, "Deploy acme/shop", rbRelease, pid)
	return watchEnv{batchEnv: e, auto: autoStore, project: pid, release: tk}
}

func importWatchProject(t *testing.T, srv *Server, ws, name, repo string) string {
	t.Helper()
	id := "p-" + name + "-" + ws
	if err := srv.projectStore.ImportProject(t.Context(), project.Project{ID: id, Name: name, WorkspaceID: ws,
		Path: filepath.Join(t.TempDir(), name), RepoURL: repo, DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	return id
}

func decodeWatch(t *testing.T, w *httptest.ResponseRecorder) project.DeployWatch {
	t.Helper()
	var resp struct {
		Watch project.DeployWatch `json:"watch"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return resp.Watch
}

func (e watchEnv) watchKey(t *testing.T, tools ...string) string {
	t.Helper()
	return e.grantRun(t, e.release.ID, rbRelease, "octopus-api@read+"+strings.Join(tools, "+"))
}

func TestDeployWatchRunNeedsTheNamedTool(t *testing.T) {
	e := newWatchEnv(t)
	path := "/tasks/" + e.release.ID + "/deploy-watches"
	body := map[string]any{"source": "github_workflow", "target": "deploy.yml"}

	for grant, method := range map[string]string{
		"octopus-api@write":                          http.MethodPut,
		"octopus-api@release":                        http.MethodPut,
		"octopus-api@read+deploy_watch_list":         http.MethodPut,
		"thulu-github@read+deploy_watch_set":         http.MethodPut,
		"octopus-api@read+deploy_watch_set":          http.MethodGet,
		"octopus-api@read+deploy_watch_set+task_ask": http.MethodDelete,
	} {
		key := e.grantRun(t, e.release.ID, rbRelease, grant)
		target := path
		if method == http.MethodDelete {
			target += "/whatever"
		}
		w := keyDo(t, e.h, method, target, key, body)
		if w.Code != http.StatusForbidden || errCode(t, w) != "api.scope.errors.run_tool_not_named" {
			t.Errorf("%s with %q = %d %s, want 403 run_tool_not_named", method, grant, w.Code, w.Body)
		}
	}

	// Named, but through another task than its own.
	other := e.task(t, "someone else's", rbRelease, e.project)
	key := e.watchKey(t, "deploy_watch_set")
	if w := keyDo(t, e.h, http.MethodPut, "/tasks/"+other.ID+"/deploy-watches", key, body); w.Code != http.StatusForbidden ||
		errCode(t, w) != errRunTaskScope {
		t.Errorf("set through another task = %d %s, want 403 run_task_scope", w.Code, w.Body)
	}

	// A project the task does not have.
	foreign := importWatchProject(t, e.srv, e.ws, "billing", "https://github.com/acme/billing")
	w := keyDo(t, e.h, http.MethodPut, path, key, map[string]any{"project_id": foreign, "source": "github_workflow", "target": "deploy.yml"})
	if w.Code != http.StatusForbidden || errCode(t, w) != "api.deploy_watches.errors.project_not_of_task" {
		t.Errorf("set on a project not of the task = %d %s", w.Code, w.Body)
	}

	w = keyDo(t, e.h, http.MethodPut, path, key, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("named set = %d %s", w.Code, w.Body)
	}
	got := decodeWatch(t, w)
	if got.ProjectID != e.project || got.Target != ".github/workflows/deploy.yml" || got.TypeID != "release" ||
		got.BoardID != rbBoard || got.AutomationID == "" || got.CreatedBy != rbAgent || got.CreatedByTask != e.release.ID {
		t.Fatalf("watch = %+v", got)
	}
}

func TestDeployWatchSetUpdatesListsAndDeletes(t *testing.T) {
	e := newWatchEnv(t)
	ctx := t.Context()
	path := "/tasks/" + e.release.ID + "/deploy-watches"
	key := e.watchKey(t, "deploy_watch_set", "deploy_watch_list", "deploy_watch_delete")

	w := keyDo(t, e.h, http.MethodPut, path, key, map[string]any{"source": "http", "target": "https://shop.example.com/version"})
	if w.Code != http.StatusCreated {
		t.Fatalf("first set = %d %s", w.Code, w.Body)
	}
	first := decodeWatch(t, w)
	if first.IntervalSeconds != 900 {
		t.Errorf("default interval = %ds, want 900", first.IntervalSeconds)
	}
	w = keyDo(t, e.h, http.MethodPut, path, key, map[string]any{"source": "http", "target": "https://shop.example.com/version", "interval": "2h"})
	if w.Code != http.StatusOK {
		t.Fatalf("second set = %d %s, want 200 (an update)", w.Code, w.Body)
	}
	second := decodeWatch(t, w)
	if second.ID != first.ID || second.AutomationID != first.AutomationID || second.IntervalSeconds != 7200 {
		t.Fatalf("second set = %+v, want the same watch and automation with 2h", second)
	}
	a, err := e.auto.Get(ctx, first.AutomationID, automation.InWorkspace(e.ws))
	if err != nil {
		t.Fatal(err)
	}
	if a.ManagedBy != managedByDeployWatch+first.ID || a.Parsed.Trigger.Schedule != "@every 2h" || !a.Enabled {
		t.Errorf("automation = managed %q schedule %q enabled %v", a.ManagedBy, a.Parsed.Trigger.Schedule, a.Enabled)
	}
	if all, _ := e.auto.List(ctx, automation.InWorkspace(e.ws)); len(all) != 1 {
		t.Errorf("automations after two sets = %d, want 1", len(all))
	}

	for name, body := range map[string]map[string]any{
		"interval under 5m":   {"source": "http", "target": "https://shop.example.com/", "interval": "1m"},
		"interval on webhook": {"source": "github_deployment", "target": "production", "interval": "10m"},
		"unknown source":      {"source": "ssh", "target": "prod"},
		"url with password":   {"source": "http", "target": "https://u:p@shop.example.com/"},
		"workflow elsewhere":  {"source": "github_workflow", "target": "ci/deploy.yml"},
		"unreadable interval": {"source": "http", "target": "https://shop.example.com/", "interval": "soon"},
		"cloud, no such env":  {"source": "cloud", "target": "env-nope"},
	} {
		if w := keyDo(t, e.h, http.MethodPut, path, key, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, w.Code, w.Body)
		}
	}

	w = keyDo(t, e.h, http.MethodGet, path, key, nil)
	var listed struct {
		Watches []deployWatchView `json:"watches"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Watches) != 1 || listed.Watches[0].ProjectName != "shop" {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}

	w = keyDo(t, e.h, http.MethodDelete, path+"/"+first.ID, key, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if _, err := e.auto.Get(ctx, first.AutomationID, automation.InWorkspace(e.ws)); err == nil {
		t.Error("deleting the watch left its automation")
	}
	if _, err := e.srv.projectStore.GetDeployWatch(ctx, first.ID); err == nil {
		t.Error("the watch survived its delete")
	}
}

// A person needs admin to register a watch; the project page lists and deletes them.
func TestDeployWatchPersonRolesAndProjectPage(t *testing.T) {
	e := newWatchEnv(t)
	dev, _ := mintWorkspaceUserAs(t, e.srv, "dev@test.com", "Dev", auth.RoleDeveloper)
	path := "/tasks/" + e.release.ID + "/deploy-watches"
	body := map[string]any{"source": "github_deployment", "target": "production"}
	if w := putJSON(t, e.h, path, body, dev); w.Code != http.StatusForbidden {
		t.Errorf("developer set = %d %s, want 403", w.Code, w.Body)
	}
	w := putJSON(t, e.h, path, body, e.token)
	if w.Code != http.StatusCreated {
		t.Fatalf("owner set = %d %s", w.Code, w.Body)
	}
	watch := decodeWatch(t, w)

	g := getJSON(t, e.h, "/projects/"+e.project+"/deploy-watches", e.token)
	var listed struct {
		Watches []project.DeployWatch `json:"watches"`
	}
	if err := json.Unmarshal(g.Body.Bytes(), &listed); err != nil || len(listed.Watches) != 1 || listed.Watches[0].ID != watch.ID {
		t.Fatalf("project list = %d %s", g.Code, g.Body)
	}
	req := httptest.NewRequest(http.MethodDelete, "/projects/"+e.project+"/deploy-watches/"+watch.ID, nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("project delete = %d %s", rec.Code, rec.Body)
	}
	if _, err := e.auto.Get(t.Context(), watch.AutomationID, automation.InWorkspace(e.ws)); err == nil {
		t.Error("the project page delete left the automation")
	}
}

// The generated automation is the daemon's: the automations API refuses to edit or delete it, and
// no hand-written recipe may carry the deploy_watch step that reads with the daemon's access.
func TestManagedAutomationRefusesHandEdits(t *testing.T) {
	e := newWatchEnv(t)
	w := putJSON(t, e.h, "/tasks/"+e.release.ID+"/deploy-watches", map[string]any{"source": "github_workflow", "target": "Deploy"}, e.token)
	if w.Code != http.StatusCreated {
		t.Fatalf("set = %d %s", w.Code, w.Body)
	}
	watch := decodeWatch(t, w)
	spec := "name: mine\ntrigger:\n  manual: true\nsteps:\n  - id: s\n    deploy_watch:\n      watch: " + watch.ID + "\n"

	upd := putJSON(t, e.h, "/automations/"+watch.AutomationID, map[string]any{"spec": spec}, e.token)
	if upd.Code != http.StatusConflict || errCode(t, upd) != "api.automations.errors.managed_by_deploy_watch" {
		t.Errorf("edit managed = %d %s, want 409", upd.Code, upd.Body)
	}
	req := httptest.NewRequest(http.MethodDelete, "/automations/"+watch.AutomationID, nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("delete managed = %d %s, want 409", rec.Code, rec.Body)
	}
	if c := postJSONAuth(t, e.h, "/automations", e.token, map[string]any{"spec": spec}); c.Code != http.StatusBadRequest ||
		errCode(t, c) != "api.automations.errors.deploy_watch_step_reserved" {
		t.Errorf("hand-written deploy_watch step = %d %s, want 400", c.Code, c.Body)
	}
	if v := postJSONAuth(t, e.h, "/automations/validate", e.token, map[string]any{"spec": spec}); !strings.Contains(v.Body.String(), `"valid":false`) {
		t.Errorf("validate = %s, want invalid", v.Body)
	}
}

func TestDeployWatchTemplatePerSource(t *testing.T) {
	s := &Server{}
	proj := &project.Project{ID: "p1", Name: "shop {{ steps.x }}", RepoURL: "git@github.com:Acme/Shop.git"}
	gen := func(w project.DeployWatch) *automation.Spec {
		t.Helper()
		w.ID, w.BoardID, w.TypeID = "dw1", rbBoard, "release"
		src, err := s.deployWatchSpec(&w, proj, rbRelease)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := automation.ParseSpec(src)
		if err != nil {
			t.Fatalf("%s: %v\n%s", w.Source, err, src)
		}
		if err := automation.CheckFilterFields(&spec.Trigger); err != nil {
			t.Fatalf("%s filter: %v", w.Source, err)
		}
		if strings.Contains(spec.Name, "{{") {
			t.Errorf("project name opened a template: %q", spec.Name)
		}
		return spec
	}
	match := func(spec *automation.Spec, kind string, payload map[string]any) bool {
		t.Helper()
		ok, err := automation.FilterMatches(spec, kind, payload)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	byPath := gen(project.DeployWatch{Source: project.WatchGitHubWorkflow, Target: ".github/workflows/deploy.yml"})
	run := map[string]any{"repo": "acme/shop", "action": "completed", "conclusion": "success", "head_branch": "main",
		"default_branch": "main", "workflow_path": ".github/workflows/deploy.yml", "workflow_name": "Deploy"}
	if byPath.Trigger.Event != automation.EventWorkflowRun || !match(byPath, automation.EventWorkflowRun, run) {
		t.Errorf("workflow watch missed its run: %s", byPath.Trigger.Filter)
	}
	for field, value := range map[string]any{"repo": "acme/other", "conclusion": "cancelled", "head_branch": "feature",
		"workflow_path": ".github/workflows/ci.yml", "action": "requested"} {
		miss := map[string]any{}
		for k, v := range run {
			miss[k] = v
		}
		miss[field] = value
		if match(byPath, automation.EventWorkflowRun, miss) {
			t.Errorf("workflow watch fired with %s=%v", field, value)
		}
	}
	task := byPath.Steps[0].Task
	if len(byPath.Steps) != 1 || task == nil || task.Type != "release" || task.Stage != string(rbRelease) ||
		task.OnDuplicate != automation.DuplicateSkip || !strings.Contains(task.Fingerprint, "dw1") ||
		len(task.ProjectIDs) != 1 || byPath.Policy.Debounce == 0 {
		t.Errorf("workflow steps = %+v policy %+v", byPath.Steps, byPath.Policy)
	}

	byName := gen(project.DeployWatch{Source: project.WatchGitHubWorkflow, Target: "Deploy"})
	named := map[string]any{"repo": "Acme/Shop", "action": "completed", "conclusion": "failure", "head_branch": "main",
		"default_branch": "main", "workflow_path": ".github/workflows/x.yml", "workflow_name": "Deploy"}
	if !match(byName, automation.EventWorkflowRun, named) {
		t.Errorf("workflow watch by name missed: %s", byName.Trigger.Filter)
	}

	dep := gen(project.DeployWatch{Source: project.WatchGitHubDeployment, Target: "production"})
	status := map[string]any{"repo": "acme/shop", "environment": "production", "deploy_state": "error"}
	if dep.Trigger.Event != automation.EventDeployment || !match(dep, automation.EventDeployment, status) {
		t.Errorf("deployment watch missed: %s", dep.Trigger.Filter)
	}
	for _, miss := range []map[string]any{
		{"repo": "acme/shop", "environment": "staging", "deploy_state": "success"},
		{"repo": "acme/shop", "environment": "production", "deploy_state": "in_progress"},
	} {
		if match(dep, automation.EventDeployment, miss) {
			t.Errorf("deployment watch fired on %v", miss)
		}
	}

	cloud := gen(project.DeployWatch{Source: project.WatchCloud, Target: "env1", IntervalSeconds: 900})
	if cloud.Trigger.Schedule != "@every 15m" || len(cloud.Steps) != 2 || cloud.Steps[0].DeployWatch == nil ||
		cloud.Steps[0].DeployWatch.Watch != "dw1" || cloud.Steps[1].When != "steps.observe.changed" || cloud.Steps[1].Task == nil {
		t.Errorf("cloud spec = %+v", cloud)
	}
	if strings.Contains(cloud.Steps[1].Task.Title, "{{ steps.x") {
		t.Errorf("project name reached the title as a template: %q", cloud.Steps[1].Task.Title)
	}
	hourly := gen(project.DeployWatch{Source: project.WatchHTTP, Target: "https://shop.example.com/version", IntervalSeconds: 7200})
	if hourly.Trigger.Schedule != "@every 2h" {
		t.Errorf("http schedule = %q", hourly.Trigger.Schedule)
	}
}

// A polled watch opens one release task per change: the first reading is the baseline, the same
// reading again opens nothing, and a new version or a failure each opens one.
func TestPolledWatchOpensOneTaskPerChange(t *testing.T) {
	e := newWatchEnv(t)
	ctx := t.Context()
	var version atomic.Value
	version.Store("v1")
	var status atomic.Int32
	status.Store(http.StatusOK)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_ = json.NewEncoder(w).Encode(map[string]any{"version": version.Load()})
	}))
	t.Cleanup(site.Close)

	w := putJSON(t, e.h, "/tasks/"+e.release.ID+"/deploy-watches", map[string]any{"source": "http", "target": site.URL + "/version"}, e.token)
	if w.Code != http.StatusCreated {
		t.Fatalf("set = %d %s", w.Code, w.Body)
	}
	watch := decodeWatch(t, w)
	deps := e.srv.AutomationDeps()
	deps.Watches = &deployWatchObserver{s: e.srv, client: site.Client()}
	engine := automation.NewEngine(e.auto, deps)
	a, err := e.auto.Get(ctx, watch.AutomationID, automation.InWorkspace(e.ws))
	if err != nil {
		t.Fatal(err)
	}
	fire := func() {
		t.Helper()
		run, err := engine.Execute(ctx, a, automation.Firing{Trigger: automation.TriggerSchedule})
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != automation.RunSuccess {
			t.Fatalf("run = %s %s", run.Status, run.Error)
		}
	}
	releaseTasks := func() []task.Task {
		t.Helper()
		all, err := e.srv.taskStore.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []task.Task
		for _, tk := range all {
			if tk.WorkspaceID == e.ws && tk.ID != e.release.ID && tk.TypeID == "release" {
				out = append(out, tk)
			}
		}
		return out
	}

	fire() // baseline
	fire() // same reading
	if got := releaseTasks(); len(got) != 0 {
		t.Fatalf("baseline and a repeat opened %d tasks", len(got))
	}
	version.Store("v2")
	fire()
	fire()
	got := releaseTasks()
	if len(got) != 1 || got[0].Stage != types.Stage(rbRelease) || got[0].BoardID != rbBoard ||
		len(got[0].ProjectIDs) != 1 || got[0].ProjectIDs[0] != e.project || !strings.Contains(got[0].Title, "v2") {
		t.Fatalf("after a new version: %+v", got)
	}
	status.Store(http.StatusBadGateway)
	fire()
	if got := releaseTasks(); len(got) != 2 {
		t.Fatalf("a failure after success opened %d tasks in all, want 2", len(got))
	}
	stored, err := e.srv.projectStore.GetDeployWatch(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastState != project.WatchStateFailure || stored.LastVersion != "v2" || stored.LastChangedAt == nil {
		t.Errorf("stored observation = %+v", stored)
	}
}

func TestDeployWatchFollowsItsProjectOut(t *testing.T) {
	e := newWatchEnv(t)
	w := putJSON(t, e.h, "/tasks/"+e.release.ID+"/deploy-watches", map[string]any{"source": "github_deployment", "target": "production"}, e.token)
	if w.Code != http.StatusCreated {
		t.Fatalf("set = %d %s", w.Code, w.Body)
	}
	watch := decodeWatch(t, w)
	req := httptest.NewRequest(http.MethodDelete, "/projects/"+e.project, nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete project = %d %s", rec.Code, rec.Body)
	}
	if _, err := e.auto.Get(t.Context(), watch.AutomationID, automation.InWorkspace(e.ws)); err == nil {
		t.Error("the project went and its watch's automation stayed")
	}
}
