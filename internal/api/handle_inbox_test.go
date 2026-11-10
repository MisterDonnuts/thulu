package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestInbox_ListsTheWorkspacesOpenQuestions(t *testing.T) {
	t.Parallel()
	rig := newRunScopeRig(t)
	runningA(t, rig)
	q := askAsA(t, rig).Question

	var inbox struct {
		Questions []task.PendingQuestion `json:"questions"`
		Approvals []json.RawMessage      `json:"approvals"`
	}
	w := getJSON(t, rig.h, "/inbox", rig.token)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &inbox) != nil {
		t.Fatalf("inbox = %d: %s", w.Code, w.Body.String())
	}
	if len(inbox.Questions) != 1 || inbox.Questions[0].ID != q.ID || inbox.Questions[0].TaskTitle != "A" ||
		inbox.Questions[0].Stage != "in_progress" {
		t.Errorf("questions = %+v, want A's question with its task", inbox.Questions)
	}
	if inbox.Approvals == nil {
		t.Errorf("approvals = null, want a list: %s", w.Body.String())
	}

	otherToken, _ := mintWorkspaceUser(t, rig.srv, "elsewhere@test.com", "Elsewhere")
	w = getJSON(t, rig.h, "/inbox", otherToken)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &inbox) != nil || len(inbox.Questions) != 0 {
		t.Errorf("another workspace's inbox = %d %s, want empty", w.Code, w.Body.String())
	}
}
