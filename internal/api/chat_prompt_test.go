package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegration_Chats_PostMessage_NoPrompt(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "chatnoprompt@test.com", "NP", "SecurePassword1")
	name := setupChatAgent(t, srv, "mute")
	if err := os.Remove(filepath.Join(srv.agentsDir, "agents", name, "prompts", "main.md")); err != nil {
		t.Fatal(err)
	}
	id := createChat(t, h, token, name)

	w := postJSONAuth(t, h, "/chats/"+id+"/messages", token, map[string]string{"text": "hola"})
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("POST message to an agent with no prompt = %d, want 412: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(w.Body).Decode(&body)
	for _, want := range []string{"mute", "prompts/chat.md", "prompts/main.md"} {
		if !strings.Contains(body.Error, want) {
			t.Errorf("error %q does not mention %q", body.Error, want)
		}
	}

	mw := getJSON(t, h, "/chats/"+id+"/messages", token)
	var msgs []map[string]any
	_ = json.NewDecoder(mw.Body).Decode(&msgs)
	if len(msgs) != 0 {
		t.Errorf("a refused message was stored: %v", msgs)
	}

	chatPrompt := filepath.Join(srv.agentsDir, "agents", name, "prompts", "chat.md")
	if err := os.WriteFile(chatPrompt, []byte("# Chat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := postJSONAuth(t, h, "/chats/"+id+"/messages", token, map[string]string{"text": "hola"}); w.Code != http.StatusAccepted {
		t.Errorf("POST message with prompts/chat.md = %d, want 202: %s", w.Code, w.Body.String())
	}
}
