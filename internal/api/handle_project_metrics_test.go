package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

type projectMetricsResponse struct {
	ProjectID string `json:"project_id"`
	Flow      struct {
		TasksTotal     int `json:"tasks_total"`
		TasksOpen      int `json:"tasks_open"`
		TasksDone      int `json:"tasks_done"`
		TasksCompleted int `json:"tasks_completed"`
		AgentRuns      int `json:"agent_runs"`
		ByStage        []struct {
			Stage string `json:"stage"`
			Count int    `json:"count"`
		} `json:"by_stage"`
		Throughput []struct {
			Bucket string `json:"bucket"`
		} `json:"throughput"`
	} `json:"flow"`
	Agent struct {
		Sessions      int     `json:"sessions"`
		TasksWithRuns int     `json:"tasks_with_runs"`
		CostPerTask   float64 `json:"cost_per_task"`
	} `json:"agent"`
}

func createProjectForMetrics(t *testing.T, h http.Handler, token, name string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"name": name, "repo_url": testgit.BareRepo(t)})
	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /projects = %d, want 201: %s", w.Code, w.Body.String())
	}
	var proj map[string]any
	if err := json.NewDecoder(w.Body).Decode(&proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	id, _ := proj["id"].(string)
	if id == "" {
		t.Fatal("expected project id")
	}
	return id
}

func TestIntegration_ProjectMetrics(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "metrics@test.com", "M", "SecurePassword1")

	mineID := createProjectForMetrics(t, h, token, "Mine")
	otherID := createProjectForMetrics(t, h, token, "Other")

	mustCreate := func(id string, projects []string, stage task.Stage) {
		t.Helper()
		if err := srv.taskStore.Create(t.Context(), &task.Task{WorkspaceID: "ws-test",
			ID: id, ProjectIDs: projects, Title: id, Stage: stage,
		}); err != nil {
			t.Fatalf("create task %s: %v", id, err)
		}
	}
	mustCreate("t-open", []string{mineID}, "backlog")
	mustCreate("t-done", []string{mineID}, "done")
	mustCreate("t-other", []string{otherID}, "backlog")

	if err := srv.taskStore.RecordEvent(t.Context(), "t-open", task.EventAgentRunStarted, "", "octopus", "run", nil); err != nil {
		t.Fatalf("record event: %v", err)
	}

	w := getJSON(t, h, "/projects/"+mineID+"/metrics", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET metrics = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got projectMetricsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}

	if got.ProjectID != mineID {
		t.Errorf("project_id = %q, want %q", got.ProjectID, mineID)
	}
	if got.Flow.TasksTotal != 2 {
		t.Errorf("tasks_total = %d, want 2 (the other project's task must not leak)", got.Flow.TasksTotal)
	}
	if got.Flow.TasksOpen != 1 || got.Flow.TasksDone != 1 {
		t.Errorf("open %d done %d, want 1/1", got.Flow.TasksOpen, got.Flow.TasksDone)
	}
	if got.Flow.AgentRuns != 1 {
		t.Errorf("agent_runs = %d, want 1", got.Flow.AgentRuns)
	}
	if len(got.Flow.ByStage) != 2 {
		t.Errorf("by_stage has %d entries, want 2", len(got.Flow.ByStage))
	}
	if len(got.Flow.Throughput) < 28 {
		t.Errorf("throughput has %d buckets, want a zero-filled 30-day series", len(got.Flow.Throughput))
	}
	if got.Agent.Sessions != 0 || got.Agent.TasksWithRuns != 0 || got.Agent.CostPerTask != 0 {
		t.Errorf("agent rollup should be zero without sessions, got %+v", got.Agent)
	}
}

func TestIntegration_ProjectMetricsIgnoresQueryProjectID(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "metrics2@test.com", "M2", "SecurePassword1")

	mineID := createProjectForMetrics(t, h, token, "Scoped")
	if err := srv.taskStore.Create(t.Context(), &task.Task{WorkspaceID: "ws-test",
		ID: "t-scoped", ProjectIDs: []string{mineID}, Title: "scoped", Stage: "backlog",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	w := getJSON(t, h, "/projects/"+mineID+"/metrics?project_id=someone-else", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET metrics = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got projectMetricsResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if got.ProjectID != mineID {
		t.Errorf("project_id = %q, want %q — the path must win", got.ProjectID, mineID)
	}
	if got.Flow.TasksTotal != 1 {
		t.Errorf("tasks_total = %d, want 1", got.Flow.TasksTotal)
	}
}

func TestIntegration_ProjectMetricsUnknownProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "metrics3@test.com", "M3", "SecurePassword1")

	w := getJSON(t, h, "/projects/does-not-exist/metrics", token)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET metrics for unknown project = %d, want 404", w.Code)
	}
}
