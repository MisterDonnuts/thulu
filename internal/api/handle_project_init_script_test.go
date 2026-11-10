package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func decodeInitScript(t *testing.T, body []byte) project.InitScript {
	t.Helper()
	var sc project.InitScript
	if err := json.Unmarshal(body, &sc); err != nil {
		t.Fatalf("decode init script: %v (%s)", err, body)
	}
	return sc
}

func TestInitScript_SaveHistoryAndRestore(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "init-a@test.com", "InitA")
	pid := createProject(t, h, token, "Init App")
	base := "/projects/" + pid + "/init-script"

	w := getJSON(t, h, base, token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET before any save = %d: %s", w.Code, w.Body.String())
	}
	if sc := decodeInitScript(t, w.Body.Bytes()); sc.Revision != 0 || sc.Manifest.Workdir != "." {
		t.Fatalf("empty script = %+v", sc)
	}

	w = sendJSON(t, h, http.MethodPut, base, token, map[string]any{
		"script":      "npm ci && npm run dev",
		"manifest":    map[string]any{"ports": []int{3000}, "params": []map[string]any{{"name": "NODE_ENV", "default": "development"}}},
		"note":        "first",
		"author_kind": "agent", "author_label": "someone else",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	first := decodeInitScript(t, w.Body.Bytes())
	if first.Revision != 1 || first.AuthorKind != project.InitAuthorUser || first.AuthorLabel == "someone else" {
		t.Fatalf("first = %+v", first)
	}

	if w = sendJSON(t, h, http.MethodPut, base, token, map[string]any{
		"script": "pnpm i && pnpm dev", "manifest": map[string]any{"ports": []int{5173}},
	}); w.Code != http.StatusOK {
		t.Fatalf("PUT 2 = %d: %s", w.Code, w.Body.String())
	}

	for name, body := range map[string]any{
		"empty script":   map[string]any{"script": " "},
		"bad param name": map[string]any{"script": "x", "manifest": map[string]any{"params": []map[string]any{{"name": "lower"}}}},
		"bad port":       map[string]any{"script": "x", "manifest": map[string]any{"ports": []int{70000}}},
	} {
		if w := sendJSON(t, h, http.MethodPut, base, token, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d, want 400: %s", name, w.Code, w.Body.String())
		}
	}

	w = getJSON(t, h, base+"/revisions", token)
	var history []project.InitScriptSummary
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history) != 2 || history[0].Revision != 2 {
		t.Fatalf("history = %s (%v)", w.Body.String(), err)
	}

	w = getJSON(t, h, base+"/revisions/1", token)
	if sc := decodeInitScript(t, w.Body.Bytes()); w.Code != http.StatusOK || sc.Script != "npm ci && npm run dev" {
		t.Fatalf("GET revision 1 = %d: %s", w.Code, w.Body.String())
	}
	if w = getJSON(t, h, base+"/revisions/9", token); w.Code != http.StatusNotFound {
		t.Fatalf("GET revision 9 = %d, want 404", w.Code)
	}
	if w = getJSON(t, h, base+"/revisions/abc", token); w.Code != http.StatusBadRequest {
		t.Fatalf("GET revision abc = %d, want 400", w.Code)
	}

	w = postJSONAuth(t, h, base+"/revisions/1/restore", token, map[string]any{"note": "back to npm"})
	if w.Code != http.StatusOK {
		t.Fatalf("restore = %d: %s", w.Code, w.Body.String())
	}
	restored := decodeInitScript(t, w.Body.Bytes())
	if restored.Revision != 3 || restored.RestoredFrom != 1 || restored.Script != first.Script || restored.Manifest.Ports[0] != 3000 {
		t.Fatalf("restored = %+v", restored)
	}
	if w = postJSONAuth(t, h, base+"/revisions/9/restore", token, nil); w.Code != http.StatusNotFound {
		t.Fatalf("restore of a missing revision = %d, want 404", w.Code)
	}

	if cur := decodeInitScript(t, getJSON(t, h, base, token).Body.Bytes()); cur.Revision != 3 {
		t.Fatalf("current after restore = %+v", cur)
	}

	entries, err := srv.auditStore.List(audit.ListOptions{Kind: audit.KindProjectInitScriptChanged, ResourceID: pid})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("audit entries = %d, want 3 (two saves and a restore)", len(entries))
	}
	for _, e := range entries {
		if e.WorkspaceID != ws || e.Metadata["author_kind"] != project.InitAuthorUser {
			t.Errorf("audit entry = %+v", e)
		}
	}
}

func TestInitScript_Authz(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	adminA, wsA := mintWorkspaceUser(t, srv, "init-admin@test.com", "Admin")
	tokenB, _ := mintWorkspaceUser(t, srv, "init-b@test.com", "Other")
	pid := createProject(t, h, adminA, "Guarded")
	base := "/projects/" + pid + "/init-script"
	if w := sendJSON(t, h, http.MethodPut, base, adminA, map[string]any{"script": "true"}); w.Code != http.StatusOK {
		t.Fatalf("seed PUT = %d: %s", w.Code, w.Body.String())
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, base},
		{http.MethodPut, base},
		{http.MethodGet, base + "/revisions"},
		{http.MethodGet, base + "/revisions/1"},
		{http.MethodPost, base + "/revisions/1/restore"},
	} {
		if w := sendJSON(t, h, c.method, c.path, tokenB, map[string]any{"script": "evil"}); w.Code != http.StatusNotFound {
			t.Errorf("other workspace %s %s = %d, want 404", c.method, c.path, w.Code)
		}
	}

	viewer := mintKey(t, srv, wsA, auth.RoleViewer, auth.PermAll)
	developer := mintKey(t, srv, wsA, auth.RoleDeveloper, auth.PermAll)
	if w := keyDo(t, h, http.MethodGet, base, viewer, nil); w.Code != http.StatusOK {
		t.Fatalf("viewer GET = %d", w.Code)
	}
	if w := keyDo(t, h, http.MethodGet, base+"/revisions", viewer, nil); w.Code != http.StatusOK {
		t.Fatalf("viewer GET revisions = %d", w.Code)
	}
	for _, key := range []string{viewer, developer} {
		if w := keyDo(t, h, http.MethodPut, base, key, map[string]any{"script": "x"}); w.Code != http.StatusForbidden {
			t.Fatalf("non-admin PUT = %d, want 403", w.Code)
		}
		if w := keyDo(t, h, http.MethodPost, base+"/revisions/1/restore", key, nil); w.Code != http.StatusForbidden {
			t.Fatalf("non-admin restore = %d, want 403", w.Code)
		}
	}

	taskID := createTaskWithProjects(t, h, adminA, "run", pid)
	runKey, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: taskID, Agent: "developer", Stage: "in_progress"}, wsA, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if w := keyDo(t, h, http.MethodPut, base, runKey, map[string]any{"script": "x"}); w.Code != http.StatusForbidden {
		t.Fatalf("run key PUT = %d, want 403", w.Code)
	}

	if w := sendJSON(t, h, http.MethodDelete, "/projects/"+pid, adminA, nil); w.Code != http.StatusNoContent && w.Code != http.StatusOK {
		t.Fatalf("delete project = %d: %s", w.Code, w.Body.String())
	}
	if _, err := srv.projectStore.InitScriptRevision(t.Context(), pid, 1); err == nil {
		t.Fatal("init script revisions survived the project delete")
	}
}

func createTaskWithProjects(t *testing.T, h http.Handler, token, title string, projectIDs ...string) string {
	t.Helper()
	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": title, "project_ids": projectIDs})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	return created.ID
}

type sseSession struct {
	g        *sandboxGatewayRig
	sc       *bufio.Scanner
	endpoint string
	cred     func(*http.Request)
	nextID   int
}

func openToolSession(t *testing.T, g *sandboxGatewayRig, taskID string, cred func(*http.Request)) *sseSession {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, g.ts.URL+"/sandbox/sse?task_id="+taskID, nil)
	cred(req)
	resp, err := g.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open session = %d", resp.StatusCode)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	s := &sseSession{g: g, sc: bufio.NewScanner(resp.Body), cred: cred}
	s.sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	s.endpoint = s.nextData(t)
	return s
}

func (s *sseSession) nextData(t *testing.T) string {
	t.Helper()
	for s.sc.Scan() {
		if data, ok := strings.CutPrefix(s.sc.Text(), "data: "); ok {
			return data
		}
	}
	t.Fatalf("SSE stream ended: %v", s.sc.Err())
	return ""
}

func (s *sseSession) call(t *testing.T, tool string, args any) (string, bool) {
	t.Helper()
	s.nextID++
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": s.nextID, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, s.g.ts.URL+s.endpoint, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	s.cred(req)
	resp, err := s.g.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST tools/call = %d", resp.StatusCode)
	}
	var msg struct {
		ID     int `json:"id"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(s.nextData(t)), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ID != s.nextID || len(msg.Result.Content) == 0 {
		t.Fatalf("unexpected response %+v", msg)
	}
	return msg.Result.Content[0].Text, msg.Result.IsError
}

func TestInitScript_AgentWritesThroughTheSandbox(t *testing.T) {
	t.Parallel()
	g := newSandboxGatewayRig(t)
	ownPID := createProject(t, g.h, g.tokenA, "Agent App")
	otherPID := createProject(t, g.h, g.tokenA, "Not On Task")
	taskID := createTaskWithProjects(t, g.h, g.tokenA, "agent task", ownPID)
	runKey, _, err := g.srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: taskID, Agent: "developer", Stage: "in_progress"}, g.wsA, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := openToolSession(t, g, taskID, withKey(runKey))

	text, isErr := s.call(t, "init_status", map[string]any{})
	if isErr || !strings.Contains(text, `"has_script": false`) || strings.Contains(text, "Not On Task") {
		t.Fatalf("init_status = %s", text)
	}

	text, isErr = s.call(t, "init_script_update", map[string]any{
		"project": "Agent App", "script": "make up", "note": "found it",
		"manifest":     map[string]any{"ports": []int{8080}, "ready": map[string]any{"command": "curl -fs localhost:8080"}},
		"author_label": "admin@example.com",
	})
	if isErr {
		t.Fatalf("init_script_update = %s", text)
	}
	sc, err := g.srv.projectStore.InitScript(t.Context(), ownPID)
	if err != nil {
		t.Fatal(err)
	}
	wantLabel := fmt.Sprintf("agent developer · task %s · stage in_progress", taskID)
	if sc.AuthorKind != project.InitAuthorAgent || sc.AuthorLabel != wantLabel || sc.Note != "found it" {
		t.Fatalf("saved = kind %q label %q note %q, want agent %q", sc.AuthorKind, sc.AuthorLabel, sc.Note, wantLabel)
	}

	text, isErr = s.call(t, "init_script_get", map[string]any{"project": ownPID})
	if isErr || !strings.Contains(text, `"script": "make up"`) {
		t.Fatalf("init_script_get = %s", text)
	}

	for _, p := range []string{"Not On Task", otherPID} {
		if text, isErr := s.call(t, "init_script_update", map[string]any{"project": p, "script": "evil", "note": "n"}); !isErr {
			t.Fatalf("update of a project outside the task = %s", text)
		}
	}
	if _, err := g.srv.projectStore.InitScript(t.Context(), otherPID); err == nil {
		t.Fatal("a project outside the task got a script")
	}

	entries, err := g.srv.auditStore.List(audit.ListOptions{Kind: audit.KindProjectInitScriptChanged, ResourceID: ownPID})
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit = %v, %v", entries, err)
	}
	if e := entries[0]; e.ActorType != auth.ActorAgent || e.WorkspaceID != g.wsA || e.Metadata["author"] != wantLabel {
		t.Fatalf("audit entry = %+v", e)
	}
}
