package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestIntegration_RunKeyProposesThroughItsTask(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, ws := mintWorkspaceUser(t, srv, "proposals-run@test.com", "Admin")
	projID := createProjectForMetrics(t, h, admin, "ProposalsRun")
	other := createProjectForMetrics(t, h, admin, "ProposalsOther")
	envID := decodeEnv(t, sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", admin,
		map[string]any{"name": "prod", "provider": "aws"}))["id"].(string)
	otherEnv := decodeEnv(t, sendJSON(t, h, http.MethodPost, "/projects/"+other+"/environments", admin,
		map[string]any{"name": "prod", "provider": "aws"}))["id"].(string)
	taskID := createTaskWithProjects(t, h, admin, "map observability", projID)
	runKey, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: taskID, Agent: "tasker", Stage: "in_progress"}, ws, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mappings := map[string]any{"mappings": []map[string]any{
		{"service": "api", "signal": "latency_p95",
			"source": map[string]any{"namespace": "AWS/Lambda", "metric_name": "Duration"}},
	}}
	projBase := "/projects/" + projID + "/environments/" + envID
	taskBase := "/tasks/" + taskID + "/environments/"

	if w := keyDo(t, h, http.MethodPost, projBase+"/mappings", runKey, mappings); w.Code != http.StatusForbidden {
		t.Fatalf("run key on the project route = %d, want 403", w.Code)
	}
	if w := keyDo(t, h, http.MethodPost, taskBase+envID+"/mapping-proposals", runKey, mappings); w.Code != http.StatusCreated {
		t.Fatalf("run key on the task route = %d, want 201: %s", w.Code, w.Body.String())
	}
	var flags []map[string]any
	decodeInto(t, getJSON(t, h, "/tasks/"+taskID+"/flags", admin).Body.Bytes(), &flags)
	if len(flags) != 1 || flags[0]["status"] != "pending" {
		t.Fatalf("flags after the run's proposal = %+v, want one pending review flag", flags)
	}

	dashboard := map[string]any{"name": "API", "panels": []map[string]any{}}
	if w := keyDo(t, h, http.MethodPost, taskBase+envID+"/dashboard-proposals", runKey, dashboard); w.Code == http.StatusForbidden || w.Code == http.StatusNotFound {
		t.Fatalf("run key on the dashboard task route = %d, want it past the permission gate: %s", w.Code, w.Body.String())
	}

	if w := keyDo(t, h, http.MethodPost, taskBase+otherEnv+"/mapping-proposals", runKey, mappings); w.Code != http.StatusNotFound {
		t.Fatalf("run key on another project's environment = %d, want 404", w.Code)
	}
	if w := keyDo(t, h, http.MethodPost, projBase+"/mappings/approve", runKey, nil); w.Code != http.StatusForbidden {
		t.Fatalf("run key approving = %d, want 403", w.Code)
	}
}
