package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestProjectCreateRejectsHostileInput(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "projhostile@test.com", "PH", "SecurePassword1")

	cases := []map[string]any{
		{"name": "opt", "repo_url": "--upload-pack=true"},
		{"name": "creds", "repo_url": "https://user:secret@github.com/o/r.git"},
		{"name": "metadata", "repo_url": "http://169.254.169.254/latest"},
		{"name": "plain-http", "repo_url": "http://github.com/o/r.git"},
		{"name": "transport", "repo_url": "fd::17"},
		{"name": "branch-opt", "repo_url": "https://github.com/o/r.git", "default_branch": "--orphan"},
		{"name": "branch-dots", "repo_url": "https://github.com/o/r.git", "default_branch": "a..b"},
	}
	for _, body := range cases {
		w := postJSONAuth(t, h, "/projects", token, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("POST /projects %v = %d, want 400: %s", body, w.Code, w.Body.String())
		}
	}
}

func TestProjectUpdateRejectsHostileBranch(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "projhostileup@test.com", "PU", "SecurePassword1")
	repo := testgit.BareRepo(t)

	wc := postJSONAuth(t, h, "/projects", token, map[string]any{"name": "P", "repo_url": repo})
	if wc.Code != http.StatusCreated {
		t.Fatalf("POST /projects = %d: %s", wc.Code, wc.Body.String())
	}
	var proj struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(wc.Body).Decode(&proj); err != nil || proj.ID == "" {
		t.Fatalf("decode project: %v", err)
	}

	for _, branch := range []string{"--force", "main..x", "a b", "refs/heads/@{-1}"} {
		w := patchJSONAuth(t, h, "/projects/"+proj.ID, token, map[string]any{"default_branch": branch})
		if w.Code != http.StatusBadRequest {
			t.Errorf("PATCH default_branch %q = %d, want 400: %s", branch, w.Code, w.Body.String())
		}
	}
}

func TestProjectInputStatus(t *testing.T) {
	cases := map[error]int{
		fmt.Errorf("wrap: %w", project.ErrInvalidRepoURL): http.StatusBadRequest,
		fmt.Errorf("wrap: %w", project.ErrInvalidBranch):  http.StatusBadRequest,
		errors.New("clone failed"):                        http.StatusInternalServerError,
	}
	for err, want := range cases {
		if got := projectInputStatus(err); got != want {
			t.Errorf("projectInputStatus(%v) = %d, want %d", err, got, want)
		}
	}
}

func TestTokenPermissionsParam(t *testing.T) {
	withDeployments := githubapp.PermsAgentTools.With(githubapp.PermDeploymentsWrite)
	withWorkflows := githubapp.PermsAgentTools.With(githubapp.PermWorkflowsWrite)
	cases := []struct {
		query          string
		want           githubapp.Permissions
		ok             bool
		workflowsOptIn bool
	}{
		{"", githubapp.PermsAgentTools, true, false},
		{"?permissions=actions:write", githubapp.PermsActionsWrite, true, false},
		{"?permissions=deployments:write", withDeployments, true, false},
		{"?permissions=administration:write", nil, false, false},
		{"?permissions=actions:write,contents:write", nil, false, false},
		{"?permissions=ACTIONS:WRITE", nil, false, false},
		{"?permissions=workflows:write", nil, false, false},
		{"?permissions=workflows:write", withWorkflows, true, true},
		{"?permissions=actions:write,workflows:write", githubapp.PermsActionsWrite.With(githubapp.PermWorkflowsWrite), true, true},
		{"?permissions=checks:read", githubapp.PermsAgentTools.With(githubapp.PermChecksRead), true, false},
		{"?permissions=checks:write", nil, false, false},
		{"?permissions=actions:write,checks:read", githubapp.PermsActionsWrite.With(githubapp.PermChecksRead), true, false},
	}
	for _, c := range cases {
		optIn := ""
		if c.workflowsOptIn {
			optIn = "1"
		}
		t.Setenv(githubapp.EnvAllowWorkflowWrites, optIn)
		r := httptest.NewRequest(http.MethodGet, "/internal/projects/p/github-token"+c.query, nil)
		got, err := tokenPermissionsParam(r)
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok=%v", c.query, err, c.ok)
			continue
		}
		if c.ok && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: perms = %v, want %v", c.query, got, c.want)
		}
	}
}
