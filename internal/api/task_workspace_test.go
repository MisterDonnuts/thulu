package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestCreateTask_UnscopedCallerInheritsProjectWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskws@test.com", "TW", "SecurePassword1")
	projID, projWS := projectOwnedByUser(t, h, srv, token)

	cw := postJSON(t, h, "/tasks", map[string]any{
		"title":       "inherits the project's workspace",
		"project_ids": []string{projID},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d, want 201: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)

	got := taskWorkspaceOf(t, srv, created["id"].(string))
	if got != projWS {
		t.Errorf("task workspace = %q, want the project's %q", got, projWS)
	}
}

func TestCreateTask_UnownedIsRefused(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	cw := postJSON(t, h, "/tasks", map[string]any{
		"title":       "nobody owns me",
		"project_ids": []string{},
	})
	if cw.Code != http.StatusBadRequest {
		t.Fatalf("POST /tasks with no workspace to derive = %d, want 400: %s", cw.Code, cw.Body.String())
	}
}

func TestCreateTask_UnscopedCallerTargetsNamedWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	const target = "ws-named-by-the-caller"

	cw := postJSON(t, h, "/tasks?workspace="+target, map[string]any{
		"title":       "no repo, explicit workspace",
		"project_ids": []string{},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks?workspace= = %d, want 201: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)

	if got := taskWorkspaceOf(t, srv, created["id"].(string)); got != target {
		t.Errorf("task workspace = %q, want %q", got, target)
	}
}

func TestCreateTask_ScopedCallerCannotTargetAnotherWorkspace(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskws-scoped@test.com", "TWS", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks?workspace=somebody-else", token, map[string]any{
		"title": "trying to write next door",
	})
	if cw.Code != http.StatusForbidden {
		t.Fatalf("POST /tasks?workspace=<other> = %d, want 403: %s", cw.Code, cw.Body.String())
	}
}

func projectOwnedByUser(t *testing.T, h http.Handler, srv *Server, token string) (string, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"name": "WS Probe", "repo_url": testgit.BareRepo(t)})
	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /projects = %d, want 201: %s", w.Code, w.Body.String())
	}
	var proj map[string]any
	json.NewDecoder(w.Body).Decode(&proj)
	id, _ := proj["id"].(string)
	if id == "" {
		t.Fatal("POST /projects returned no id")
	}
	stored, err := srv.projectStore.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("get project %s: %v", id, err)
	}
	if stored.WorkspaceID == "" {
		t.Fatal("project came back unowned; the fixture cannot prove inheritance")
	}
	return id, stored.WorkspaceID
}

func taskWorkspaceOf(t *testing.T, srv *Server, id string) string {
	t.Helper()
	tk, err := srv.taskStore.GetRow(t.Context(), id)
	if err != nil {
		t.Fatalf("get task %s: %v", id, err)
	}
	return tk.WorkspaceID
}

func TestCreateProject_UnownedIsRefused(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := postJSON(t, h, "/projects", map[string]any{"name": "nobody's", "repo_url": testgit.BareRepo(t)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /projects with no workspace to derive = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "workspace") {
		t.Errorf("the refusal should say what is missing, got %s", w.Body.String())
	}
}

func TestCreateProject_TakesTheCallersWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "proj-ws@test.com", "PW")

	b, _ := json.Marshal(map[string]any{"name": "owned", "repo_url": testgit.BareRepo(t)})
	req := httptest.NewRequest(http.MethodPost, "/projects", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /projects = %d, want 201: %s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.NewDecoder(w.Body).Decode(&created)
	stored, err := srv.projectStore.Get(t.Context(), created["id"].(string))
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if stored.WorkspaceID != ws {
		t.Errorf("project workspace = %q, want the caller's %q", stored.WorkspaceID, ws)
	}
}
