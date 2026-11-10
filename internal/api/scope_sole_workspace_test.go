package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestDevMode_NewTaskLandsInTheOnlyWorkspace(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	w := postJSON(t, h, "/tasks", map[string]any{"title": "before any workspace"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /tasks with no workspace on the install = %d, want 400: %s", w.Code, w.Body.String())
	}

	_, wid := registerUserInWorkspace(t, h, "sole-ws@test.com", "Sole", "SecurePassword1")

	w = postJSON(t, h, "/tasks", map[string]any{"title": "after the workspace"})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d, want 201: %s", w.Code, w.Body.String())
	}
	if got := taskWorkspace(t, w); got != wid {
		t.Errorf("task created in workspace %q, want the install's only one %q", got, wid)
	}
}

func TestDevMode_NewTaskRefusedWithTwoWorkspaces(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	_, first := registerUserInWorkspace(t, h, "two-a@test.com", "TwoA", "SecurePassword1")
	registerUserInWorkspace(t, h, "two-b@test.com", "TwoB", "SecurePassword1")

	w := postJSON(t, h, "/tasks", map[string]any{"title": "which tenant?"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /tasks with two workspaces = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "workspace") {
		t.Errorf("the refusal should name what is missing; body = %s", w.Body.String())
	}

	named := postJSON(t, h, "/tasks?workspace="+first, map[string]any{"title": "named"})
	if named.Code != http.StatusCreated {
		t.Fatalf("POST /tasks?workspace= = %d, want 201: %s", named.Code, named.Body.String())
	}
	if got := taskWorkspace(t, named); got != first {
		t.Errorf("task created in %q, want the named %q", got, first)
	}
}

func TestDevMode_NewTaskTakesTheNamedBoardsWorkspace(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	registerUserInWorkspace(t, h, "board-a@test.com", "BoardA", "SecurePassword1")
	second, secondWID := registerUserInWorkspace(t, h, "board-b@test.com", "BoardB", "SecurePassword1")

	bw := getJSON(t, h, "/boards", second)
	var boards struct {
		Default string `json:"default"`
	}
	if err := json.Unmarshal(bw.Body.Bytes(), &boards); err != nil || boards.Default == "" {
		t.Fatalf("GET /boards = %d %s", bw.Code, bw.Body.String())
	}

	w := postJSON(t, h, "/tasks", map[string]any{"title": "no repo", "board_id": boards.Default})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /tasks on a named board = %d, want 201: %s", w.Code, w.Body.String())
	}
	if got := taskWorkspace(t, w); got != secondWID {
		t.Errorf("task created in %q, want the board's workspace %q", got, secondWID)
	}
}

func TestDevMode_NewProjectLandsInTheOnlyWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	_, wid := registerUserInWorkspace(t, h, "sole-proj@test.com", "SoleProj", "SecurePassword1")

	w := postJSON(t, h, "/projects", map[string]any{"name": "Thulu", "repo_url": testgit.BareRepo(t)})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /projects = %d, want 201: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode project: %v (body %s)", err, w.Body.String())
	}
	p, err := srv.projectStore.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get project %s: %v", created.ID, err)
	}
	if p.WorkspaceID != wid {
		t.Errorf("project created in workspace %q, want %q", p.WorkspaceID, wid)
	}
}

func TestDevMode_LLMKeyLandsInTheOnlyWorkspace(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	_, wid := registerUserInWorkspace(t, h, "sole-key@test.com", "SoleKey", "SecurePassword1")

	body, _ := json.Marshal(map[string]string{"provider": "anthropic", "key": "sk-ant-api-sole-workspace"})
	put := httptest.NewRequest(http.MethodPut, "/llm-key", strings.NewReader(string(body)))
	put.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, put)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT /llm-key = %d: %s", w.Code, w.Body.String())
	}
	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["workspace_id"] != wid {
		t.Errorf("key landed in workspace %v, want the install's only one %q", st["workspace_id"], wid)
	}
}

func taskWorkspace(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var created struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode task: %v (body %s)", err, w.Body.String())
	}
	return created.WorkspaceID
}
