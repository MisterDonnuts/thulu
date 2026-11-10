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
	"github.com/MisterDonnuts/thulu/internal/chat"
)

type chatPrivacyRig struct {
	srv          *Server
	h            http.Handler
	ws           string
	ana, bob     string // tokens; bob is a workspace admin
	personal     string // ana's chat with no task
	onTask       string // ana's chat linked to a task: still hers
	mention      string // the chat the daemon answers mentions on that task from
	taskID       string
	anaEmail     string
	legacyOfAna  string // ana's chat from before owners were recorded
	legacyOfNone string // a chat from before authorship was recorded
}

func newChatPrivacyRig(t *testing.T) *chatPrivacyRig {
	t.Helper()
	srv, h := newIntegrationServer(t)
	rig := &chatPrivacyRig{srv: srv, h: h, anaEmail: "ana@test.com"}
	rig.ana, rig.ws = mintWorkspaceUserAs(t, srv, rig.anaEmail, "Ana", auth.RoleDeveloper)
	u, err := srv.authStore.CreateUser(t.Context(), "bob@test.com", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.authStore.AddMember(t.Context(), rig.ws, u.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	rig.bob, err = auth.IssueJWT(auth.Claims{UserID: u.ID, Email: u.Email, WorkspaceID: rig.ws, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}

	setupChatAgent(t, srv, "chatty")
	rig.personal = rig.open(t, "")
	rig.taskID = createTaskViaAPI(t, h, rig.ana, "Export invoices")
	rig.onTask = rig.open(t, rig.taskID)
	mention, err := srv.chatSvc.FindOrCreateForTask(t.Context(), rig.ws, "chatty", rig.taskID, "", "Export invoices")
	if err != nil {
		t.Fatal(err)
	}
	rig.mention = mention.ID

	for _, c := range []*chat.Chat{
		{WorkspaceID: rig.ws, AgentType: "chatty", Title: "legacy ana", CreatedBy: rig.anaEmail},
		{WorkspaceID: rig.ws, AgentType: "chatty", Title: "legacy none"},
	} {
		if err := srv.chatSvc.Store().CreateChat(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		if c.CreatedBy != "" {
			rig.legacyOfAna = c.ID
		} else {
			rig.legacyOfNone = c.ID
		}
	}
	return rig
}

func (rig *chatPrivacyRig) open(t *testing.T, taskID string) string {
	t.Helper()
	w := postJSONAuth(t, rig.h, "/chats", rig.ana, map[string]string{"agent_type": "chatty", "task_id": taskID})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /chats task=%q = %d: %s", taskID, w.Code, w.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	json.NewDecoder(w.Body).Decode(&c)
	return c.ID
}

func (rig *chatPrivacyRig) listed(t *testing.T, token string) map[string]bool {
	t.Helper()
	w := getJSON(t, rig.h, "/chats", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /chats = %d: %s", w.Code, w.Body.String())
	}
	var chats []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(w.Body).Decode(&chats)
	out := map[string]bool{}
	for _, c := range chats {
		out[c.ID] = true
	}
	return out
}

func TestChats_PersonalChatIsOnlyItsCreators(t *testing.T) {
	t.Parallel()
	rig := newChatPrivacyRig(t)

	ana := rig.listed(t, rig.ana)
	for _, id := range []string{rig.personal, rig.onTask, rig.legacyOfAna, rig.legacyOfNone} {
		if !ana[id] {
			t.Errorf("creator's list is missing %s: %v", id, ana)
		}
	}
	bob := rig.listed(t, rig.bob)
	if bob[rig.personal] || bob[rig.onTask] || bob[rig.legacyOfAna] {
		t.Errorf("an admin's list shows ana's personal chats: %v", bob)
	}
	if !bob[rig.legacyOfNone] {
		t.Errorf("an admin's list is missing the unowned chat: %v", bob)
	}
	if ana[rig.mention] || bob[rig.mention] {
		t.Errorf("the mention chat is listed; it lives in its task: ana %v, bob %v", ana, bob)
	}

	for _, id := range []string{rig.personal, rig.onTask, rig.legacyOfAna} {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/chats/" + id, nil},
			{http.MethodGet, "/chats/" + id + "/messages", nil},
			{http.MethodPost, "/chats/" + id + "/messages", map[string]string{"text": "hi"}},
			{http.MethodPatch, "/chats/" + id, map[string]any{"title": "mine now"}},
			{http.MethodPost, "/chats/" + id + "/reset", nil},
			{http.MethodGet, "/chats/" + id + "/actions", nil},
			{http.MethodDelete, "/chats/" + id, nil},
		} {
			if w := tokenDo(t, rig.h, c.method, c.path, rig.bob, c.body); w.Code != http.StatusNotFound {
				t.Errorf("admin %s %s = %d, want 404: %s", c.method, c.path, w.Code, w.Body.String())
			}
		}
		if w := getJSON(t, rig.h, "/chats/"+id, rig.ana); w.Code != http.StatusOK {
			t.Errorf("creator GET %s = %d: %s", id, w.Code, w.Body.String())
		}
	}
	if w := getJSON(t, rig.h, "/chats/"+rig.mention+"/messages", rig.bob); w.Code != http.StatusOK {
		t.Errorf("admin reading the mention chat = %d: %s", w.Code, w.Body.String())
	}
}

func TestChats_OpeningAChatOnATaskNeverLandsInSomeoneElses(t *testing.T) {
	t.Parallel()
	rig := newChatPrivacyRig(t)
	open := func(token string) (id string, reused bool) {
		w := postJSONAuth(t, rig.h, "/chats", token, map[string]string{"agent_type": "chatty", "task_id": rig.taskID})
		var got struct {
			ID     string `json:"id"`
			Reused bool   `json:"reused"`
		}
		json.NewDecoder(w.Body).Decode(&got)
		return got.ID, got.Reused
	}
	if id, reused := open(rig.ana); !reused || id != rig.onTask {
		t.Errorf("ana reopening their task chat = %s reused=%v, want %s", id, reused, rig.onTask)
	}
	if id, reused := open(rig.bob); reused || id == rig.onTask || id == rig.mention {
		t.Errorf("admin opening a chat on the task = %s reused=%v, want a new chat of their own", id, reused)
	}
}

// The UI reads chat events from the multiplexed hub; /chats/events is the older single-topic feed.
// Both must deliver only what the reader's list shows.
func TestChats_EventsOfAPersonalChatReachOnlyItsCreator(t *testing.T) {
	t.Parallel()
	rig := newChatPrivacyRig(t)
	ts := httptest.NewServer(rig.h)
	defer ts.Close()

	store := rig.srv.chatSvc.Store()
	chatOf := func(id string) *chat.Chat {
		c, err := store.GetChat(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// The unowned chat goes last: it is the first one an admin may receive.
	publish := func() {
		for _, id := range []string{rig.personal, rig.onTask, rig.mention, rig.legacyOfNone} {
			rig.srv.chatBus.Publish(chat.Event{Type: chat.EventTurnStarted, TurnID: id}.For(chatOf(id)))
		}
	}
	chatIDOf := func(raw []byte) string {
		var ev chat.Event
		json.Unmarshal(raw, &ev)
		return ev.ChatID
	}

	viaHub := func(token string) string {
		s := openEventStream(t, ts, token, "chat")
		defer s.close()
		publish()
		name, data := s.next(3 * time.Second)
		if name != "msg" {
			t.Fatalf("hub event = %q, want msg", name)
		}
		return chatIDOf(decodeFrame(t, data).Data)
	}
	viaLegacy := func(token string) string {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/chats/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := ts.Client().Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("open /chats/events: %v %v", resp, err)
		}
		defer resp.Body.Close()
		rd := bufio.NewReader(resp.Body)
		if line, _ := rd.ReadString('\n'); !strings.HasPrefix(line, ": connected") {
			t.Fatalf("expected the connected comment, got %q", line)
		}
		publish()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE: %v", err)
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				return chatIDOf([]byte(data))
			}
		}
		return ""
	}

	for name, read := range map[string]func(string) string{"hub": viaHub, "/chats/events": viaLegacy} {
		if got := read(rig.bob); got != rig.legacyOfNone {
			t.Errorf("%s: admin's first event is for %q, want the unowned chat %s", name, got, rig.legacyOfNone)
		}
		if got := read(rig.ana); got != rig.personal {
			t.Errorf("%s: creator's first event is for %q, want the creator's chat %s", name, got, rig.personal)
		}
	}
}
