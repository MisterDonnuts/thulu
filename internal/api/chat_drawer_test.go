package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/chat"
)

func TestDevMode_ChatOpensWhereItsAgentWasListed(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	setupChatAgent(t, srv, "chatty")

	w := postJSON(t, h, "/chats", map[string]string{"agent_type": "chatty"})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /chats with no workspace named = %d, want 201: %s", w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/chat-delegation", ""); w.Code != http.StatusOK {
		t.Fatalf("GET /chat-delegation with no workspace named = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestChats_TaskChatIsReusedAndNamedAfterTheTask(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "taskchat@test.com", "TC", "SecurePassword1")
	setupChatAgent(t, srv, "chatty")
	taskID := createTaskViaAPI(t, h, token, "Export invoices")

	type view struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		TaskTitle string `json:"task_title"`
		Reused    bool   `json:"reused"`
	}
	open := func(title string) (view, int) {
		w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "chatty", "task_id": taskID, "title": title})
		var v view
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return v, w.Code
	}

	first, code := open("")
	if code != http.StatusCreated || first.Reused {
		t.Fatalf("first POST /chats = %d %+v, want a new chat", code, first)
	}
	if first.Title != "Chatty · Export invoices" || first.TaskTitle != "Export invoices" {
		t.Errorf("chat = %+v, want it named after its task and carrying the task title", first)
	}

	again, code := open("Otra charla")
	if code != http.StatusOK || !again.Reused || again.ID != first.ID {
		t.Fatalf("second POST /chats = %d %+v, want %s reused", code, again, first.ID)
	}

	var listed []view
	if err := json.Unmarshal(getJSON(t, h, "/chats", token).Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].TaskTitle != "Export invoices" {
		t.Errorf("GET /chats = %+v, want the one chat with its task title", listed)
	}
}

func TestChats_AgentsListsReadiness(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatagents@test.com", "CA", "SecurePassword1")
	setupChatAgent(t, srv, "chatty")
	setupAgent(t, srv, "no-chat-agent")

	w := getJSON(t, h, "/chats/agents", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /chats/agents = %d: %s", w.Code, w.Body.String())
	}
	var agents []chatAgentView
	if err := json.Unmarshal(w.Body.Bytes(), &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Type != "chatty" {
		t.Fatalf("agents = %+v, want only the chat-capable one", agents)
	}
	if !agents[0].PromptReady || agents[0].MCP == nil {
		t.Errorf("chatty = %+v, want its prompt found and an MCP list (empty, not null)", agents[0])
	}
}

func TestChats_MessagesAndPreviewInTheReadersLanguage(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatlocale@test.com", "CL", "SecurePassword1")
	setupChatAgent(t, srv, "chatty")
	w := postJSONAuth(t, h, "/chats", token, map[string]string{"agent_type": "chatty"})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /chats = %d: %s", w.Code, w.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	failed := &chat.Message{ChatID: c.ID, Role: chat.RoleAssistant, Author: "chatty", Text: "**Agent error:** boom"}
	failed.SetTexts(map[string]string{"en": "**Agent error:** boom", "es": "**Error del agente:** explotó"})
	if _, err := srv.chatSvc.Store().AddMessage(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Accept-Language", "es")
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, r)
		if rw.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rw.Code, rw.Body.String())
		}
		return rw.Body.String()
	}
	if body := get("/chats/" + c.ID + "/messages"); !strings.Contains(body, "Error del agente") {
		t.Errorf("messages in Spanish = %s", body)
	}
	if body := get("/chats"); !strings.Contains(body, `"last_message_preview":"**Error del agente:** explotó"`) {
		t.Errorf("chat list in Spanish = %s", body)
	}
}

func TestChatPreview(t *testing.T) {
	short := "**Cambio de criterio:** redondeo en centavos"
	if got := chatPreview(short); got != short {
		t.Errorf("short preview = %q", got)
	}
	long := strings.Repeat("ñ", previewRunes+10)
	got := chatPreview(long)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != previewRunes+1 {
		t.Errorf("long preview has %d runes, valid %v", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
}
