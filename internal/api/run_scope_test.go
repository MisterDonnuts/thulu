package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type runScopeRig struct {
	srv                *Server
	h                  http.Handler
	token, ws          string
	a, a1, a2, b, b1   string
	keyA, keyA1, keyB1 string
}

func newRunScopeRig(t *testing.T) *runScopeRig {
	t.Helper()
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "run-scope@test.com", "RunScope")
	rig := &runScopeRig{srv: srv, h: h, token: token, ws: ws}
	rig.a = taskIDFor(t, h, token, "A")
	rig.b = taskIDFor(t, h, token, "B")
	rig.a1 = subtaskIDFor(t, h, token, rig.a, "A1")
	rig.a2 = subtaskIDFor(t, h, token, rig.a, "A2")
	rig.b1 = subtaskIDFor(t, h, token, rig.b, "B1")
	rig.keyA = rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: "developer", Stage: "in_progress"})
	rig.keyA1 = rig.runKey(t, auth.RunScope{TaskID: rig.a1, Agent: "tester", Stage: "in_progress"})
	rig.keyB1 = rig.runKey(t, auth.RunScope{TaskID: rig.b1, Agent: "tester", Stage: "in_progress"})
	return rig
}

func (rig *runScopeRig) runKey(t *testing.T, scope auth.RunScope) string {
	t.Helper()
	raw, _, err := rig.srv.authStore.CreateRunScopedAPIKey(t.Context(), scope, rig.ws, time.Hour)
	if err != nil {
		t.Fatalf("mint run key %+v: %v", scope, err)
	}
	return raw
}

func subtaskIDFor(t *testing.T, h http.Handler, token, parent, title string) string {
	t.Helper()
	w := postJSONAuth(t, h, "/tasks/"+parent+"/subtasks", token, map[string]any{"title": title})
	if w.Code != http.StatusCreated {
		t.Fatalf("create subtask %q = %d: %s", title, w.Code, w.Body.String())
	}
	var created map[string]any
	json.NewDecoder(w.Body).Decode(&created)
	return created["id"].(string)
}

func keyDo(t *testing.T, h http.Handler, method, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

type scopedCall struct {
	method, path string
	body         any
}

func taskWrites(id string) []scopedCall {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	return []scopedCall{
		{http.MethodPatch, "/tasks/" + id, map[string]any{"title": "renamed by a run"}},
		{http.MethodDelete, "/tasks/" + id, nil},
		{http.MethodPost, "/tasks/" + id + "/subtasks", map[string]any{"title": "planted"}},
		{http.MethodPost, "/tasks/" + id + "/clone", nil},
		{http.MethodPost, "/tasks/" + id + "/fork", nil},
		{http.MethodPost, "/tasks/" + id + "/relaunch", map[string]any{}},
		{http.MethodPost, "/tasks/" + id + "/ensure-pr", nil},
		{http.MethodPost, "/tasks/" + id + "/comments", map[string]any{"body": "hello"}},
		{http.MethodPost, "/tasks/" + id + "/converse", map[string]any{"text": "hello"}},
		{http.MethodPost, "/tasks/" + id + "/conversation/m1/to-comment", nil},
		{http.MethodPost, "/tasks/" + id + "/attachments", map[string]any{
			"filename": "x.png", "mime_type": "image/png", "content_base64": png,
		}},
		{http.MethodPost, "/tasks/" + id + "/flags", map[string]any{"label": "needs a look"}},
		{http.MethodPost, "/tasks/" + id + "/flags/import", nil},
		{http.MethodPost, "/tasks/" + id + "/auditor-feedback?instance=i1", nil},
		{http.MethodPost, "/tasks/" + id + "/reports", map[string]any{"kind": "evidence", "body": "planted"}},
		{http.MethodPut, "/tasks/" + id + "/sections/goal", map[string]any{"content": "x"}},
		{http.MethodPatch, "/tasks/" + id + "/sections/goal/items/0", map[string]any{"checked": true}},
		{http.MethodPost, "/tasks/" + id + "/test-cases", map[string]any{"title": "planted case"}},
		{http.MethodPost, "/tasks/" + id + "/test-cases/import-body", nil},
		{http.MethodPatch, "/tasks/" + id + "/test-cases/TC-1", map[string]any{"status": "pass"}},
		{http.MethodPost, "/tasks/" + id + "/test-cases/TC-1/runs", map[string]any{"verdict": "pass"}},
		{http.MethodDelete, "/tasks/" + id + "/test-cases/TC-1", nil},
		{http.MethodPost, "/tasks/" + id + "/dependencies", map[string]any{"depends_on_task_id": "nope"}},
		{http.MethodDelete, "/tasks/" + id + "/dependencies/nope", nil},
		{http.MethodDelete, "/tasks/" + id + "/sandbox", nil},
	}
}

func TestRunKey_CannotChangeUnrelatedTasks(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	for _, target := range []string{rig.b, rig.b1} {
		for _, c := range taskWrites(target) {
			w := keyDo(t, rig.h, c.method, c.path, rig.keyA, c.body)
			if w.Code != http.StatusForbidden {
				t.Errorf("A's run key %s %s = %d, want 403: %s", c.method, c.path, w.Code, w.Body.String())
			}
		}
	}
	w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.b, rig.keyA, map[string]any{"title": "x"})
	if !strings.Contains(w.Body.String(), "its own task") {
		t.Errorf("refusal should explain the run's scope, got %s", w.Body.String())
	}
	got := getJSON(t, rig.h, "/tasks/"+rig.b, rig.token)
	if strings.Contains(got.Body.String(), "renamed by a run") {
		t.Error("B was renamed by A's run key")
	}
}

func TestRunKey_ChangesItsOwnGroup(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	cases := []struct {
		name, key, target string
	}{
		{"own task", rig.keyA, rig.a},
		{"parent run on its subtask", rig.keyA, rig.a1},
		{"subtask run on itself", rig.keyA1, rig.a1},
		{"subtask run on its parent", rig.keyA1, rig.a},
		{"subtask run on its sibling", rig.keyA1, rig.a2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+tc.target, tc.key,
				map[string]any{"title": "retitled " + tc.name})
			if w.Code != http.StatusOK {
				t.Fatalf("PATCH = %d, want 200: %s", w.Code, w.Body.String())
			}
			w = keyDo(t, rig.h, http.MethodPost, "/tasks/"+tc.target+"/comments", tc.key,
				map[string]any{"body": "progress from " + tc.name})
			if w.Code != http.StatusCreated && w.Code != http.StatusOK {
				t.Fatalf("comment = %d, want 2xx: %s", w.Code, w.Body.String())
			}
			w = keyDo(t, rig.h, http.MethodPut, "/tasks/"+tc.target+"/sections/goal", tc.key,
				map[string]any{"content": "- [ ] do it"})
			if w.Code != http.StatusOK {
				t.Fatalf("section = %d, want 200: %s", w.Code, w.Body.String())
			}
			w = keyDo(t, rig.h, http.MethodPost, "/tasks/"+tc.target+"/flags", tc.key,
				map[string]any{"label": "raised by " + tc.name})
			if w.Code != http.StatusCreated {
				t.Fatalf("raise flag = %d, want 201: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, c := range taskWrites(rig.a) {
		w := keyDo(t, rig.h, c.method, c.path, rig.keyA, c.body)
		if w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), errRunTaskScope) {
			t.Errorf("own task %s %s refused by the run scope: %s", c.method, c.path, w.Body.String())
		}
	}
}

func TestRunKey_OwnsTheTasksItCreates(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)

	w := keyDo(t, rig.h, http.MethodPost, "/tasks", rig.keyA, map[string]any{"title": "split off"})
	if w.Code != http.StatusCreated {
		t.Fatalf("run creating a task = %d: %s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.NewDecoder(w.Body).Decode(&created)
	c := created["id"].(string)
	if created["origin_task_id"] != rig.a {
		t.Fatalf("origin_task_id = %v, want %s", created["origin_task_id"], rig.a)
	}

	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+c, rig.keyA, map[string]any{"title": "refined"}); w.Code != http.StatusOK {
		t.Errorf("creator editing its task = %d, want 200: %s", w.Code, w.Body.String())
	}
	sub := keyDo(t, rig.h, http.MethodPost, "/tasks/"+c+"/subtasks", rig.keyA, map[string]any{"title": "slice"})
	if sub.Code != http.StatusCreated {
		t.Fatalf("creator carving a subtask = %d: %s", sub.Code, sub.Body.String())
	}
	var subBody map[string]any
	json.NewDecoder(sub.Body).Decode(&subBody)
	if subBody["origin_task_id"] != rig.a {
		t.Errorf("subtask origin_task_id = %v, want %s", subBody["origin_task_id"], rig.a)
	}
	next := rig.runKey(t, auth.RunScope{TaskID: rig.a, Agent: "reviewer", Stage: "review"})
	if w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+c+"/comments", next, map[string]any{"body": "ok"}); w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Errorf("later run on the creator's task commenting = %d, want 2xx: %s", w.Code, w.Body.String())
	}
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+c, rig.keyB1, map[string]any{"title": "hijack"}); w.Code != http.StatusForbidden {
		t.Errorf("unrelated run editing the created task = %d, want 403: %s", w.Code, w.Body.String())
	}

	w = postJSONAuth(t, rig.h, "/tasks", rig.token, map[string]any{"title": "human", "origin_task_id": rig.b1})
	var human map[string]any
	json.NewDecoder(w.Body).Decode(&human)
	if human["origin_task_id"] != nil {
		t.Errorf("a person's task got origin_task_id %v", human["origin_task_id"])
	}
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+human["id"].(string), rig.keyB1, map[string]any{"title": "x"}); w.Code != http.StatusForbidden {
		t.Errorf("forged origin let a run edit a person's task: %d", w.Code)
	}
}

func TestRunKey_CannotResolveFlags(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	w := keyDo(t, rig.h, http.MethodPost, "/tasks/"+rig.a+"/flags", rig.keyA, map[string]any{"label": "approve me"})
	if w.Code != http.StatusCreated {
		t.Fatalf("raise flag = %d: %s", w.Code, w.Body.String())
	}
	var f map[string]any
	json.NewDecoder(w.Body).Decode(&f)
	flagPath := "/tasks/" + rig.a + "/flags/" + f["id"].(string)

	if w := keyDo(t, rig.h, http.MethodPatch, flagPath, rig.keyA, map[string]any{"status": "approved"}); w.Code != http.StatusForbidden {
		t.Errorf("run approving its own flag = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := keyDo(t, rig.h, http.MethodDelete, flagPath, rig.keyA, nil); w.Code != http.StatusForbidden {
		t.Errorf("run deleting its own flag = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, rig.h, flagPath, rig.token, map[string]any{"status": "approved"}); w.Code != http.StatusOK {
		t.Errorf("person approving the flag = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestRunKey_AttachmentDeleteFollowsItsTask(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	upload := func(task string) string {
		w := postJSONAuth(t, rig.h, "/tasks/"+task+"/attachments", rig.token, map[string]any{
			"filename": "x.png", "mime_type": "image/png", "content_base64": png,
		})
		if w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("upload to %s = %d: %s", task, w.Code, w.Body.String())
		}
		var a map[string]any
		json.NewDecoder(w.Body).Decode(&a)
		return a["id"].(string)
	}
	onB, onA := upload(rig.b), upload(rig.a)

	if w := keyDo(t, rig.h, http.MethodDelete, "/task-attachments/"+onB, rig.keyA, nil); w.Code != http.StatusForbidden {
		t.Errorf("deleting B's attachment with A's key = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := keyDo(t, rig.h, http.MethodDelete, "/task-attachments/"+onA, rig.keyA, nil); w.Code != http.StatusNoContent {
		t.Errorf("deleting A's own attachment = %d, want 204: %s", w.Code, w.Body.String())
	}
	if w := getWithKey(t, rig.h, "/task-attachments/"+onB, rig.keyA); w.Code != http.StatusOK {
		t.Errorf("reading B's attachment with A's key = %d, want 200", w.Code)
	}
}

func TestRunKey_ReadsTheBoard(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	for _, path := range []string{
		"/tasks", "/tasks/" + rig.b, "/tasks/" + rig.b + "/sections",
		"/tasks/" + rig.b + "/flags", "/tasks/" + rig.b + "/dependencies",
		"/tasks/" + rig.b + "/events", "/tasks/" + rig.b + "/brief",
	} {
		if w := getWithKey(t, rig.h, path, rig.keyA); w.Code != http.StatusOK {
			t.Errorf("A's run key GET %s = %d, want 200: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestRunKey_TranscriptsFollowTheTaskRule(t *testing.T) {
	rig := newRunScopeRig(t)
	logDir := t.TempDir()
	seed := func(id, task string) {
		path := filepath.Join(logDir, id+".log")
		if err := os.WriteFile(path, []byte(`{"type":"result"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rig.srv.usageStore.Add(t.Context(), usage.Session{
			ID: id, TaskID: task, WorkspaceID: rig.ws, LogsFile: path,
		}); err != nil {
			t.Fatalf("seed session %s: %v", id, err)
		}
	}
	seed("run-on-b", rig.b)
	seed("run-on-a1", rig.a1)

	for _, p := range []string{"/usage/sessions/run-on-b/logs", "/usage/sessions/run-on-b/files", "/usage/sessions/run-on-b/tool-events"} {
		if w := getWithKey(t, rig.h, p, rig.keyA); w.Code != http.StatusForbidden {
			t.Errorf("A's run key GET %s = %d, want 403: %s", p, w.Code, w.Body.String())
		}
	}
	if w := getWithKey(t, rig.h, "/usage/sessions/run-on-a1/logs", rig.keyA); w.Code != http.StatusOK {
		t.Errorf("A's run reading its subtask's transcript = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := getJSON(t, rig.h, "/usage/sessions/run-on-b/logs", rig.token); w.Code != http.StatusOK {
		t.Errorf("person reading B's transcript = %d, want 200", w.Code)
	}

	ts := httptest.NewServer(rig.h)
	defer ts.Close()
	s := openEventStream(t, ts, rig.keyA, "")
	defer s.close()
	for _, topic := range []string{"logs:run-on-b", "chat"} {
		resp := postTopics(t, ts, rig.keyA, s.id, "subscribe", []string{topic})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("A's run subscribing to %s = %d, want 400", topic, resp.StatusCode)
		}
	}
	resp := postTopics(t, ts, rig.keyA, s.id, "subscribe", []string{"logs:run-on-a1", "tasks"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("A's run subscribing to its subtask's logs and the board = %d, want 200", resp.StatusCode)
	}
}

func TestRunKey_ChatReachesOnlyItsOwnChat(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	chatKey := rig.runKey(t, auth.RunScope{ChatID: "chat-mine", Agent: "assistant", Stage: "chat"})

	for _, c := range []scopedCall{
		{http.MethodGet, "/chats/chat-other", nil},
		{http.MethodGet, "/chats/chat-other/messages", nil},
		{http.MethodPost, "/chats/chat-other/messages", map[string]any{"text": "hi"}},
		{http.MethodPatch, "/chats/chat-other", map[string]any{"archived": true}},
		{http.MethodDelete, "/chats/chat-other", nil},
		{http.MethodPost, "/chats/chat-other/reset", nil},
		{http.MethodPost, "/chats", map[string]any{"agent_type": "assistant"}},
	} {
		if w := keyDo(t, rig.h, c.method, c.path, chatKey, c.body); w.Code != http.StatusForbidden {
			t.Errorf("chat key %s %s = %d, want 403: %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if w := getWithKey(t, rig.h, "/chats/chat-mine", chatKey); w.Code == http.StatusForbidden {
		t.Errorf("chat key on its own chat = 403: %s", w.Body.String())
	}
	if w := getWithKey(t, rig.h, "/chats/chat-mine", rig.keyA); w.Code != http.StatusForbidden {
		t.Errorf("task run key on a chat = %d, want 403", w.Code)
	}
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a, chatKey, map[string]any{"title": "x"}); w.Code != http.StatusForbidden {
		t.Errorf("chat key editing a task = %d, want 403", w.Code)
	}
}

func TestRunKey_ChatListIsFiltered(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	if err := rig.srv.chatSvc.Store().CreateChat(t.Context(), &chat.Chat{
		WorkspaceID: rig.ws, AgentType: "assistant", Title: "someone's chat", CreatedBy: "run-scope@test.com",
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if w := getJSON(t, rig.h, "/chats", rig.token); !strings.Contains(w.Body.String(), "someone's chat") {
		t.Fatalf("person's list is missing the chat: %s", w.Body.String())
	}
	chatKey := rig.runKey(t, auth.RunScope{ChatID: "chat-mine", Agent: "assistant", Stage: "chat"})
	w := getWithKey(t, rig.h, "/chats", chatKey)
	if w.Code != http.StatusOK {
		t.Fatalf("chat key listing chats = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "someone's chat") {
		t.Error("a run's chat list included somebody else's chat")
	}
}
