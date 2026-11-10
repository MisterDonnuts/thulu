package api

import (
	"net/http"
	"testing"
)

func TestTaskSandbox_NoneIsNoContentForARegisteredUser(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "nosandbox@test.com", "NS", "SecurePassword1")
	taskID := createTaskViaAPI(t, h, token, "Sin sandbox")
	if w := getJSON(t, h, "/tasks/"+taskID+"/sandbox", token); w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Errorf("GET sandbox of a task with none = %d %q, want 204", w.Code, w.Body.String())
	}
	if w := getJSON(t, h, "/tasks/nope/sandbox", token); w.Code != http.StatusNotFound {
		t.Errorf("GET sandbox of a missing task = %d, want 404", w.Code)
	}
}
