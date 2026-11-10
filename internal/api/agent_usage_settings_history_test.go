package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestAgentUsageStaysInTheWorkspace(t *testing.T) {
	db := testpg.New(t)
	dir := t.TempDir()
	tasks, err := task.NewStore(db, filepath.Join(dir, "att"))
	if err != nil {
		t.Fatalf("task store: %v", err)
	}
	projects, err := project.NewStore(db, filepath.Join(dir, "projects"))
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	srv := &Server{taskStore: tasks, projectStore: projects}
	ctx := t.Context()

	for _, tk := range []*task.Task{
		{WorkspaceID: "ws-a", Title: "one", StageAssignments: map[types.Stage]string{types.StageInProgress: "backend"}},
		{WorkspaceID: "ws-b", Title: "two", StageAssignments: map[types.Stage]string{types.StageInProgress: "backend"}},
	} {
		if err := tasks.Create(ctx, tk); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}
	for _, p := range []struct{ id, wid, tpl string }{
		{"p-a", "ws-a", `{"review":"backend","in_progress":"backend","refinement":"refiner"}`},
		{"p-a2", "ws-a", `{"review":"reviewer"}`},
		{"p-b", "ws-b", `{"review":"backend"}`},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, path, repo_url, stage_template, workspace_id, created_at)
			VALUES ($1, $1, $1, 'https://example.test/r.git', $2, $3, $4)`, p.id, p.tpl, p.wid, time.Now()); err != nil {
			t.Fatalf("insert project: %v", err)
		}
	}

	r := tenantRequestBody(http.MethodGet, "/types/backend/usage", "ws-a", "")
	r.SetPathValue("name", "backend")
	w := httptest.NewRecorder()
	srv.handleAgentUsage(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", w.Code, w.Body.String())
	}
	var got agentUsage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Tasks != 1 {
		t.Errorf("tasks = %d, want 1 (ws-b's task is not ws-a's)", got.Tasks)
	}
	if len(got.Projects) != 1 || got.Projects[0].ID != "p-a" ||
		len(got.Projects[0].Stages) != 2 || got.Projects[0].Stages[0] != "in_progress" {
		t.Errorf("projects = %+v, want p-a with in_progress and review", got.Projects)
	}
}

func TestSavingSettingsRecordsARevision(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards()}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")
	const ws = "ws-history"

	created := httptest.NewRecorder()
	srv.handleCreateAgent(created, tenantRequestBody(http.MethodPost, "/types", ws, `{"name":"developer"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", created.Code, created.Body.String())
	}
	save := func(s AgentSettings) {
		t.Helper()
		body, _ := json.Marshal(s)
		r := tenantRequestBody(http.MethodPut, "/types/developer/settings", ws, string(body))
		r.SetPathValue("name", "developer")
		w := httptest.NewRecorder()
		srv.handlePutAgentSettings(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT settings = %d: %s", w.Code, w.Body.String())
		}
	}
	first := AgentSettings{Name: "developer", ValidStages: []string{"in_progress"}}
	save(first)
	save(first)
	second := AgentSettings{Name: "developer", ValidStages: []string{"in_progress", "review"},
		MCPByStage: map[string][]string{"review": {"thulu-github@read"}}}
	save(second)

	revs, err := store.ListRevisions(t.Context(), ws, agentstore.KindAgent, "developer", agentstore.SettingsPath, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("revisions = %d, want 3: %+v", len(revs), revs)
	}
	body, err := store.RevisionBody(t.Context(), ws, revs[0].ID)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	var latest AgentSettings
	if err := json.Unmarshal(body, &latest); err != nil {
		t.Fatalf("decode revision: %v (%s)", err, body)
	}
	if len(latest.ValidStages) != 2 || latest.MCPByStage["review"][0] != "thulu-github@read" {
		t.Fatalf("latest revision = %+v", latest)
	}
}

func TestCreatingAnAgentWithItsShapeLeavesNoHistory(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards()}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")
	const ws = "ws-create-shape"
	ctx := t.Context()

	create := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		srv.handleCreateAgent(w, tenantRequestBody(http.MethodPost, "/types", ws, body))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	create(`{"name":"dev-bot","description":"Writes code","valid_stages":["in_progress","review","bad stage"],"skills":["git-flow"]}`)

	got, err := store.GetAgent(ctx, ws, "dev-bot")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Description != "Writes code" || len(got.ValidStages) != 2 ||
		got.SkillsByStage["in_progress"][0] != "git-flow" || got.SkillsByStage["review"][0] != "git-flow" {
		t.Fatalf("created = %+v, want the dialog's description, two stages and git-flow on both", got.AgentType)
	}
	if revs, err := store.ListDefRevisions(ctx, ws, "dev-bot"); err != nil || len(revs) != 0 {
		t.Fatalf("definition history = %+v (%v), want empty", revs, err)
	}

	create(`{"name":"dev-copy","template":"dev-bot","skills":["lint","git-flow"]}`)
	clone, err := store.GetAgent(ctx, ws, "dev-copy")
	if err != nil {
		t.Fatalf("get clone: %v", err)
	}
	if want := []string{"git-flow", "lint"}; len(clone.ValidStages) != 2 ||
		!slices.Equal(clone.SkillsByStage["review"], want) {
		t.Fatalf("clone = %+v, want the source's stages with %v", clone.AgentType, want)
	}
	if clone.Description != "Writes code" {
		t.Errorf("clone description = %q, want the source's", clone.Description)
	}
	if revs, err := store.ListDefRevisions(ctx, ws, "dev-copy"); err != nil || len(revs) != 0 {
		t.Fatalf("clone history = %+v (%v), want empty", revs, err)
	}
}
