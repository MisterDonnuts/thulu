package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestIntegration_CreateProjectInBackground(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "bgclone@test.com", "B", "SecurePassword1")

	w := postJSONAuth(t, h, "/projects", token, map[string]any{
		"name": "Background", "repo_url": testgit.BareRepo(t), "background": true,
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /projects background = %d, want 202: %s", w.Code, w.Body.String())
	}
	var proj project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &proj); err != nil {
		t.Fatal(err)
	}
	if proj.CloneStatus != project.CloneCloning {
		t.Errorf("clone_status = %q, want %q", proj.CloneStatus, project.CloneCloning)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		g := getJSON(t, h, "/projects/"+proj.ID, token)
		var got project.Project
		if err := json.Unmarshal(g.Body.Bytes(), &got); err != nil {
			t.Fatalf("GET /projects/%s = %d %s", proj.ID, g.Code, g.Body.String())
		}
		if got.CloneStatus == "" {
			if got.DefaultBranch != "main" {
				t.Errorf("default_branch = %q, want main", got.DefaultBranch)
			}
			return
		}
		if got.CloneStatus == project.CloneFailed || time.Now().After(deadline) {
			t.Fatalf("clone ended as %q (%q)", got.CloneStatus, got.CloneError)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
