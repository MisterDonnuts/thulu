package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestIntegration_ConversationIsItsOwnThread(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "conv@example.com", "Conv", "SecurePassword1")

	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "parser", "body": "arreglar el parser", "project_ids": []string{},
	})
	if cw.Code != http.StatusCreated {
		t.Fatalf("POST /tasks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	id, _ := created["id"].(string)

	mw := postJSONAuth(t, h, "/tasks/"+id+"/converse", token, map[string]any{
		"text": "start with the tests",
	})
	if mw.Code != http.StatusCreated {
		t.Fatalf("POST converse = %d: %s", mw.Code, mw.Body.String())
	}
	var res struct {
		Delivery string `json:"delivery"`
		Message  struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		} `json:"message"`
	}
	if err := json.NewDecoder(mw.Body).Decode(&res); err != nil {
		t.Fatalf("decode converse: %v", err)
	}
	if res.Delivery != "unreachable" {
		t.Errorf("delivery = %q, want unreachable", res.Delivery)
	}
	if res.Message.ID == "" || res.Message.Body != "start with the tests" {
		t.Fatalf("message = %+v, want the text recorded", res.Message)
	}

	conv := getJSON(t, h, "/tasks/"+id+"/conversation", token)
	if conv.Code != http.StatusOK {
		t.Fatalf("GET conversation = %d: %s", conv.Code, conv.Body.String())
	}
	var got conversationResponse
	if err := json.NewDecoder(conv.Body).Decode(&got); err != nil {
		t.Fatalf("decode conversation: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Kind != "message" {
		t.Fatalf("items = %+v, want one message", got.Items)
	}

	if n := commentCount(t, h, id, token); n != 0 {
		t.Fatalf("comments = %d, want the thread untouched", n)
	}

	pw := postJSONAuth(t, h, "/tasks/"+id+"/conversation/"+res.Message.ID+"/to-comment", token, map[string]any{})
	if pw.Code != http.StatusCreated {
		t.Fatalf("POST to-comment = %d: %s", pw.Code, pw.Body.String())
	}
	if n := commentCount(t, h, id, token); n != 1 {
		t.Fatalf("comments = %d, want the message moved over", n)
	}
}

func commentCount(t *testing.T, h http.Handler, id, token string) int {
	t.Helper()
	w := getJSON(t, h, "/tasks/"+id, token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /tasks/%s = %d: %s", id, w.Code, w.Body.String())
	}
	var got struct {
		Comments []any `json:"comments"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return len(got.Comments)
}
