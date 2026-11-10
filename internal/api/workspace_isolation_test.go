package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
)

func mintWorkspaceUser(t *testing.T, srv *Server, email, name string) (token, wsID string) {
	t.Helper()
	return mintWorkspaceUserAs(t, srv, email, name, auth.RoleAdmin)
}

func mintWorkspaceUserAs(t *testing.T, srv *Server, email, name string, role auth.Role) (token, wsID string) {
	t.Helper()
	u, err := srv.authStore.CreateUser(t.Context(), email, name)
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	slug := strings.ReplaceAll(strings.Split(email, "@")[0], ".", "-")
	ws, err := srv.authStore.CreateWorkspace(t.Context(), u.ID, name+"'s workspace", slug)
	if err != nil {
		t.Fatalf("create workspace for %s: %v", email, err)
	}
	token, err = auth.IssueJWT(auth.Claims{
		UserID:      u.ID,
		Email:       u.Email,
		WorkspaceID: ws.ID,
		Role:        role,
	})
	if err != nil {
		t.Fatalf("issue jwt for %s: %v", email, err)
	}
	return token, ws.ID
}

func TestIntegration_WorkspaceIsolation_Projects(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "iso-a@test.com", "IsoA")
	tokenB, _ := mintWorkspaceUser(t, srv, "iso-b@test.com", "IsoB")

	projID := createProject(t, h, tokenA, "Iso Project A")

	if w := getJSON(t, h, "/projects/"+projID, tokenA); w.Code != http.StatusOK {
		t.Fatalf("A GET own project = %d: %s", w.Code, w.Body.String())
	}

	lw := getJSON(t, h, "/projects", tokenB)
	if lw.Code != http.StatusOK {
		t.Fatalf("B GET /projects = %d: %s", lw.Code, lw.Body.String())
	}
	var list []map[string]any
	json.NewDecoder(lw.Body).Decode(&list)
	for _, p := range list {
		if p["id"] == projID {
			t.Errorf("B's /projects list leaked A's project: %+v", p)
		}
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/projects/" + projID},
		{http.MethodPatch, "/projects/" + projID},
		{http.MethodDelete, "/projects/" + projID},
		{http.MethodGet, "/projects/" + projID + "/worktrees"},
		{http.MethodGet, "/projects/" + projID + "/stage-files"},
		{http.MethodGet, "/projects/" + projID + "/stage-files/prompts/backlog"},
		{http.MethodPut, "/projects/" + projID + "/stage-files/prompts/backlog"},
		{http.MethodDelete, "/projects/" + projID + "/stage-files/prompts/backlog"},
		{http.MethodGet, "/projects/" + projID + "/github-credential"},
		{http.MethodPut, "/projects/" + projID + "/github-credential"},
	} {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokenB)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("B %s %s = %d, want 404: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}

	pw := patchJSONAuth(t, h, "/projects/"+projID, tokenA, map[string]any{"description": "still mine"})
	if pw.Code != http.StatusOK {
		t.Errorf("A PATCH own project = %d: %s", pw.Code, pw.Body.String())
	}
}

func TestIntegration_WorkspaceIsolation_Tasks(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "iso-ta@test.com", "IsoTA")
	tokenB, _ := mintWorkspaceUser(t, srv, "iso-tb@test.com", "IsoTB")

	projA := createProject(t, h, tokenA, "Iso Task Project A")

	cw := postJSONAuth(t, h, "/tasks", tokenA, map[string]any{
		"title": "A's secret task", "project_ids": []string{projA},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("A POST /tasks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	taskID, _ := created["id"].(string)
	if got, _ := created["workspace_id"].(string); got != wsA {
		t.Errorf("created task workspace_id = %q, want %q", got, wsA)
	}

	lw := getJSON(t, h, "/tasks", tokenB)
	if lw.Code != http.StatusOK {
		t.Fatalf("B GET /tasks = %d: %s", lw.Code, lw.Body.String())
	}
	var list []map[string]any
	json.NewDecoder(lw.Body).Decode(&list)
	for _, tk := range list {
		if tk["id"] == taskID {
			t.Errorf("B's /tasks list leaked A's task: %+v", tk)
		}
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/tasks/" + taskID},
		{http.MethodPatch, "/tasks/" + taskID},
		{http.MethodDelete, "/tasks/" + taskID},
		{http.MethodPost, "/tasks/" + taskID + "/clone"},
		{http.MethodPost, "/tasks/" + taskID + "/fork"},
		{http.MethodPost, "/tasks/" + taskID + "/relaunch"},
		{http.MethodPost, "/tasks/" + taskID + "/ensure-pr"},
		{http.MethodPost, "/tasks/" + taskID + "/comments"},
		{http.MethodPost, "/tasks/" + taskID + "/attachments"},
		{http.MethodGet, "/tasks/" + taskID + "/brief"},
		{http.MethodGet, "/tasks/" + taskID + "/reports"},
		{http.MethodGet, "/tasks/" + taskID + "/flags"},
		{http.MethodPost, "/tasks/" + taskID + "/flags"},
		{http.MethodPatch, "/tasks/" + taskID + "/flags/someflag"},
		{http.MethodDelete, "/tasks/" + taskID + "/flags/someflag"},
		{http.MethodGet, "/tasks/" + taskID + "/events"},
		{http.MethodGet, "/tasks/" + taskID + "/usage"},
		{http.MethodGet, "/tasks/" + taskID + "/dependencies"},
		{http.MethodPost, "/tasks/" + taskID + "/dependencies"},
		{http.MethodDelete, "/tasks/" + taskID + "/dependencies/other"},
	} {
		body := `{"body":"x","label":"x","depends_on_task_id":"other"}`
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokenB)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("B %s %s = %d, want 404: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}

	bw := postJSONAuth(t, h, "/tasks", tokenB, map[string]any{
		"title": "steal A's repo", "project_ids": []string{projA},
	})
	if bw.Code != http.StatusBadRequest {
		t.Errorf("B POST /tasks with A's project = %d, want 400: %s", bw.Code, bw.Body.String())
	}

	bcw := postJSONAuth(t, h, "/tasks", tokenB, map[string]any{"title": "B's own task"})
	if bcw.Code != http.StatusCreated {
		t.Fatalf("B POST /tasks = %d: %s", bcw.Code, bcw.Body.String())
	}
	var bTask map[string]any
	json.NewDecoder(bcw.Body).Decode(&bTask)
	bTaskID, _ := bTask["id"].(string)
	bpw := patchJSONAuth(t, h, "/tasks/"+bTaskID, tokenB, map[string]any{"project_ids": []string{projA}})
	if bpw.Code != http.StatusBadRequest {
		t.Errorf("B PATCH own task with A's project = %d, want 400: %s", bpw.Code, bpw.Body.String())
	}

	fw := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", tokenA, map[string]any{"label": "A's gate"})
	if fw.Code != http.StatusCreated {
		t.Fatalf("A POST flag = %d: %s", fw.Code, fw.Body.String())
	}
	var aFlag map[string]any
	json.NewDecoder(fw.Body).Decode(&aFlag)
	aFlagID, _ := aFlag["id"].(string)
	if w := patchJSONAuth(t, h, "/tasks/"+bTaskID+"/flags/"+aFlagID, tokenB, map[string]any{"status": "approved"}); w.Code != http.StatusNotFound {
		t.Errorf("B PATCH A's flag via own task = %d, want 404: %s", w.Code, w.Body.String())
	}

	if w := getJSON(t, h, "/tasks/"+taskID, tokenA); w.Code != http.StatusOK {
		t.Errorf("A GET own task = %d: %s", w.Code, w.Body.String())
	}
	if w := postJSONAuth(t, h, "/tasks/"+taskID+"/comments", tokenA, map[string]any{"body": "mine"}); w.Code != http.StatusCreated {
		t.Errorf("A POST comment on own task = %d: %s", w.Code, w.Body.String())
	}

	aw := getJSON(t, h, "/tasks/"+taskID, "")
	if aw.Code != http.StatusOK {
		t.Errorf("unrestricted GET task = %d, want 200", aw.Code)
	}
}

func TestIntegration_WorkspaceIsolation_UnstampedRowsAreHidden(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenB, _ := mintWorkspaceUser(t, srv, "iso-legacy@test.com", "IsoL")

	cw := postJSON(t, h, "/tasks?workspace=ws-to-be-cleared", map[string]any{"title": "legacy task"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks (unauthenticated dev mode) = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	json.NewDecoder(cw.Body).Decode(&created)
	taskID, _ := created["id"].(string)
	if _, err := srv.taskStore.DB().Exec(
		`UPDATE tasks SET workspace_id = '' WHERE id = $1`, taskID); err != nil {
		t.Fatalf("clear workspace stamp: %v", err)
	}

	if w := getJSON(t, h, "/tasks/"+taskID, tokenB); w.Code != http.StatusNotFound {
		t.Errorf("B GET unstamped task = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/tasks", tokenB); strings.Contains(w.Body.String(), taskID) {
		t.Errorf("B's task list carries the unstamped task: %s", w.Body.String())
	}
	if w := getJSON(t, h, "/tasks/"+taskID, ""); w.Code != http.StatusOK {
		t.Errorf("unrestricted GET unstamped task = %d, want 200", w.Code)
	}
}

func TestIntegration_Metrics_TokenGate(t *testing.T) {
	const metricsToken = "test-metrics-token-123"
	t.Setenv("OCTOPUS_METRICS_TOKEN", metricsToken)
	_, h := newIntegrationServer(t)

	if w := getJSON(t, h, "/metrics", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics without token = %d, want 401", w.Code)
	}

	if w := getJSON(t, h, "/metrics", "wrong-token"); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /metrics with wrong token = %d, want 401", w.Code)
	}

	w := getJSON(t, h, "/metrics", metricsToken)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /metrics with token = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "octopus_") && !strings.Contains(w.Body.String(), "go_") {
		t.Errorf("metrics body doesn't look like Prometheus output: %.200s", w.Body.String())
	}
}

func TestIntegration_Metrics_DevModeWhenNoToken(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	if w := getJSON(t, h, "/metrics", ""); w.Code != http.StatusOK {
		t.Errorf("GET /metrics in dev mode = %d, want 200", w.Code)
	}
}

func TestIntegration_WorkspaceIsolation_Chats(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "chat-iso-a@test.com", "ChatIsoA")
	tokenB, _ := mintWorkspaceUser(t, srv, "chat-iso-b@test.com", "ChatIsoB")
	setupChatAgent(t, srv, "iso-chatty")

	chatID := createChat(t, h, tokenA, "iso-chatty")

	var listA, listB []map[string]any
	wa := getJSON(t, h, "/chats", tokenA)
	json.NewDecoder(wa.Body).Decode(&listA)
	if len(listA) != 1 {
		t.Errorf("owner list = %d chats, want 1", len(listA))
	}
	wb := getJSON(t, h, "/chats", tokenB)
	json.NewDecoder(wb.Body).Decode(&listB)
	if len(listB) != 0 {
		t.Errorf("other tenant list = %d chats, want 0", len(listB))
	}

	if w := getJSON(t, h, "/chats/"+chatID, tokenB); w.Code != http.StatusNotFound {
		t.Errorf("B GET chat = %d, want 404", w.Code)
	}
	if w := getJSON(t, h, "/chats/"+chatID+"/messages", tokenB); w.Code != http.StatusNotFound {
		t.Errorf("B GET messages = %d, want 404", w.Code)
	}
	if w := postJSONAuth(t, h, "/chats/"+chatID+"/messages", tokenB, map[string]string{"text": "hi"}); w.Code != http.StatusNotFound {
		t.Errorf("B POST message = %d, want 404", w.Code)
	}
	b, _ := json.Marshal(map[string]any{"title": "stolen"})
	r := httptest.NewRequest(http.MethodPatch, "/chats/"+chatID, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+tokenB)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("B PATCH chat = %d, want 404", w.Code)
	}
	dr := httptest.NewRequest(http.MethodDelete, "/chats/"+chatID, nil)
	dr.Header.Set("Authorization", "Bearer "+tokenB)
	dw := httptest.NewRecorder()
	h.ServeHTTP(dw, dr)
	if dw.Code != http.StatusNotFound {
		t.Errorf("B DELETE chat = %d, want 404", dw.Code)
	}

	if w := getJSON(t, h, "/chats/"+chatID, tokenA); w.Code != http.StatusOK {
		t.Errorf("A GET chat = %d, want 200", w.Code)
	}
}

func TestIntegration_ChatEvents_WorkspaceFiltered(t *testing.T) {
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "sse-a@test.com", "SseA")
	tokenB, _ := mintWorkspaceUser(t, srv, "sse-b@test.com", "SseB")

	ts := httptest.NewServer(h)
	defer ts.Close()

	stream := func(token string) (*http.Response, *bufio.Reader) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/chats/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("open SSE: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("SSE status = %d", resp.StatusCode)
		}
		return resp, bufio.NewReader(resp.Body)
	}
	respA, readerA := stream(tokenA)
	defer respA.Body.Close()
	respB, readerB := stream(tokenB)
	defer respB.Body.Close()

	for _, rd := range []*bufio.Reader{readerA, readerB} {
		line, err := rd.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, ": connected") {
			t.Fatalf("expected connected comment, got %q (%v)", line, err)
		}
	}

	srv.chatBus.Publish(chat.Event{Type: chat.EventTurnStarted, TurnID: "trn-1"}.
		For(&chat.Chat{ID: "chat-x", WorkspaceID: wsA, TaskID: "task-x"}))

	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		line, err := readerA.ReadString('\n')
		if err != nil {
			t.Fatalf("read A stream: %v", err)
		}
		if strings.HasPrefix(line, "event: ") {
			got = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			break
		}
	}
	if got != string(chat.EventTurnStarted) {
		t.Errorf("A got event %q, want turn_started", got)
	}

	done := make(chan string, 1)
	go func() {
		for {
			line, err := readerB.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event: ") {
				done <- line
				return
			}
		}
	}()
	select {
	case line := <-done:
		t.Errorf("B received cross-workspace event: %q", line)
	case <-time.After(500 * time.Millisecond):
	}
}
