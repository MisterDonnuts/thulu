package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
)

func TestRunKey_CannotWriteEvidence(t *testing.T) {
	srv, h, store := evidenceServer(t)
	token, ws := registerUserInWorkspace(t, h, "qa@example.com", "QA", "")
	env := decode[evidence.Environment](t, postJSONAuth(t, h, "/evidence/environments", token,
		map[string]any{"name": "Staging", "base_url": "https://staging.example.com"}))
	c := decode[evidence.Case](t, postJSONAuth(t, h, "/evidence/cases", token, map[string]any{"title": "Login works", "steps": "Log in"}))
	taskID := taskIDFor(t, h, token, "Checkout")

	mint := func(scope auth.RunScope) string {
		t.Helper()
		raw, _, err := srv.authStore.MintRunKey(t.Context(), scope, ws, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	taskKey := mint(auth.RunScope{TaskID: taskID, Agent: "developer", Stage: "in_progress"})
	// A chat opened on a task carries tasks:write too, but it is not recording.
	taskChatKey := mint(auth.RunScope{TaskID: taskID, ChatID: "chat-on-task", Agent: "developer", Stage: "chat"})

	writes := []scopedCall{
		{http.MethodPost, "/evidence/environments", map[string]any{"name": "Planted", "base_url": "https://attacker.example.net"}},
		{http.MethodPatch, "/evidence/environments/" + env.ID, map[string]any{"base_url": "https://attacker.example.net"}},
		{http.MethodPatch, "/evidence/environments/" + env.ID, map[string]any{"allowed_hosts": []string{"attacker.example.net"}}},
		{http.MethodPut, "/evidence/environments/" + env.ID + "/secrets", map[string]any{"TEST_PASSWORD": "planted"}},
		{http.MethodPost, "/evidence/environments/" + env.ID + "/run", map[string]any{}},
		{http.MethodPost, "/evidence/cases", map[string]any{"title": "Planted"}},
		{http.MethodPatch, "/evidence/cases/" + c.ID, map[string]any{"title": "renamed by a run"}},
		{http.MethodPut, "/evidence/cases/" + c.ID + "/take", map[string]json.RawMessage{"take": json.RawMessage(evidenceTake)}},
		{http.MethodPost, "/evidence/cases/" + c.ID + "/record", map[string]any{"environment_id": env.ID}},
		{http.MethodPost, "/evidence/runs", map[string]any{"environment_id": env.ID}},
		{http.MethodPut, "/evidence/recording/take", map[string]json.RawMessage{"take": json.RawMessage(evidenceTake)}},
	}
	for name, key := range map[string]string{"task run": taskKey, "chat on a task": taskChatKey} {
		for _, call := range writes {
			if call.path == "/evidence/recording/take" && name == "chat on a task" {
				continue // a chat key passes the gate; the handler answers that it is not recording
			}
			w := keyDo(t, h, call.method, call.path, key, call.body)
			if w.Code != http.StatusForbidden || errCode(t, w) != "api.scope.errors.run_evidence_scope" {
				t.Errorf("%s key %s %s = %d %s, want 403 run_evidence_scope", name, call.method, call.path, w.Code, w.Body)
			}
		}
	}

	got, err := store.GetEnvironment(t.Context(), ws, env.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "https://staging.example.com" {
		t.Fatalf("a run moved the environment: %q", got.BaseURL)
	}

	for _, path := range []string{"/evidence/environments", "/evidence/cases", "/evidence/cases/" + c.ID, "/evidence/runs"} {
		if w := keyDo(t, h, http.MethodGet, path, taskKey, nil); w.Code != http.StatusOK {
			t.Errorf("task run reads %s = %d %s", path, w.Code, w.Body)
		}
	}

	if _, err := store.CreateRecording(t.Context(), ws, c.ID, env.ID, "chat-rec", "qa@example.com"); err != nil {
		t.Fatal(err)
	}
	recKey := mint(auth.RunScope{ChatID: "chat-rec", Agent: "developer", Stage: "chat"})
	body, _ := json.Marshal(map[string]json.RawMessage{"take": json.RawMessage(evidenceTake)})
	if w := keyRequest(t, h, http.MethodPut, "/evidence/recording/take", recKey, body, "application/json"); w.Code != http.StatusOK {
		t.Fatalf("recording chat saves its take: %d %s", w.Code, w.Body)
	}
	if w := keyDo(t, h, http.MethodPatch, "/evidence/environments/"+env.ID, recKey, map[string]any{"base_url": "https://attacker.example.net"}); w.Code != http.StatusForbidden {
		t.Fatalf("recording chat edited the environment: %d %s", w.Code, w.Body)
	}
}
