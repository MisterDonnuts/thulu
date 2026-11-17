package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

func TestCleanInventory(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	got := cleanInventory(runner.Inventory{
		Roots: []string{"/Users/a/code/", "relative", "/Users/a/code", " "},
		Repos: []runner.InventoryRepo{
			{Path: "/Users/a/code/zeta", Name: "zeta"},
			{Path: "/Users/a/code/shop/", Name: " shop ", Branch: "main"},
			{Path: "/Users/a/code/shop", Name: "dup"},
			{Path: "/etc/elsewhere", Name: "outside"},
			{Path: "/Users/a/code/../secrets", Name: "climb"},
			{Path: "rel/repo", Name: "rel"},
		},
		Mapped:    map[string]string{"p1": "/Users/a/code/shop/", "p2": "rel", " ": "/x"},
		ScannedAt: time.Unix(0, 0),
		Engines: []runner.EngineState{
			{Engine: engine.DefaultName, Installed: true, LoggedIn: true, Email: " ada@example.com "},
			{Engine: engine.DefaultName, Installed: false},
			{Engine: "codex", Installed: true},
			{Engine: "", Installed: true},
		},
	}, now)
	if len(got.Engines) != 1 || !got.Engines[0].LoggedIn || got.Engines[0].Email != "ada@example.com" {
		t.Errorf("engines = %+v, want claude-code once, trimmed", got.Engines)
	}
	if len(got.Roots) != 1 || got.Roots[0] != "/Users/a/code" {
		t.Errorf("roots = %v", got.Roots)
	}
	if len(got.Repos) != 2 || got.Repos[0].Path != "/Users/a/code/shop" || got.Repos[1].Path != "/Users/a/code/zeta" {
		t.Fatalf("repos = %+v, want shop and zeta, sorted, deduped, inside the root", got.Repos)
	}
	if got.Repos[0].Name != "shop" || got.Repos[0].Branch != "main" {
		t.Errorf("shop = %+v", got.Repos[0])
	}
	if len(got.Mapped) != 1 || got.Mapped["p1"] != "/Users/a/code/shop" {
		t.Errorf("mapped = %v", got.Mapped)
	}
	if !got.ScannedAt.Equal(now) {
		t.Errorf("ScannedAt = %v, want the daemon's clock", got.ScannedAt)
	}
}

func TestCheckRunnerFolder(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{projectStore: f.projects, runnerStore: f.store}
	ctx := t.Context()
	rn, err := f.store.Get(ctx, "ws1", f.reg.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.checkRunnerFolder(ctx, rn, "/Users/a/code/shop", ""); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Fatalf("before any report: err = %v, want precondition", err)
	}
	if err := f.store.SaveInventory(ctx, rn.ID, runner.Inventory{Roots: []string{"/Users/a/code"},
		Repos:     []runner.InventoryRepo{{Path: "/Users/a/code/shop", Name: "shop", Branch: "main"}},
		ScannedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	clean, repo, err := srv.checkRunnerFolder(ctx, rn, "/Users/a/code/shop/", "")
	if err != nil || clean != "/Users/a/code/shop" || repo == nil || repo.Branch != "main" {
		t.Fatalf("a reported repo = %q, %+v, %v", clean, repo, err)
	}
	if clean, repo, err := srv.checkRunnerFolder(ctx, rn, "/Users/a/code/new", ""); err != nil || clean == "" || repo != nil {
		t.Errorf("a typed folder inside the root = %q, %+v, %v", clean, repo, err)
	}
	for _, bad := range []string{"code/shop", "/Users/a/other", "/Users/a/code/../secrets"} {
		if _, _, err := srv.checkRunnerFolder(ctx, rn, bad, ""); apperr.KindOf(err) != apperr.KindInvalid {
			t.Errorf("%q: err = %v, want invalid", bad, err)
		}
	}
	taken := "/Users/a/code/shop"
	if _, err := f.projects.Update(ctx, f.project.ID, projectPatchPath(taken)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.checkRunnerFolder(ctx, rn, taken, ""); apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("another project's folder: err = %v, want conflict", err)
	}
	if _, _, err := srv.checkRunnerFolder(ctx, rn, taken, f.project.ID); err != nil {
		t.Errorf("the project's own folder: err = %v", err)
	}
}

func TestRunnerReposMarksUsedFolders(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{projectStore: f.projects, runnerStore: f.store, runnerRoute: &routedLauncher{runner: f.launcher}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runners/{id}/repos", srv.handleRunnerRepos)
	get := func() runnerReposView {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, tenantRequestBody(http.MethodGet, "/runners/"+f.reg.RunnerID+"/repos", "ws1", ""))
		if w.Code != http.StatusOK {
			t.Fatalf("repos: %d %s", w.Code, w.Body.String())
		}
		var v runnerReposView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := get(); v.Reported || v.Repos == nil {
		t.Fatalf("unreported = %+v", v)
	}
	ctx := t.Context()
	if err := f.store.SaveInventory(ctx, f.reg.RunnerID, runner.Inventory{Roots: []string{"/c"},
		Repos: []runner.InventoryRepo{{Path: "/c/a", Name: "a"}, {Path: "/c/b", Name: "b"}}, ScannedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.projects.Update(ctx, f.project.ID, projectPatchPath("/c/b")); err != nil {
		t.Fatal(err)
	}
	v := get()
	if !v.Reported || v.ScannedAt == nil || len(v.Repos) != 2 {
		t.Fatalf("reported = %+v", v)
	}
	if v.Repos[0].ProjectID != "" || v.Repos[1].ProjectID != f.project.ID || v.Repos[1].ProjectName != f.project.Name {
		t.Errorf("used marks = %+v", v.Repos)
	}
}

func TestInventoryAdoptsMappedFoldersOnce(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{projectStore: f.projects, runnerStore: f.store}
	ctx := t.Context()
	rn, err := f.store.Get(ctx, "ws1", f.reg.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	srv.adoptMappedFolders(ctx, rn, map[string]string{f.project.ID: "/c/app", "unknown": "/c/x"})
	p, err := f.projects.Get(ctx, f.project.ID)
	if err != nil || p.RunnerPath != "/c/app" {
		t.Fatalf("after adopt = %q, %v", p.RunnerPath, err)
	}
	srv.adoptMappedFolders(ctx, rn, map[string]string{f.project.ID: "/c/moved"})
	if p, _ = f.projects.Get(ctx, f.project.ID); p.RunnerPath != "/c/app" {
		t.Errorf("a second report overwrote the folder set in the dashboard: %q", p.RunnerPath)
	}
}

func projectPatchPath(path string) project.Patch { return project.Patch{RunnerPath: &path} }

func TestCreateRunnerProjectFromAFolder(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{projectStore: f.projects, runnerStore: f.store, runnerRoute: &routedLauncher{runner: f.launcher}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runners/{id}/projects", srv.handleCreateRunnerProject)
	post := func(body string) *httptest.ResponseRecorder {
		r := tenantRequestBody(http.MethodPost, "/runners/"+f.reg.RunnerID+"/projects", "ws1", body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"path":"/c/shop"}`); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("before the runner reported: %d %s", w.Code, w.Body.String())
	}
	if err := f.store.SaveInventory(t.Context(), f.reg.RunnerID, runner.Inventory{Roots: []string{"/c"},
		Repos:     []runner.InventoryRepo{{Path: "/c/shop", Name: "shop", Branch: "trunk", Origin: "github.com/acme/shop"}},
		ScannedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	w := post(`{"path":"/c/shop/"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var p project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "shop" || p.RunnerPath != "/c/shop" || p.DefaultBranch != "trunk" || p.RepoURL != "github.com/acme/shop" || p.RunnerID != f.reg.RunnerID {
		t.Errorf("created = %+v", p)
	}
	if w := post(`{"name":"again","path":"/c/shop"}`); w.Code != http.StatusConflict {
		t.Errorf("the same folder twice: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"path":"/elsewhere/repo"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a folder outside the allowed ones: %d %s", w.Code, w.Body.String())
	}
}

func TestAllProjectsOnRunners(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{projectStore: f.projects}
	ctx := t.Context()
	if !srv.allProjectsOnRunners(ctx, "ws1") {
		t.Error("a workspace whose only project is on a runner still needs a model key")
	}
	if srv.allProjectsOnRunners(ctx, "empty") {
		t.Error("a workspace without projects skipped the model key")
	}
	gitProjectOn(t, f, "ws1", "web", "")
	if srv.allProjectsOnRunners(ctx, "ws1") {
		t.Error("a project in Thulu did not bring the model key back")
	}
}

func TestRunnerAddsAProjectFromTheMachine(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{projectStore: f.projects, runnerStore: f.store, runnerRoute: &routedLauncher{runner: f.launcher}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /runner/v1/projects", srv.runnerAuth(srv.handleRunnerAddProject))
	post := func(credential, body string) (*httptest.ResponseRecorder, runner.AddProjectResponse) {
		r := httptest.NewRequest(http.MethodPost, "/runner/v1/projects", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(runner.ProtocolHeader, strconv.Itoa(runner.ProtocolVersion))
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out runner.AddProjectResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	cred := f.reg.Credential
	if w, _ := post("wrong", `{"path":"/c/shop"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("a bad credential: %d %s", w.Code, w.Body.String())
	}
	if w, _ := post(cred, `{"path":"/c/shop"}`); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("before the runner reported: %d %s", w.Code, w.Body.String())
	}
	if err := f.store.SaveInventory(t.Context(), f.reg.RunnerID, runner.Inventory{Roots: []string{"/c"},
		Repos: []runner.InventoryRepo{
			{Path: "/c/shop", Name: "shop", Branch: "trunk", Origin: "github.com/acme/shop"},
			{Path: "/c/app", Name: "app"},
		},
		ScannedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	w, first := post(cred, `{"path":"/c/shop"}`)
	if w.Code != http.StatusCreated || !first.Created || first.ProjectName != "shop" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	p, err := f.projects.Get(t.Context(), first.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if p.RunnerID != f.reg.RunnerID || p.RunnerPath != "/c/shop" || p.DefaultBranch != "trunk" || p.WorkspaceID != "ws1" {
		t.Errorf("created = %+v", p)
	}
	if w, again := post(cred, `{"path":"/c/shop/"}`); w.Code != http.StatusOK || again.Created || again.ProjectID != first.ProjectID {
		t.Errorf("the same folder twice should answer with its project: %d %s", w.Code, w.Body.String())
	}
	// The fixture already has a project called app, on no folder.
	if w, _ := post(cred, `{"path":"/c/app"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "-name") {
		t.Errorf("a taken name should point at -name: %d %s", w.Code, w.Body.String())
	}
	if w, named := post(cred, `{"path":"/c/app","name":"app-web"}`); w.Code != http.StatusCreated || named.ProjectName != "app-web" {
		t.Errorf("with -name: %d %s", w.Code, w.Body.String())
	}
	if w, _ := post(cred, `{"path":"/elsewhere/repo"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a folder outside the allowed ones: %d %s", w.Code, w.Body.String())
	}
}
