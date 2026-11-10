package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

type sandboxGatewayRig struct {
	srv            *Server
	h              http.Handler
	ts             *httptest.Server
	tokenA, wsA    string
	tokenB         string
	taskA, taskB   string
	runKeyA        string
	chatKeyA       string
	subtaskOfA     string
	runKeySubtaskA string
}

func newSandboxGatewayRig(t *testing.T) *sandboxGatewayRig {
	t.Helper()
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "sbx-a@test.com", "SbxA")
	tokenB, _ := mintWorkspaceUser(t, srv, "sbx-b@test.com", "SbxB")
	taskA := taskIDFor(t, h, tokenA, "sandbox A")
	taskB := taskIDFor(t, h, tokenB, "sandbox B")

	sw := postJSONAuth(t, h, "/tasks/"+taskA+"/subtasks", tokenA, map[string]any{"title": "child of A"})
	if sw.Code != http.StatusCreated {
		t.Fatalf("create subtask = %d: %s", sw.Code, sw.Body.String())
	}
	var sub struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(sw.Body.Bytes(), &sub)

	mint := func(scope auth.RunScope) string {
		raw, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(), scope, wsA, time.Hour)
		if err != nil {
			t.Fatalf("mint run key: %v", err)
		}
		return raw
	}

	srv.sandbox.mcp = srv.newSandboxMCPHandler(nil)
	mux := http.NewServeMux()
	srv.registerSandboxMCPRoutes(mux, auth.RequireRole)
	ts := httptest.NewServer(srv.buildHandler(mux))
	t.Cleanup(ts.Close)

	return &sandboxGatewayRig{
		srv: srv, h: h, ts: ts,
		tokenA: tokenA, wsA: wsA, tokenB: tokenB,
		taskA: taskA, taskB: taskB,
		runKeyA:        mint(auth.RunScope{TaskID: taskA, Agent: "developer", Stage: "in_progress"}),
		chatKeyA:       mint(auth.RunScope{ChatID: "chat-1", Agent: "chat"}),
		subtaskOfA:     sub.ID,
		runKeySubtaskA: mint(auth.RunScope{TaskID: sub.ID, Agent: "developer", Stage: "in_progress"}),
	}
}

func (g *sandboxGatewayRig) openSSE(t *testing.T, taskID string, cred func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, g.ts.URL+"/sandbox/sse?task_id="+taskID, nil)
	cred(req)
	resp, err := g.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /sandbox/sse: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return resp, ""
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			return resp, data
		}
	}
	t.Fatalf("SSE stream ended before the endpoint event: %v", sc.Err())
	return nil, ""
}

func withKey(key string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("X-API-Key", key) }
}

func withBearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func TestSandboxSSE_RefusesCallersOutsideTheTask(t *testing.T) {
	t.Parallel()
	g := newSandboxGatewayRig(t)

	cases := []struct {
		name   string
		taskID string
		cred   func(*http.Request)
		want   int
	}{
		{"run key on another tenant's task", g.taskB, withKey(g.runKeyA), http.StatusForbidden},
		{"run key on an unknown task", "no-such-task", withKey(g.runKeyA), http.StatusForbidden},
		{"chat run key (no task)", g.taskA, withKey(g.chatKeyA), http.StatusForbidden},
		{"session from another workspace", g.taskA, withBearer(g.tokenB), http.StatusNotFound},
		{"session on an unknown task", "no-such-task", withBearer(g.tokenA), http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, _ := g.openSSE(t, c.taskID, c.cred)
			if resp.StatusCode != c.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, c.want)
			}
		})
	}
}

func TestSandboxSSE_AllowsTheTasksOwnCallers(t *testing.T) {
	t.Parallel()
	g := newSandboxGatewayRig(t)

	cases := []struct {
		name   string
		taskID string
		cred   func(*http.Request)
	}{
		{"run key on its own task", g.taskA, withKey(g.runKeyA)},
		{"subtask run key on the parent's shared sandbox", g.taskA, withKey(g.runKeySubtaskA)},
		{"parent run key on its subtask", g.subtaskOfA, withKey(g.runKeyA)},
		{"developer session in the task's workspace", g.taskA, withBearer(g.tokenA)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, endpoint := g.openSSE(t, c.taskID, c.cred)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if !strings.HasPrefix(endpoint, "/sandbox/messages?sessionId=") {
				t.Fatalf("endpoint event = %q", endpoint)
			}
		})
	}
}

func TestSandboxMessages_BoundToTheSessionsCaller(t *testing.T) {
	t.Parallel()
	g := newSandboxGatewayRig(t)
	resp, endpoint := g.openSSE(t, g.taskA, withKey(g.runKeyA))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open session = %d", resp.StatusCode)
	}

	post := func(cred func(*http.Request)) int {
		body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, g.ts.URL+endpoint, body)
		req.Header.Set("Content-Type", "application/json")
		cred(req)
		r, err := g.ts.Client().Do(req)
		if err != nil {
			t.Fatalf("POST messages: %v", err)
		}
		_ = r.Body.Close()
		return r.StatusCode
	}

	if got := post(withBearer(g.tokenB)); got != http.StatusNotFound {
		t.Errorf("another tenant's session posting to the session = %d, want 404", got)
	}
	if got := post(withKey(g.runKeySubtaskA)); got != http.StatusNotFound {
		t.Errorf("a different run key posting to the session = %d, want 404", got)
	}
	if got := post(withKey(g.runKeyA)); got != http.StatusAccepted {
		t.Errorf("the session's own key = %d, want 202", got)
	}
}

func TestSandboxListings_ScopedToTheCallersWorkspace(t *testing.T) {
	t.Parallel()
	g := newSandboxGatewayRig(t)
	ctx := t.Context()
	now := time.Now()
	st := g.srv.sandbox.store
	for _, sb := range []*sandbox.Sandbox{
		{TaskID: g.taskA, WorkspaceID: g.wsA, ContainerID: "ca", Status: sandbox.StatusReady, CreatedAt: now, LastActivityAt: now},
		{TaskID: g.taskB, ContainerID: "cb", Status: sandbox.StatusReady, CreatedAt: now, LastActivityAt: now},
	} {
		if err := st.Upsert(ctx, sb); err != nil {
			t.Fatalf("upsert sandbox: %v", err)
		}
	}
	for _, id := range []string{g.taskA, g.taskB} {
		if err := st.Enqueue(ctx, id, "in_progress", 1); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	taskIDs := func(w *httptest.ResponseRecorder) []string {
		var rows []struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.TaskID)
		}
		return out
	}

	for _, path := range []string{"/sandboxes", "/sandboxes?include=all", "/sandboxes/queue"} {
		w := getJSON(t, g.h, path, g.tokenA)
		if w.Code != http.StatusOK {
			t.Fatalf("A GET %s = %d: %s", path, w.Code, w.Body.String())
		}
		got := taskIDs(w)
		if len(got) != 1 || got[0] != g.taskA {
			t.Errorf("A GET %s = %v, want only %s", path, got, g.taskA)
		}
		if rw := getWithKey(t, g.h, path, g.runKeyA); rw.Code != http.StatusForbidden {
			t.Errorf("run key GET %s = %d, want 403", path, rw.Code)
		}
	}
}

func TestTaskSandbox_NoneIsNoContent(t *testing.T) {
	g := newSandboxGatewayRig(t)
	if w := getJSON(t, g.h, "/tasks/"+g.taskA+"/sandbox", g.tokenA); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Errorf("own task without a sandbox = %d %q, want 204 and no body", w.Code, w.Body.String())
	}
	if w := getJSON(t, g.h, "/tasks/"+g.taskB+"/sandbox", g.tokenA); w.Code != http.StatusNotFound {
		t.Errorf("another workspace's task = %d, want 404", w.Code)
	}
}

func TestTaskSandbox_NoneAnswersNoContent(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "sbx-none@test.com", "SbxNone")
	id := taskIDFor(t, h, token, "no sandbox")
	w := getJSON(t, h, "/tasks/"+id+"/sandbox", token)
	if w.Code != http.StatusNoContent {
		t.Fatalf("GET /tasks/{id}/sandbox = %d want 204: %s", w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/tasks/nope/sandbox", token); w.Code != http.StatusNotFound {
		t.Errorf("GET /tasks/{unknown}/sandbox = %d want 404", w.Code)
	}
}

func TestTaskSandboxNoneIsNoContent(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	_, taskID := newProjectWithTask(t, h, "no-sandbox")
	if w := getJSON(t, h, "/tasks/"+taskID+"/sandbox", ""); w.Code != http.StatusNoContent {
		t.Fatalf("GET sandbox of a task without one = %d %s, want 204", w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/tasks/no-such-task/sandbox", ""); w.Code != http.StatusNotFound {
		t.Errorf("GET sandbox of a missing task = %d, want 404", w.Code)
	}
}

func TestSandboxListing_NamesThePrewarmProject(t *testing.T) {
	t.Parallel()
	g := newSandboxGatewayRig(t)
	ctx := t.Context()
	projectID := createProject(t, g.h, g.tokenA, "tienda-api")
	now := time.Now()
	if err := g.srv.sandbox.store.Upsert(ctx, &sandbox.Sandbox{
		TaskID: sandbox.PrewarmOwner(projectID), WorkspaceID: g.wsA,
		Status: sandbox.StatusStarting, CreatedAt: now, LastActivityAt: now,
	}); err != nil {
		t.Fatalf("upsert prewarm: %v", err)
	}

	w := getJSON(t, g.h, "/sandboxes", g.tokenA)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /sandboxes = %d: %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want the prewarm", rows)
	}
	if rows[0]["project_name"] != "tienda-api" {
		t.Errorf("project_name = %v, want tienda-api", rows[0]["project_name"])
	}
	if _, ok := rows[0]["publishes_ports"].(bool); !ok {
		t.Errorf("publishes_ports missing from %v", rows[0])
	}
}
