package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTaskPatch_RefusesServerOwnedFields(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "patch-owned@test.com", "PO", "SecurePassword1")
	id := taskIDFor(t, h, token, "patch me")

	for field, value := range map[string]any{
		"worktree_paths":   map[string]string{"p1": "/etc"},
		"branch_name":      "--upload-pack=evil",
		"pr_urls":          map[string]string{"p1": "javascript:alert(1)"},
		"current_run_id":   "run-x",
		"current_instance": "inst-x",
	} {
		w := patchJSONAuth(t, h, "/tasks/"+id, token, map[string]any{"title": "x", field: value})
		if w.Code != http.StatusBadRequest {
			t.Errorf("PATCH with %s = %d, want 400: %s", field, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), field) {
			t.Errorf("PATCH with %s: the refusal does not name the field: %s", field, w.Body.String())
		}
	}

	got, err := srv.taskStore.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.WorktreePaths) != 0 || got.BranchName != "" || len(got.PRURLs) != 0 || got.Title != "patch me" {
		t.Fatalf("a refused patch still wrote: %+v", got)
	}

	w := patchJSONAuth(t, h, "/tasks/"+id, token, map[string]any{
		"title": "renamed", "body": "new body", "tags": []string{"a"}, "priority": "high",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("legitimate PATCH = %d: %s", w.Code, w.Body.String())
	}
	var updated map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &updated)
	if updated["title"] != "renamed" || updated["body"] != "new body" || updated["priority"] != "high" {
		t.Errorf("legitimate PATCH did not apply: %v", updated)
	}
}
