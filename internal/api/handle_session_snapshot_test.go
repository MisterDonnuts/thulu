package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

func rawDo(t *testing.T, h http.Handler, method, path, key, bearer string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/gzip")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func snapshotPath(id, sid string) string {
	return "/tasks/" + id + "/session-snapshot?sid=" + sid
}

func TestSessionSnapshot_RoundTrip(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	if w := rawDo(t, rig.h, http.MethodPut, snapshotPath(rig.a, "sess-1"), rig.keyA, "", []byte("archive-1")); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	if w := rawDo(t, rig.h, http.MethodPut, snapshotPath(rig.a, "sess-2"), rig.keyA, "", []byte("archive-2")); w.Code != http.StatusOK {
		t.Fatalf("PUT 2 = %d: %s", w.Code, w.Body.String())
	}
	w := rawDo(t, rig.h, http.MethodGet, snapshotPath(rig.a, "sess-2"), rig.keyA, "", nil)
	if w.Code != http.StatusOK || w.Body.String() != "archive-2" {
		t.Fatalf("GET = %d %q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if w := rawDo(t, rig.h, http.MethodGet, snapshotPath(rig.a, "sess-1"), rig.keyA, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("GET of the replaced session = %d, want 404 (the entrypoint rehydrates)", w.Code)
	}

	file := filepath.Join(task.SessionsRootBeside(rig.srv.runsDir), rig.a, rig.a, "developer", "sess-2.tar.gz")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stored file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}

	if w := rawDo(t, rig.h, http.MethodPut, snapshotPath(rig.a1, "sess-3"), rig.keyA1, "", []byte("sub")); w.Code != http.StatusOK {
		t.Fatalf("subtask PUT = %d: %s", w.Code, w.Body.String())
	}
	if w := rawDo(t, rig.h, http.MethodDelete, "/tasks/"+rig.a1, "", rig.token, nil); w.Code >= 300 {
		t.Fatalf("DELETE subtask = %d: %s", w.Code, w.Body.String())
	}
	root := task.SessionsRootBeside(rig.srv.runsDir)
	if _, err := os.Stat(filepath.Join(root, rig.a, rig.a1)); !os.IsNotExist(err) {
		t.Errorf("the subtask's snapshots survived its deletion: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("deleting a subtask took the parent's snapshot: %v", err)
	}
}

func TestSessionSnapshot_AuthMatrix(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	if w := rawDo(t, rig.h, http.MethodPut, snapshotPath(rig.a1, "sess-1"), rig.keyA1, "", []byte("x")); w.Code != http.StatusOK {
		t.Fatalf("own PUT = %d: %s", w.Code, w.Body.String())
	}
	otherAgentOnA1 := rig.runKey(t, auth.RunScope{TaskID: rig.a1, Agent: "reviewer", Stage: "in_progress"})
	chatKey := rig.runKey(t, auth.RunScope{ChatID: "chat-1", Agent: "tester", Stage: "chat"})

	cases := []struct {
		name, method, path, key, bearer string
		want                            int
	}{
		{"person PUT", http.MethodPut, snapshotPath(rig.a1, "sess-1"), "", rig.token, http.StatusForbidden},
		{"person GET", http.MethodGet, snapshotPath(rig.a1, "sess-1"), "", rig.token, http.StatusForbidden},
		{"another task's run GET", http.MethodGet, snapshotPath(rig.a1, "sess-1"), rig.keyB1, "", http.StatusForbidden},
		{"another task's run PUT", http.MethodPut, snapshotPath(rig.a1, "sess-9"), rig.keyB1, "", http.StatusForbidden},
		{"sibling subtask's run", http.MethodGet, snapshotPath(rig.a2, "sess-1"), rig.keyA1, "", http.StatusForbidden},
		{"another agent on the task", http.MethodGet, snapshotPath(rig.a1, "sess-1"), otherAgentOnA1, "", http.StatusNotFound},
		{"chat run", http.MethodGet, snapshotPath(rig.a1, "sess-1"), chatKey, "", http.StatusForbidden},
		{"parent's run PUT on a subtask", http.MethodPut, snapshotPath(rig.a1, "sess-1"), rig.keyA, "", http.StatusForbidden},
		{"own GET", http.MethodGet, snapshotPath(rig.a1, "sess-1"), rig.keyA1, "", http.StatusOK},
	}
	for _, c := range cases {
		w := rawDo(t, rig.h, c.method, c.path, c.key, c.bearer, []byte("y"))
		if w.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d: %s", c.name, c.method, c.path, w.Code, c.want, w.Body.String())
		}
	}
	if w := rawDo(t, rig.h, http.MethodGet, snapshotPath(rig.a1, "sess-1"), rig.keyA1, "", nil); w.Body.String() != "x" {
		t.Errorf("the tester's snapshot changed: %q", w.Body.String())
	}
}

func TestSessionSnapshot_SizeCapAndBadSid(t *testing.T) {
	rig := newRunScopeRig(t)
	t.Setenv(sessionSnapshotMaxEnv, "8")
	if w := rawDo(t, rig.h, http.MethodPut, snapshotPath(rig.a, "sess-1"), rig.keyA, "", []byte("123456789")); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("over the cap = %d, want 413: %s", w.Code, w.Body.String())
	}
	if w := rawDo(t, rig.h, http.MethodPut, snapshotPath(rig.a, "sess-1"), rig.keyA, "", []byte("12345678")); w.Code != http.StatusOK {
		t.Errorf("at the cap = %d, want 200: %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest(http.MethodPut, snapshotPath(rig.a, "sess-2"), io.MultiReader(strings.NewReader("12345"), strings.NewReader("67890")))
	req.ContentLength = -1
	req.Header.Set("X-API-Key", rig.keyA)
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("streamed over the cap = %d, want 413: %s", w.Code, w.Body.String())
	}
	if w := rawDo(t, rig.h, http.MethodGet, snapshotPath(rig.a, "sess-1"), rig.keyA, "", nil); w.Body.String() != "12345678" {
		t.Errorf("a refused upload touched the stored snapshot: %d %q", w.Code, w.Body.String())
	}

	for _, sid := range []string{"", "..", "..%2F..%2Fetc", ".hidden", "a%2Fb", strings.Repeat("x", 200)} {
		if w := rawDo(t, rig.h, http.MethodPut, snapshotPath(rig.a, sid), rig.keyA, "", []byte("x")); w.Code != http.StatusBadRequest {
			t.Errorf("PUT sid=%q = %d, want 400", sid, w.Code)
		}
		if w := rawDo(t, rig.h, http.MethodGet, snapshotPath(rig.a, sid), rig.keyA, "", nil); w.Code != http.StatusBadRequest {
			t.Errorf("GET sid=%q = %d, want 400", sid, w.Code)
		}
	}
}

func TestConversationTranscript(t *testing.T) {
	rig := newRunScopeRig(t)
	logsDir := filepath.Join(rig.srv.usageStore.DataDir(), "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := func(id, agent, log, file string) {
		path := filepath.Join(logsDir, file)
		if !filepath.IsAbs(file) {
			if err := os.WriteFile(path, []byte(log), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			path = file
		}
		if err := rig.srv.usageStore.Add(t.Context(), usage.Session{
			ID: id, TaskID: rig.a, WorkspaceID: rig.ws, AgentType: agent, LogsFile: path,
			Stage: "in_progress", StartedAt: time.Now().Add(-time.Hour),
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("run-dev", "developer", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"make test"}}]}}
{"type":"result","subtype":"success","result":"Need the DB name.","session_id":"s1"}
`, "run-dev.log")
	seed("run-rev", "reviewer", `{"type":"result","subtype":"success","result":"REVIEWER SECRET","session_id":"s2"}
`, "run-rev.log")
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte(`{"type":"result","subtype":"success","result":"OUTSIDE","session_id":"s3"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seed("run-out", "developer", "", outside)

	path := "/tasks/" + rig.a + "/conversation-transcript"
	w := getWithKey(t, rig.h, path, rig.keyA)
	if w.Code != http.StatusOK {
		t.Fatalf("run GET = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "make test") || !strings.Contains(body, "Need the DB name.") {
		t.Errorf("transcript lacks the developer's run:\n%s", body)
	}
	if strings.Contains(body, "REVIEWER SECRET") || strings.Contains(body, "OUTSIDE") {
		t.Errorf("transcript leaked another agent's run or an outside file:\n%s", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q", ct)
	}

	if w := getWithKey(t, rig.h, path+"?agent=reviewer", rig.keyA); w.Code != http.StatusForbidden {
		t.Errorf("run naming another agent = %d, want 403", w.Code)
	}
	if w := getWithKey(t, rig.h, path, rig.keyB1); w.Code != http.StatusForbidden {
		t.Errorf("another task's run = %d, want 403", w.Code)
	}
	if w := getJSON(t, rig.h, path+"?agent=reviewer", rig.token); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "REVIEWER SECRET") {
		t.Errorf("person GET reviewer = %d: %s", w.Code, w.Body.String())
	}
	if w := getJSON(t, rig.h, path, rig.token); w.Code != http.StatusBadRequest {
		t.Errorf("person without agent = %d, want 400", w.Code)
	}
	if w := getJSON(t, rig.h, path+"?agent=nobody", rig.token); w.Code != http.StatusNotFound {
		t.Errorf("agent with nothing = %d, want 404", w.Code)
	}

	narrow := mintKey(t, rig.srv, rig.ws, auth.RoleDeveloper, auth.PermTasksRead)
	if w := getWithKey(t, rig.h, path+"?agent=developer", narrow); w.Code != http.StatusForbidden {
		t.Errorf("key without runs:read = %d, want 403", w.Code)
	}
	wide := mintKey(t, rig.srv, rig.ws, auth.RoleDeveloper, auth.PermTasksRead, auth.PermRunsRead)
	if w := getWithKey(t, rig.h, path+"?agent=developer", wide); w.Code != http.StatusOK {
		t.Errorf("key with runs:read = %d: %s", w.Code, w.Body.String())
	}

	t.Setenv(transcriptMaxEnv, "300")
	if w := getWithKey(t, rig.h, path, rig.keyA); w.Code != http.StatusOK || len(w.Body.String()) > 300 {
		t.Errorf("capped transcript = %d, %d bytes", w.Code, len(w.Body.String()))
	}
}
