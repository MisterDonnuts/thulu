package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/chat"
)

func TestChats_TaskCanBeLinkedChangedAndRemoved(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "relink@test.com", "RL", "SecurePassword1")
	setupChatAgent(t, srv, "chatty")
	first := createTaskViaAPI(t, h, token, "Export invoices")
	second := createTaskViaAPI(t, h, token, "Fix the cart")

	w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "chatty"})
	var c struct {
		ID        string `json:"id"`
		TaskID    string `json:"task_id"`
		TaskTitle string `json:"task_title"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("POST /chats = %d: %s", w.Code, w.Body.String())
	}

	link := func(taskID string) (int, string, string) {
		w := patchJSONAuth(t, h, "/chats/"+c.ID, token, map[string]string{"task_id": taskID})
		var v struct {
			TaskID    string `json:"task_id"`
			TaskTitle string `json:"task_title"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v.TaskID, v.TaskTitle
	}
	if code, id, title := link(first); code != http.StatusOK || id != first || title != "Export invoices" {
		t.Fatalf("link = %d %q %q", code, id, title)
	}
	if code, id, title := link(second); code != http.StatusOK || id != second || title != "Fix the cart" {
		t.Fatalf("change = %d %q %q", code, id, title)
	}
	if code, _, _ := link("0000000000000000"); code != http.StatusNotFound {
		t.Fatalf("link to a missing task = %d, want 404", code)
	}
	if code, id, _ := link(""); code != http.StatusOK || id != "" {
		t.Fatalf("unlink = %d %q", code, id)
	}

	// The daemon's own chats keep their task.
	got, err := srv.chatSvc.Store().GetChat(t.Context(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	sys, err := srv.chatSvc.FindOrCreateForTask(t.Context(), got.WorkspaceID, "chatty", first, "", "Export invoices")
	if err != nil {
		t.Fatal(err)
	}
	if w := patchJSONAuth(t, h, "/chats/"+sys.ID, token, map[string]string{"task_id": second}); w.Code != http.StatusConflict {
		t.Errorf("relinking a mention chat = %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestChats_TranscriptOfATurn(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "transcript@test.com", "TR", "SecurePassword1")
	setupChatAgent(t, srv, "chatty")
	w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "chatty"})
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &c)

	rt := srv.chatSvc.Runtime()
	if rt.TranscriptPath(c.ID, "msg-1") == "" {
		t.Skip("no data dir in this server")
	}
	rt.BeginLive(c.ID, "msg-1")
	rt.EndLive(c.ID, "msg-1")

	w = getJSON(t, h, "/chats/"+c.ID+"/turns/msg-1/transcript", token)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"lines":[]`) {
		t.Fatalf("transcript = %d %s", w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/chats/"+c.ID+"/turns/msg-none/transcript", token); w.Code != http.StatusNotFound {
		t.Errorf("missing transcript = %d, want 404", w.Code)
	}
	other := registerUser(t, h, "other-transcript@test.com", "OT", "SecurePassword1")
	if w := getJSON(t, h, "/chats/"+c.ID+"/turns/msg-1/transcript", other); w.Code != http.StatusNotFound {
		t.Errorf("someone else's transcript = %d, want 404", w.Code)
	}
}

func TestChats_MentionedTasksReachTheAgent(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "refs@test.com", "RF", "SecurePassword1")
	setupChatAgent(t, srv, "chatty")
	taskID := createTaskViaAPI(t, h, token, "Export invoices")
	w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "chatty"})
	var c chat.Chat
	_ = json.Unmarshal(w.Body.Bytes(), &c)

	w = postJSONAuth(t, h, "/chats/"+c.ID+"/messages", token, map[string]any{
		"text": "¿Cómo sigue?", "refs": []string{taskID, "0000000000000000"},
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST message = %d: %s", w.Code, w.Body.String())
	}
	var msgs []chat.Message
	_ = json.Unmarshal(getJSON(t, h, "/chats/"+c.ID+"/messages", token).Body.Bytes(), &msgs)
	if len(msgs) == 0 || msgs[0].Text != "¿Cómo sigue?" {
		t.Fatalf("messages = %+v, want the text as written", msgs)
	}
	if msgs[0].Metadata[chat.MetaRefs] != taskID {
		t.Errorf("refs = %q, want only the task that exists", msgs[0].Metadata[chat.MetaRefs])
	}
	if w := postJSONAuth(t, h, "/chats/"+c.ID+"/messages", token, map[string]any{"text": "x", "mode": "later"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad mode = %d, want 400", w.Code)
	}
}
