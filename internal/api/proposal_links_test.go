package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func flagIDFor(t *testing.T, h http.Handler, token, taskID string) string {
	t.Helper()
	w := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", token, map[string]any{
		"kind": "infra_impact", "label": "review me",
	})
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("create flag = %d: %s", w.Code, w.Body.String())
	}
	var f map[string]any
	_ = json.NewDecoder(w.Body).Decode(&f)
	id, _ := f["id"].(string)
	if id == "" {
		t.Fatalf("create flag returned no id: %v", f)
	}
	return id
}

func TestProposals_RefuseAnotherWorkspacesTaskOrFlag(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA := registerUser(t, h, "proposal-a@test.com", "PA", "SecurePassword1")
	tokenB := registerUser(t, h, "proposal-b@test.com", "PB", "SecurePassword1")

	projA := createProjectForMetrics(t, h, tokenA, "ProposalsA")
	envID := decodeEnv(t, sendJSON(t, h, http.MethodPost, "/projects/"+projA+"/environments", tokenA,
		map[string]any{"name": "prod", "provider": "aws"}))["id"].(string)
	base := "/projects/" + projA + "/environments/" + envID

	taskA := taskIDFor(t, h, tokenA, "A's task")
	flagA := flagIDFor(t, h, tokenA, taskA)
	taskB := taskIDFor(t, h, tokenB, "B's task")
	flagB := flagIDFor(t, h, tokenB, taskB)

	mappings := []map[string]any{{"service": "api", "signal": "cpu",
		"source": map[string]any{"namespace": "AWS/Lambda", "metric_name": "Duration"}}}
	propose := func(taskID, flagID string) int {
		return sendJSON(t, h, http.MethodPost, base+"/mappings", tokenA, map[string]any{
			"task_id": taskID, "flag_id": flagID, "mappings": mappings,
		}).Code
	}
	dashboard := func(taskID, flagID string) int {
		return sendJSON(t, h, http.MethodPost, base+"/dashboards", tokenA, map[string]any{
			"title": "d", "task_id": taskID, "flag_id": flagID,
		}).Code
	}

	for name, c := range map[string]struct {
		taskID, flagID string
		want           int
	}{
		"another tenant's task":           {taskB, "", http.StatusNotFound},
		"another tenant's flag":           {taskA, flagB, http.StatusNotFound},
		"another tenant's task and flag":  {taskB, flagB, http.StatusNotFound},
		"a flag with no task":             {"", flagB, http.StatusBadRequest},
		"a flag of another task, same ws": {taskIDFor(t, h, tokenA, "A's other task"), flagA, http.StatusNotFound},
	} {
		if got := propose(c.taskID, c.flagID); got != c.want {
			t.Errorf("mappings naming %s = %d, want %d", name, got, c.want)
		}
		if got := dashboard(c.taskID, c.flagID); got != c.want {
			t.Errorf("dashboard naming %s = %d, want %d", name, got, c.want)
		}
	}

	bFlags, err := srv.taskStore.ListFlags(t.Context(), taskB)
	if err != nil || len(bFlags) != 1 {
		t.Fatalf("B's task has %d flags (%v), want only its own", len(bFlags), err)
	}

	if got := propose(taskA, flagA); got != http.StatusCreated {
		t.Fatalf("mappings naming A's own task and flag = %d, want 201", got)
	}
	if w := sendJSON(t, h, http.MethodPost, base+"/mappings/approve", tokenA, nil); w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	f, err := srv.taskStore.GetFlag(t.Context(), flagA)
	if err != nil || f.Status != task.FlagApproved {
		t.Errorf("A's flag after approving its proposal = %+v (%v), want approved", f, err)
	}
	if f, _ := srv.taskStore.GetFlag(t.Context(), flagB); f.Status != task.FlagPending {
		t.Errorf("B's flag = %s, want still pending", f.Status)
	}
}

func TestRemoveDependency_WritesNothingIntoAnotherWorkspacesTask(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA := registerUser(t, h, "dep-a@test.com", "DA", "SecurePassword1")
	tokenB := registerUser(t, h, "dep-b@test.com", "DB", "SecurePassword1")
	taskA := taskIDFor(t, h, tokenA, "A")
	taskA2 := taskIDFor(t, h, tokenA, "A2")
	taskB := taskIDFor(t, h, tokenB, "B")

	before, _ := srv.taskStore.ListEvents(t.Context(), taskB)
	w := sendJSON(t, h, http.MethodDelete, "/tasks/"+taskA+"/dependencies/"+taskB, tokenA, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("remove dependency = %d: %s", w.Code, w.Body.String())
	}
	after, _ := srv.taskStore.ListEvents(t.Context(), taskB)
	if len(after) != len(before) {
		t.Errorf("B's timeline went from %d to %d events after A removed a dependency on it", len(before), len(after))
	}

	if w := postJSONAuth(t, h, "/tasks/"+taskA+"/dependencies", tokenA,
		map[string]any{"depends_on_task_id": taskA2}); w.Code != http.StatusCreated {
		t.Fatalf("add dependency = %d: %s", w.Code, w.Body.String())
	}
	before, _ = srv.taskStore.ListEvents(t.Context(), taskA2)
	if w := sendJSON(t, h, http.MethodDelete, "/tasks/"+taskA+"/dependencies/"+taskA2, tokenA, nil); w.Code != http.StatusOK {
		t.Fatalf("remove own dependency = %d: %s", w.Code, w.Body.String())
	}
	after, _ = srv.taskStore.ListEvents(t.Context(), taskA2)
	if len(after) != len(before)+1 {
		t.Errorf("A2's timeline gained %d events, want the dep_removed mirror", len(after)-len(before))
	}
}
