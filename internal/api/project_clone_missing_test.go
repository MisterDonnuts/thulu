package api

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
)

func TestProjectWithoutCloneAnswersPrecondition(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "noclone@test.com", "NoClone")
	if err := srv.projectStore.ImportProject(t.Context(), project.Project{
		ID: "noclone000000001", Name: "conduit-api", Path: filepath.Join(t.TempDir(), "missing"),
		RepoURL: "https://github.com/acme/conduit-api", DefaultBranch: "main", WorkspaceID: ws,
	}); err != nil {
		t.Fatalf("import project: %v", err)
	}
	for _, path := range []string{"/projects/noclone000000001/worktrees", "/projects/noclone000000001/init-script/repo-files"} {
		w := getJSON(t, h, path, token)
		if w.Code != http.StatusPreconditionFailed {
			t.Errorf("GET %s = %d want 412: %s", path, w.Code, w.Body.String())
		}
	}
}
