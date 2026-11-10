package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/eventhub"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type sseEvent struct {
	name string
	data []byte
}

type sseStream struct {
	t      *testing.T
	resp   *http.Response
	events chan sseEvent
	id     string
}

func openEventStream(t *testing.T, ts *httptest.Server, token, topics string) *sseStream {
	t.Helper()
	return openEventStreamIn(t, ts, token, topics, "")
}

func openEventStreamIn(t *testing.T, ts *httptest.Server, token, topics, lang string) *sseStream {
	t.Helper()
	url := ts.URL + "/events"
	if topics != "" {
		url += "?topics=" + topics
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open /events: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("/events status = %d", resp.StatusCode)
	}
	s := &sseStream{t: t, resp: resp, events: make(chan sseEvent, 4096)}
	go s.pump()
	name, data := s.next(3 * time.Second)
	if name != "hello" {
		t.Fatalf("first event = %q, want hello", name)
	}
	var hello struct {
		StreamID string `json:"stream_id"`
		Err      string `json:"error"`
	}
	if err := json.Unmarshal(data, &hello); err != nil {
		t.Fatalf("decode hello frame %q: %v", data, err)
	}
	if hello.Err != "" {
		t.Fatalf("hello frame reported %q", hello.Err)
	}
	if hello.StreamID == "" {
		t.Fatal("hello frame carried no stream_id")
	}
	s.id = hello.StreamID
	return s
}

func (s *sseStream) pump() {
	defer close(s.events)
	rd := bufio.NewReader(s.resp.Body)
	var name string
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			s.events <- sseEvent{name: name, data: []byte(strings.TrimPrefix(line, "data: "))}
			name = ""
		}
	}
}

func (s *sseStream) next(within time.Duration) (name string, data []byte) {
	s.t.Helper()
	name, data, ok := s.read(within)
	if !ok {
		s.t.Fatalf("no event within %s", within)
	}
	return name, data
}

func (s *sseStream) read(within time.Duration) (name string, data []byte, ok bool) {
	s.t.Helper()
	select {
	case ev, more := <-s.events:
		if !more {
			return "", nil, false
		}
		return ev.name, ev.data, true
	case <-time.After(within):
		return "", nil, false
	}
}

func (s *sseStream) close() { s.resp.Body.Close() }

func decodeFrame(t *testing.T, data []byte) eventhub.Frame {
	t.Helper()
	var f eventhub.Frame
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decode frame %q: %v", data, err)
	}
	return f
}

func postTopics(t *testing.T, ts *httptest.Server, token, streamID, action string, topics []string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string][]string{"topics": topics})
	url := fmt.Sprintf("%s/events/%s/%s", ts.URL, streamID, action)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return resp
}

func TestIntegration_Events_ChatMessageInTheReadersLanguage(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "evlang@test.com", "EvLang")

	ts := httptest.NewServer(h)
	defer ts.Close()

	es := openEventStreamIn(t, ts, token, "chat", "es")
	defer es.close()
	en := openEventStreamIn(t, ts, token, "chat", "en")
	defer en.close()

	failed := &chat.Message{ID: "msg-1", ChatID: "chat-lang", Role: chat.RoleAssistant, Text: "**Agent error:** boom"}
	failed.SetTexts(map[string]string{"en": "**Agent error:** boom", "es": "**Error del agente:** explotó"})
	srv.chatBus.Publish(chat.Event{Type: chat.EventMessageAdded, Message: failed}.
		For(&chat.Chat{ID: "chat-lang", WorkspaceID: wsID}))

	for _, c := range []struct {
		s    *sseStream
		want string
	}{{es, "**Error del agente:** explotó"}, {en, "**Agent error:** boom"}} {
		name, data := c.s.next(3 * time.Second)
		if name != "msg" {
			t.Fatalf("got event %q, want msg", name)
		}
		var ev chat.Event
		if err := json.Unmarshal(decodeFrame(t, data).Data, &ev); err != nil {
			t.Fatalf("decode chat event: %v", err)
		}
		if ev.Message == nil || ev.Message.Text != c.want {
			t.Errorf("message = %+v, want text %q", ev.Message, c.want)
		}
	}
	if failed.Text != "**Agent error:** boom" {
		t.Errorf("the published message was rewritten in place: %q", failed.Text)
	}
}

func TestIntegration_Events_MultiplexesChatAndLogs(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "mux@test.com", "Mux")

	runID := "container-mux-1"
	writeSessionLog(t, srv, runID, wsID, "line one\nline two\n")

	ts := httptest.NewServer(h)
	defer ts.Close()

	s := openEventStream(t, ts, token, "")
	defer s.close()

	if resp := postTopics(t, ts, token, s.id, "subscribe", []string{"chat", "logs:" + runID}); resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe = %d", resp.StatusCode)
	}
	defer func() { postTopics(t, ts, token, s.id, "unsubscribe", []string{"chat", "logs:" + runID}).Body.Close() }()

	srv.chatBus.Publish(chat.Event{Type: chat.EventTurnStarted, TurnID: "trn-1"}.
		For(&chat.Chat{ID: "chat-mux", WorkspaceID: wsID}))

	sawChat, logLines := false, []string{}
	deadline := time.Now().Add(5 * time.Second)
	for (!sawChat || len(logLines) < 2) && time.Now().Before(deadline) {
		name, data, ok := s.read(time.Second)
		if !ok {
			continue
		}
		if name != "msg" {
			continue
		}
		f := decodeFrame(t, data)
		switch {
		case f.Topic == "chat" && f.Type == string(chat.EventTurnStarted):
			sawChat = true
		case f.Topic == "logs:"+runID && f.Type == "line":
			var line string
			if err := json.Unmarshal(f.Data, &line); err != nil {
				t.Fatalf("log line payload %q: %v", f.Data, err)
			}
			logLines = append(logLines, line)
		}
	}
	if !sawChat {
		t.Error("chat event never arrived on the multiplexed stream")
	}
	if len(logLines) < 2 || logLines[0] != "line one" || logLines[1] != "line two" {
		t.Errorf("log lines = %v, want [line one line two]", logLines)
	}
}

func TestIntegration_Events_ChatIsWorkspaceFiltered(t *testing.T) {
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "ev-a@test.com", "EvA")
	tokenB, _ := mintWorkspaceUser(t, srv, "ev-b@test.com", "EvB")

	ts := httptest.NewServer(h)
	defer ts.Close()

	a := openEventStream(t, ts, tokenA, "chat")
	defer a.close()
	b := openEventStream(t, ts, tokenB, "chat")
	defer b.close()

	srv.chatBus.Publish(chat.Event{Type: chat.EventTurnStarted, TurnID: "trn-1"}.
		For(&chat.Chat{ID: "chat-x", WorkspaceID: wsA}))

	name, data := a.next(3 * time.Second)
	if name != "msg" {
		t.Fatalf("A got event %q, want msg", name)
	}
	if f := decodeFrame(t, data); f.Type != string(chat.EventTurnStarted) {
		t.Errorf("A got %+v, want turn_started", f)
	}

	if name, data, ok := b.read(500 * time.Millisecond); ok {
		t.Errorf("B received %s %s from another workspace", name, data)
	}
}

func TestIntegration_Events_StreamIsOwnerBound(t *testing.T) {
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "own-a@test.com", "OwnA")
	tokenB, _ := mintWorkspaceUser(t, srv, "own-b@test.com", "OwnB")

	ts := httptest.NewServer(h)
	defer ts.Close()

	a := openEventStream(t, ts, tokenA, "")
	defer a.close()

	resp := postTopics(t, ts, tokenB, a.id, "subscribe", []string{"chat"})
	defer resp.Body.Close()
	// 404, not 403: a foreign stream must look like a missing one so ids can't be probed.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign subscribe = %d, want 404", resp.StatusCode)
	}
	if resp := postTopics(t, ts, tokenA, "does-not-exist", "subscribe", []string{"chat"}); resp.StatusCode != http.StatusNotFound {
		resp.Body.Close()
		t.Errorf("unknown stream = %d, want 404", resp.StatusCode)
	}
}

func TestIntegration_Events_UnknownTopicRejected(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "topic@test.com", "Topic")

	ts := httptest.NewServer(h)
	defer ts.Close()

	s := openEventStream(t, ts, token, "")
	defer s.close()

	resp := postTopics(t, ts, token, s.id, "subscribe", []string{"chat", "definitely-not-a-topic"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown topic = %d, want 400", resp.StatusCode)
	}
	sub := postTopics(t, ts, token, s.id, "unsubscribe", nil)
	defer sub.Body.Close()
	var body struct {
		Topics []string `json:"topics"`
	}
	json.NewDecoder(sub.Body).Decode(&body)
	if len(body.Topics) != 0 {
		t.Errorf("topics after a rejected batch = %v, want none", body.Topics)
	}
}

func TestIntegration_Events_UnsubscribeReleasesSource(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "rel@test.com", "Rel")
	runID := "container-rel-1"
	writeSessionLog(t, srv, runID, wsID, "hello\n")

	ts := httptest.NewServer(h)
	defer ts.Close()

	s := openEventStream(t, ts, token, "logs:"+runID)
	defer s.close()

	name, data := s.next(3 * time.Second)
	if name != "msg" || decodeFrame(t, data).Type != "line" {
		t.Fatalf("unexpected first frame %s %s", name, data)
	}
	if got := srv.hub.Stats().Sources; got != 1 {
		t.Fatalf("sources = %d, want 1", got)
	}

	postTopics(t, ts, token, s.id, "unsubscribe", []string{"logs:" + runID}).Body.Close()
	waitFor(t, 2*time.Second, func() bool { return srv.hub.Stats().Sources == 0 })
}

func TestIntegration_Events_DisconnectCleansUp(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "clean@test.com", "Clean")
	runID := "container-clean-1"
	writeSessionLog(t, srv, runID, wsID, "hello\n")

	ts := httptest.NewServer(h)
	defer ts.Close()

	s := openEventStream(t, ts, token, "chat,logs:"+runID)
	s.next(3 * time.Second)
	waitFor(t, 2*time.Second, func() bool { return srv.hub.Stats().Streams == 1 })

	s.close()
	waitFor(t, 3*time.Second, func() bool {
		st := srv.hub.Stats()
		return st.Streams == 0 && st.Sources == 0
	})
}

func TestIntegration_Events_SameRunSharesOneSource(t *testing.T) {
	srv, h := newIntegrationServer(t)
	tokenA, wsID := mintWorkspaceUser(t, srv, "share-a@test.com", "ShareA")
	tokenB := mintWorkspacePeer(t, srv, "share-b@test.com", "ShareB", wsID)
	runID := "container-share-1"
	writeSessionLog(t, srv, runID, wsID, "shared line\n")

	ts := httptest.NewServer(h)
	defer ts.Close()

	a := openEventStream(t, ts, tokenA, "logs:"+runID)
	defer a.close()
	b := openEventStream(t, ts, tokenB, "logs:"+runID)
	defer b.close()

	for _, s := range []*sseStream{a, b} {
		name, data := s.next(3 * time.Second)
		if name != "msg" || decodeFrame(t, data).Type != "line" {
			t.Fatalf("unexpected frame %s %s", name, data)
		}
	}
	if got := srv.hub.Stats(); got.Sources != 1 || got.Subscriptions != 2 {
		t.Errorf("stats = %+v, want 1 source with 2 subscriptions", got)
	}
}

func TestIntegration_Events_TaskChangesArePushed(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "taskpush@test.com", "TaskPush")

	ts := httptest.NewServer(h)
	defer ts.Close()

	s := openEventStream(t, ts, token, "tasks")
	defer s.close()

	w := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "pushed task"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.NewDecoder(w.Body).Decode(&created)

	name, data := s.next(3 * time.Second)
	if name != "msg" {
		t.Fatalf("got %q, want msg", name)
	}
	f := decodeFrame(t, data)
	if f.Topic != "tasks" || f.Type != string(task.ChangeCreated) {
		t.Fatalf("frame = %+v, want a tasks/%s frame", f, task.ChangeCreated)
	}
	var change task.Change
	if err := json.Unmarshal(f.Data, &change); err != nil {
		t.Fatalf("decode change %q: %v", f.Data, err)
	}
	if change.TaskID != created.ID {
		t.Errorf("change.task_id = %q, want %q", change.TaskID, created.ID)
	}

	pw := patchJSONAuth(t, h, "/tasks/"+created.ID, token, map[string]any{"stage": "refinement"})
	if pw.Code != http.StatusOK {
		t.Fatalf("patch task = %d: %s", pw.Code, pw.Body.String())
	}
	name, data = s.next(3 * time.Second)
	if got := decodeFrame(t, data); name != "msg" || got.Type != string(task.ChangeUpdated) {
		t.Errorf("frame = %s %+v, want %s", name, got, task.ChangeUpdated)
	}
}

func TestIntegration_Events_TaskChangesAreWorkspaceFiltered(t *testing.T) {
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "tp-a@test.com", "TpA")
	tokenB, _ := mintWorkspaceUser(t, srv, "tp-b@test.com", "TpB")

	ts := httptest.NewServer(h)
	defer ts.Close()

	b := openEventStream(t, ts, tokenB, "tasks")
	defer b.close()

	if w := postJSONAuth(t, h, "/tasks", tokenA, map[string]any{"title": "A's task"}); w.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", w.Code, w.Body.String())
	}
	if name, data, ok := b.read(700 * time.Millisecond); ok {
		t.Errorf("B saw %s %s from another workspace", name, data)
	}
}

func TestIntegration_Events_MissingLogsReportError(t *testing.T) {
	restore := logsWaitTimeout
	logsWaitTimeout, logsWaitPoll = 200*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { logsWaitTimeout, logsWaitPoll = restore, 2*time.Second })

	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "nolog@test.com", "NoLog")

	runID := "container-nolog-1"
	now := time.Now().UTC()
	if err := srv.usageStore.Add(t.Context(), usage.Session{
		ID: runID, Agent: "agent", WorkspaceID: wsID, StartedAt: now, FinishedAt: now,
	}); err != nil {
		t.Fatalf("record session: %v", err)
	}

	ts := httptest.NewServer(h)
	defer ts.Close()

	s := openEventStream(t, ts, token, "logs:"+runID)
	defer s.close()

	types := []string{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		name, data, ok := s.read(time.Second)
		if !ok || name != "msg" {
			continue
		}
		f := decodeFrame(t, data)
		types = append(types, f.Type)
		if f.Type == eventhub.TypeError {
			break
		}
	}
	if len(types) == 0 || types[0] != typeWaiting {
		t.Errorf("frames = %v, want %s first", types, typeWaiting)
	}
	if len(types) == 0 || types[len(types)-1] != eventhub.TypeError {
		t.Errorf("frames = %v, want an %s frame at the end", types, eventhub.TypeError)
	}
}

func TestIntegration_Events_LegacyLogStreamStillServes(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wsID := mintWorkspaceUser(t, srv, "legacy@test.com", "Legacy")
	runID := "container-legacy-1"
	writeSessionLog(t, srv, runID, wsID, "legacy line\n")

	ts := httptest.NewServer(h)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/usage/sessions/"+runID+"/logs/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("open legacy stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy stream = %d", resp.StatusCode)
	}
	rd := bufio.NewReader(resp.Body)
	line, err := rd.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: legacy line") {
		t.Fatalf("legacy stream first line = %q (%v)", line, err)
	}
}

func TestIntegration_Events_LegacyMissingLogsIs404(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "legacy404@test.com", "Legacy404")
	if w := getJSON(t, h, "/usage/sessions/nope/logs/stream", token); w.Code != http.StatusNotFound {
		t.Errorf("missing run = %d, want 404", w.Code)
	}
	_ = srv
}

func writeSessionLog(t *testing.T, srv *Server, runID, wid, content string) {
	t.Helper()
	dir := filepath.Join(srv.usageStore.DataDir(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent_"+runID+".log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := srv.usageStore.Add(t.Context(), usage.Session{
		ID: runID, Agent: "agent", AgentType: "agent", WorkspaceID: wid,
		StartedAt: now, FinishedAt: now, LogsFile: path,
	}); err != nil {
		t.Fatalf("record session for %s: %v", runID, err)
	}
}

func mintWorkspacePeer(t *testing.T, srv *Server, email, name, wsID string) string {
	t.Helper()
	u, err := srv.authStore.CreateUser(t.Context(), email, name)
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	token, err := auth.IssueJWT(auth.Claims{
		UserID: u.ID, Email: u.Email, WorkspaceID: wsID, Role: auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("issue jwt for %s: %v", email, err)
	}
	return token
}

func waitFor(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition still false after %s", within)
}

// Workspace keys carry no run scope; an owner built from run scope alone made every such key, in
// every tenant, the same owner.
func TestIntegration_Events_StreamIsBoundToTheKeyThatOpenedIt(t *testing.T) {
	srv, h := newIntegrationServer(t)
	_, wsA := mintWorkspaceUser(t, srv, "own-key-a@test.com", "OwnKeyA")
	_, wsB := mintWorkspaceUser(t, srv, "own-key-b@test.com", "OwnKeyB")
	keyA := mintKey(t, srv, wsA, auth.RoleViewer, auth.PermAll)
	keyA2 := mintKey(t, srv, wsA, auth.RoleViewer, auth.PermAll)
	keyB := mintKey(t, srv, wsB, auth.RoleViewer, auth.PermAll)

	ts := httptest.NewServer(h)
	defer ts.Close()
	a := openEventStream(t, ts, keyA, "")
	defer a.close()

	for name, key := range map[string]string{"another tenant's key": keyB, "another key of the workspace": keyA2} {
		resp := postTopics(t, ts, key, a.id, "subscribe", []string{"chat"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("subscribe with %s = %d, want 404", name, resp.StatusCode)
		}
	}
	resp := postTopics(t, ts, keyA, a.id, "subscribe", []string{"chat"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("subscribe with the opening key = %d, want 200", resp.StatusCode)
	}
}
