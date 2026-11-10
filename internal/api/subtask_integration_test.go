package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func createSubtaskViaAPI(t *testing.T, h http.Handler, token, parentID string, body map[string]any) map[string]any {
	t.Helper()
	w := postJSONAuth(t, h, "/tasks/"+parentID+"/subtasks", token, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create subtask: expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode subtask: %v", err)
	}
	return resp
}

func TestIntegration_CreateSubtask_InheritsAndOverrides(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sub-create@test.com", "Sub", "SecurePassword1")

	pw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":             "feature: checkout",
		"stage_assignments": map[string]string{"in_progress": "developer", "testing": "developer"},
	})
	if pw.Code != http.StatusCreated {
		t.Fatalf("create parent: %d %s", pw.Code, pw.Body.String())
	}
	var parent map[string]any
	json.NewDecoder(pw.Body).Decode(&parent)
	parentID := parent["id"].(string)

	sub := createSubtaskViaAPI(t, h, token, parentID, map[string]any{
		"title":             "UI testing slice",
		"stage_assignments": map[string]string{"testing": "ui-tester"},
	})

	if sub["parent_id"] != parentID {
		t.Errorf("subtask parent_id = %v want %s", sub["parent_id"], parentID)
	}
	assigns, _ := sub["stage_assignments"].(map[string]any)
	if assigns["in_progress"] != "developer" {
		t.Errorf("subtask did not inherit in_progress=developer: %v", assigns)
	}
	if assigns["testing"] != "ui-tester" {
		t.Errorf("subtask did not override testing=ui-tester: %v", assigns)
	}
}

func TestIntegration_CreateSubtask_RejectsGrandchild(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sub-grand@test.com", "Sub", "SecurePassword1")
	parentID := createTaskViaAPI(t, h, token, "feature")
	sub := createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": "child"})
	subID := sub["id"].(string)

	w := postJSONAuth(t, h, "/tasks/"+subID+"/subtasks", token, map[string]any{"title": "grandchild"})
	if w.Code != http.StatusConflict {
		t.Errorf("grandchild subtask = %d want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestIntegration_Subtasks_BoardFilteringAndChildren(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sub-board@test.com", "Sub", "SecurePassword1")
	parentID := createTaskViaAPI(t, h, token, "feature")
	sub := createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": "dev slice"})
	subID := sub["id"].(string)

	lw := getJSON(t, h, "/tasks", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("list tasks: %d", lw.Code)
	}
	var board []map[string]any
	json.NewDecoder(lw.Body).Decode(&board)
	for _, tk := range board {
		if tk["id"] == subID {
			t.Error("board list leaked a subtask as a top-level card")
		}
	}

	cw := getJSON(t, h, "/tasks?parent_id="+parentID, token)
	var children []map[string]any
	json.NewDecoder(cw.Body).Decode(&children)
	if len(children) != 1 || children[0]["id"] != subID {
		t.Errorf("parent_id list = %v want [%s]", children, subID)
	}

	gw := getJSON(t, h, "/tasks/"+parentID, token)
	var full map[string]any
	json.NewDecoder(gw.Body).Decode(&full)
	embedded, _ := full["children"].([]any)
	if len(embedded) != 1 {
		t.Errorf("GET parent children = %v want 1", full["children"])
	}
}

func TestIntegration_DeleteParentWithChildren(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sub-del@test.com", "Sub", "SecurePassword1")
	parentID := createTaskViaAPI(t, h, token, "feature")
	createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": "child"})

	r := httptest.NewRequest(http.MethodDelete, "/tasks/"+parentID, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("delete parent with children = %d want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestResolveTaskWorkspace_SubtaskUsesParent(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	parent := &task.Task{WorkspaceID: "ws-test", Title: "parent"}
	if err := srv.taskStore.Create(t.Context(), parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	sub := &task.Task{WorkspaceID: "ws-test", Title: "child", ParentID: parent.ID}
	if err := srv.taskStore.Create(t.Context(), sub); err != nil {
		t.Fatalf("create subtask: %v", err)
	}

	owner, _, _, _, err := srv.resolveTaskWorkspace(t.Context(), sub.ID)
	if err != nil {
		t.Fatalf("resolveTaskWorkspace: %v", err)
	}
	if owner != parent.ID {
		t.Errorf("subtask sandbox owner = %q want parent %q", owner, parent.ID)
	}
}

func TestIntegration_DeleteParentWithSubtasksOnRequest(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "sub-del-all@test.com", "Sub", "SecurePassword1")
	parentID := createTaskViaAPI(t, h, token, "feature")
	sub := createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": "child"})
	subID := sub["id"].(string)

	w := deleteRequest(t, h, "/tasks/"+parentID+"?subtasks=delete", token)
	if w.Code != http.StatusOK {
		t.Fatalf("delete parent with ?subtasks=delete = %d want 200; body=%s", w.Code, w.Body.String())
	}
	for _, id := range []string{parentID, subID} {
		if g := getJSON(t, h, "/tasks/"+id, token); g.Code != http.StatusNotFound {
			t.Errorf("GET /tasks/%s after the delete = %d want 404", id, g.Code)
		}
	}
}

func TestIntegration_ArchiveParentTakesSubtasksOffTheBoard(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "archive@test.com", "Arch", "SecurePassword1")
	parentID := createTaskViaAPI(t, h, token, "feature")
	sub := createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": "child"})
	subID := sub["id"].(string)

	listed := func(path string) map[string]bool {
		t.Helper()
		w := getJSON(t, h, path, token)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}
		var rows []map[string]any
		json.NewDecoder(w.Body).Decode(&rows)
		out := map[string]bool{}
		for _, r := range rows {
			out[r["id"].(string)] = true
		}
		return out
	}

	w := patchJSONAuth(t, h, "/tasks/"+parentID, token, map[string]any{"archived": true})
	if w.Code != http.StatusOK {
		t.Fatalf("archive = %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	json.NewDecoder(w.Body).Decode(&got)
	if got["archived_at"] == nil {
		t.Errorf("the archived task carries no archived_at: %v", got)
	}
	if listed("/tasks")[parentID] {
		t.Error("the board still lists the archived task")
	}
	if !listed("/tasks?archived=true")[parentID] {
		t.Error("?archived=true does not list the archived task")
	}
	if !listed("/tasks?archived=true&include_subtasks=true")[subID] {
		t.Error("the subtask was not archived with its parent")
	}

	w = patchJSONAuth(t, h, "/tasks/"+parentID, token, map[string]any{"archived": false})
	if w.Code != http.StatusOK {
		t.Fatalf("unarchive = %d: %s", w.Code, w.Body.String())
	}
	if !listed("/tasks")[parentID] {
		t.Error("the restored task is not back on the board")
	}
	if listed("/tasks?archived=true&include_subtasks=true")[subID] {
		t.Error("the subtask stayed archived after its parent came back")
	}
}

func TestIntegration_ArchiveSubtaskAloneRefused(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "archive-sub@test.com", "Arch", "SecurePassword1")
	parentID := createTaskViaAPI(t, h, token, "feature")
	sub := createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": "child"})
	subID := sub["id"].(string)

	for _, archived := range []bool{true, false} {
		w := patchJSONAuth(t, h, "/tasks/"+subID, token, map[string]any{"archived": archived})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("PATCH subtask {archived:%v} = %d, want 400: %s", archived, w.Code, w.Body.String())
		}
		var refusal struct {
			Code string `json:"code"`
		}
		json.NewDecoder(w.Body).Decode(&refusal)
		if refusal.Code != "api.tasks.errors.archive_subtask" {
			t.Errorf("refusal code = %q, want api.tasks.errors.archive_subtask", refusal.Code)
		}
	}
	w := getJSON(t, h, "/tasks/"+subID, token)
	var got map[string]any
	json.NewDecoder(w.Body).Decode(&got)
	if got["archived_at"] != nil {
		t.Errorf("the refused patch archived the subtask anyway: %v", got["archived_at"])
	}
}
