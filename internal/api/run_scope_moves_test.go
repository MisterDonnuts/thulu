package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

// A run that moved its own task forward jumped the stages after it: their agents never ran, and a
// required approval of a stage it jumped was never opened, so nothing held the move.
func TestRunKey_CannotMoveTheTaskItWorksOn(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	if w := patchJSONAuth(t, rig.h, "/stages/review", "", map[string]any{
		"required_approvals": []map[string]any{{"key": "merge", "label": "Merge"}},
	}); w.Code != http.StatusOK {
		t.Fatalf("PATCH review approvals = %d: %s", w.Code, w.Body.String())
	}
	if w := patchJSONAuth(t, rig.h, "/tasks/"+rig.a, rig.token, map[string]any{"stage": "in_progress"}); w.Code != http.StatusOK {
		t.Fatalf("person moving A to in_progress = %d: %s", w.Code, w.Body.String())
	}

	cases := []struct {
		name, key, target string
		body              map[string]any
	}{
		{"own task past review", rig.keyA, rig.a, map[string]any{"stage": "done"}},
		{"own task one stage on", rig.keyA, rig.a, map[string]any{"stage": "review"}},
		{"own task back", rig.keyA, rig.a, map[string]any{"stage": "backlog"}},
		{"own task to another type", rig.keyA, rig.a, map[string]any{"type_id": "chore"}},
		{"own task to another board", rig.keyA, rig.a, map[string]any{"board_id": "elsewhere"}},
		{"subtask run moving its parent", rig.keyA1, rig.a, map[string]any{"stage": "done"}},
	}
	for _, tc := range cases {
		w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+tc.target, tc.key, tc.body)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "run_moves_own_task") {
			t.Errorf("%s = %d, want 403 run_moves_own_task: %s", tc.name, w.Code, w.Body.String())
		}
	}
	got := getJSON(t, rig.h, "/tasks/"+rig.a, rig.token)
	var row map[string]any
	if err := json.NewDecoder(got.Body).Decode(&row); err != nil {
		t.Fatal(err)
	}
	if row["stage"] != "in_progress" {
		t.Errorf("A sits in %v after refused moves, want in_progress", row["stage"])
	}

	// The rest of a PATCH stays the run's: it names no stage, type or board, or the same ones.
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a, rig.keyA,
		map[string]any{"title": "retitled", "stage": "in_progress"}); w.Code != http.StatusOK {
		t.Errorf("own task PATCH naming its current stage = %d, want 200: %s", w.Code, w.Body.String())
	}
	// A delegated chat acts for the person who delegated it.
	chat := rig.runKey(t, auth.RunScope{TaskID: rig.a, ChatID: "chat-1", Agent: "admin", Stage: "chat",
		DelegatedBy: "run-scope@test.com"})
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a, chat, map[string]any{"stage": "review"}); w.Code == http.StatusForbidden &&
		strings.Contains(w.Body.String(), "run_moves_own_task") {
		t.Errorf("delegated chat refused as a run moving its own task: %s", w.Body.String())
	}
}

func TestRunKey_MovesTheTasksItCreated(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	w := keyDo(t, rig.h, http.MethodPost, "/tasks", rig.keyA, map[string]any{"title": "follow-up"})
	if w.Code != http.StatusCreated {
		t.Fatalf("run creating a task = %d: %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	c := created["id"].(string)
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+c, rig.keyA, map[string]any{"stage": "review"}); w.Code != http.StatusOK {
		t.Errorf("run moving the task it created = %d, want 200: %s", w.Code, w.Body.String())
	}
	if w := keyDo(t, rig.h, http.MethodPatch, "/tasks/"+rig.a2, rig.keyA1, map[string]any{"stage": "review"}); w.Code != http.StatusOK {
		t.Errorf("subtask run moving its sibling = %d, want 200: %s", w.Code, w.Body.String())
	}
}
