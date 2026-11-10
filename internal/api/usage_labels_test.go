package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

func TestIntegration_UsageReportsNameTasksAndProjects(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "usage-names@test.com", "UsageNames")
	projectID := createProject(t, h, token, "tienda-web")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "Fix the checkout"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID, _ := created["id"].(string)

	now := time.Now().UTC().Add(-time.Hour)
	if err := srv.usageStore.Add(t.Context(), usage.Session{
		ID: "named-run", WorkspaceID: wid, Agent: "dev", AgentType: "dev",
		Engine: "claude-code", Model: "sonnet", TaskID: taskID, ProjectID: projectID,
		StartedAt: now.Add(-time.Minute), FinishedAt: now,
		Turns: 2, InputTokens: 10, OutputTokens: 5, CostUSD: 0.02,
	}); err != nil {
		t.Fatalf("add session: %v", err)
	}

	var tasks []usage.TaskRow
	w := getJSON(t, h, "/usage/top-tasks", token)
	if w.Code != http.StatusOK || json.NewDecoder(w.Body).Decode(&tasks) != nil || len(tasks) != 1 {
		t.Fatalf("GET /usage/top-tasks = %d: %+v", w.Code, tasks)
	}
	if tasks[0].TaskID != taskID || tasks[0].Title != "Fix the checkout" {
		t.Errorf("task row = %+v, want its id and title", tasks[0])
	}

	var projects []usage.DimensionRow
	w = getJSON(t, h, "/usage/by-project", token)
	if w.Code != http.StatusOK || json.NewDecoder(w.Body).Decode(&projects) != nil || len(projects) != 1 {
		t.Fatalf("GET /usage/by-project = %d: %+v", w.Code, projects)
	}
	if projects[0].Key != projectID || projects[0].Label != "tienda-web" {
		t.Errorf("project row = %+v, want its id and name", projects[0])
	}
}

func TestIntegration_UsageAllTimeAndTaskCost(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "usage-cost@test.com", "UsageCost")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "Costosa"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID, _ := created["id"].(string)
	now := time.Now().UTC().Add(-time.Hour)
	if err := srv.usageStore.Add(t.Context(), usage.Session{
		ID: "cost-run", WorkspaceID: wid, Agent: "dev", AgentType: "dev",
		Engine: "claude-code", Model: "sonnet", TaskID: taskID,
		StartedAt: now.Add(-time.Minute), FinishedAt: now,
		Turns: 2, InputTokens: 10, OutputTokens: 5, CostUSD: 0.75,
	}); err != nil {
		t.Fatalf("add session: %v", err)
	}

	var u struct {
		AllTime usage.AgentSummary `json:"all_time"`
	}
	w := getJSON(t, h, "/usage", token)
	if w.Code != http.StatusOK || json.NewDecoder(w.Body).Decode(&u) != nil {
		t.Fatalf("GET /usage = %d: %s", w.Code, w.Body.String())
	}
	if u.AllTime.Sessions != 1 || u.AllTime.TotalCostUSD != 0.75 {
		t.Errorf("all_time = %+v, want 1 session and $0.75", u.AllTime)
	}

	if err := srv.taskStore.RecordEvent(t.Context(), taskID, task.EventAgentRunStarted, "inst-1", "dev", "started",
		map[string]any{"container_id": "cost-run"}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	var tu map[string]any
	w = getJSON(t, h, "/tasks/"+taskID+"/usage", token)
	if w.Code != http.StatusOK || json.NewDecoder(w.Body).Decode(&tu) != nil {
		t.Fatalf("GET /tasks/{id}/usage = %d: %s", w.Code, w.Body.String())
	}
	if tu["cost_usd"] != 0.75 {
		t.Errorf("task usage = %v, want cost_usd 0.75", tu)
	}
}
