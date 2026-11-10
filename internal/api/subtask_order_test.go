package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func childOrder(t *testing.T, h http.Handler, token, parentID string) []string {
	t.Helper()
	w := getJSON(t, h, "/tasks/"+parentID, token)
	var full struct {
		Children []struct {
			ID string `json:"id"`
		} `json:"children"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &full) != nil {
		t.Fatalf("get parent = %d: %s", w.Code, w.Body.String())
	}
	ids := make([]string, len(full.Children))
	for i, c := range full.Children {
		ids[i] = c.ID
	}
	return ids
}

func TestIntegration_ReorderSubtasks(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token, ws := registerUserInWorkspace(t, h, "sub-order@test.com", "Order", "SecurePassword1")
	parentID := createTaskViaAPI(t, h, token, "feature")
	otherID := createTaskViaAPI(t, h, token, "unrelated")
	var kids []string
	for _, title := range []string{"api", "ui", "e2e"} {
		kids = append(kids, createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": title})["id"].(string))
	}
	path := "/tasks/" + parentID + "/subtasks/order"

	want := []string{kids[2], kids[0], kids[1]}
	w := putJSON(t, h, path, map[string]any{"ids": want}, token)
	if w.Code != http.StatusOK {
		t.Fatalf("reorder = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := childOrder(t, h, token, parentID); !slices.Equal(got, want) {
		t.Fatalf("children after reorder = %v, want %v", got, want)
	}

	w = putJSON(t, h, path, map[string]any{"ids": []string{kids[0], kids[1], otherID}}, token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("foreign id = %d, want 400: %s", w.Code, w.Body.String())
	}
	var refusal map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &refusal)
	if refusal["code"] != "api.tasks.errors.subtask_order_not_child" || refusal["offending"] != otherID {
		t.Errorf("foreign id refusal = %v, want the not_child code naming %s", refusal, otherID)
	}

	for name, ids := range map[string][]string{
		"missing":  {kids[0], kids[1]},
		"repeated": {kids[0], kids[0], kids[1]},
	} {
		w = putJSON(t, h, path, map[string]any{"ids": ids}, token)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s child = %d, want 400: %s", name, w.Code, w.Body.String())
		}
		refusal = nil
		_ = json.Unmarshal(w.Body.Bytes(), &refusal)
		if refusal["code"] != "api.tasks.errors.subtask_order_incomplete" {
			t.Errorf("%s child refusal = %v, want the incomplete code", name, refusal)
		}
	}
	if got := childOrder(t, h, token, parentID); !slices.Equal(got, want) {
		t.Errorf("children after refused writes = %v, want %v unchanged", got, want)
	}

	viewer := mintKey(t, integrationServerOf(t, h), ws, auth.RoleViewer, auth.PermAll)
	if w := keyDo(t, h, http.MethodPut, path, viewer, map[string]any{"ids": kids}); w.Code != http.StatusForbidden {
		t.Errorf("viewer reorder = %d, want 403: %s", w.Code, w.Body.String())
	}
}
