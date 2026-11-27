package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A pod clones only the task branch, so the brief names the commit the branch forks from: tools
// there compare against it instead of origin/<default>.
func TestIntegration_BriefGivesEachRepoItsBaseCommit(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "briefbase@test.com", "BB", "SecurePassword1")
	projID := createProject(t, h, token, "brief-base-proj")
	cw := postJSONAuth(t, h, "/tasks", token, map[string]any{"title": "base commit in the brief", "project_ids": []string{projID}})
	var tk map[string]any
	json.NewDecoder(cw.Body).Decode(&tk)
	taskID, _ := tk["id"].(string)

	brief := func() string {
		w := getJSON(t, h, "/tasks/"+taskID+"/brief", token)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /tasks/%s/brief = %d: %s", taskID, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if body := brief(); strings.Contains(body, "Base commit") {
		t.Errorf("no worktree yet, but the brief gives a base commit: %q", body)
	}

	path, _, err := srv.projectStore.CreateTaskWorktree(t.Context(), projID, taskID)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	base := gitOut(t, path, "rev-parse", "HEAD")
	gitOut(t, path, "commit", "--allow-empty", "-qm", "work on the task")

	body := brief()
	if !strings.Contains(body, "Base commit") || !strings.Contains(body, base) {
		t.Errorf("brief does not give the base commit %s: %q", base, body)
	}
	if head := gitOut(t, path, "rev-parse", "HEAD"); strings.Contains(body, head) {
		t.Errorf("brief gives the task head %s as the base", head)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
