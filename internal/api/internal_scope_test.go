package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func postWithKey(t *testing.T, h http.Handler, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestInternalRoutes_RefuseOtherWorkspacesAndSessions(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "int-a@test.com", "IntA")
	tokenB, wsB := mintWorkspaceUser(t, srv, "int-b@test.com", "IntB")
	taskB := taskIDFor(t, h, tokenB, "victim")
	projB := createProject(t, h, tokenB, "victim-proj")

	keyA := mintKey(t, srv, wsA, auth.RoleOwner, auth.PermAll)
	runKeyA, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: taskIDFor(t, h, tokenA, "own"), Agent: "developer"}, wsA, time.Hour)
	if err != nil {
		t.Fatalf("mint run key: %v", err)
	}
	keyB := mintKey(t, srv, wsB, auth.RoleDeveloper, auth.PermInternalUse)

	type call struct {
		method, path string
	}
	calls := []call{
		{http.MethodGet, "/internal/tasks/" + taskB + "/github-token"},
		{http.MethodGet, "/internal/tasks/" + taskB + "/branch-status"},
		{http.MethodPost, "/internal/tasks/" + taskB + "/commit"},
		{http.MethodPost, "/internal/tasks/" + taskB + "/branch-push"},
		{http.MethodPost, "/internal/tasks/" + taskB + "/pr"},
		{http.MethodGet, "/internal/projects/" + projB + "/github-token"},
	}
	do := func(c call, key string) int {
		if c.method == http.MethodPost {
			return postWithKey(t, h, c.path, key).Code
		}
		return getWithKey(t, h, c.path, key).Code
	}

	for _, c := range calls {
		if got := do(c, keyA); got != http.StatusForbidden && got != http.StatusNotFound {
			t.Errorf("workspace A key %s %s = %d, want 403/404", c.method, c.path, got)
		}
		if got := do(c, runKeyA); got != http.StatusForbidden {
			t.Errorf("run key of another task %s %s = %d, want 403", c.method, c.path, got)
		}
		var w *httptest.ResponseRecorder
		if c.method == http.MethodPost {
			w = postJSONAuth(t, h, c.path, tokenB, map[string]any{})
		} else {
			w = getJSON(t, h, c.path, tokenB)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("owner session %s %s = %d, want 403", c.method, c.path, w.Code)
		}
	}

	if got := getWithKey(t, h, "/internal/tasks/"+taskB+"/github-token", keyB).Code; got != http.StatusForbidden {
		t.Errorf("workspace key reading a raw token = %d, want 403", got)
	}
	if got := getWithKey(t, h, "/internal/tasks/"+taskB+"/branch-status", keyB).Code; got != http.StatusOK {
		t.Errorf("own workspace key on branch-status = %d, want 200", got)
	}
}
