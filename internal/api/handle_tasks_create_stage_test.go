package api

import (
	"net/http"
	"testing"
)

func TestCreateTask_RefusedInitialStageCreatesNothing(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "create-gate@test.com", "CG", "SecurePassword1")
	setupAgent(t, srv, "needs-repo")

	countTasks := func() int {
		t.Helper()
		all, err := srv.taskStore.List(t.Context())
		if err != nil {
			t.Fatalf("list tasks: %v", err)
		}
		return len(all)
	}

	w := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title":             "no repo",
		"stage":             "refinement",
		"stage_assignments": map[string]string{"refinement": "needs-repo"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("create into a column whose agent needs a repo = %d, want 409: %s", w.Code, w.Body.String())
	}
	if n := countTasks(); n != 0 {
		t.Errorf("a refused create left %d tasks behind", n)
	}

	w = postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "nowhere", "stage": "no-such-column"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create into an unknown column = %d, want 400: %s", w.Code, w.Body.String())
	}
	if n := countTasks(); n != 0 {
		t.Errorf("a create into an unknown column left %d tasks behind", n)
	}

	if w := patchJSONAuth(t, h, "/stages/backlog", "", map[string]any{
		"required_approvals": []map[string]any{{"key": "triage", "label": "Triage"}},
	}); w.Code != http.StatusOK {
		t.Fatalf("PATCH /stages/backlog = %d: %s", w.Code, w.Body.String())
	}
	w = postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "skip triage", "stage": "review"})
	if w.Code != http.StatusConflict {
		t.Fatalf("create past an approval = %d, want 409: %s", w.Code, w.Body.String())
	}
	if n := countTasks(); n != 0 {
		t.Errorf("a create held by an approval left %d tasks behind", n)
	}

	w = postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "plain", "stage": "backlog"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create into the first column = %d, want 201: %s", w.Code, w.Body.String())
	}
}
