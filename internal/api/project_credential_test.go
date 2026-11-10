package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/credentials"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

// POST /credentials ignores ?workspace= (dev mode resolves the default workspace) while project
// creation reads it.
// Leave it unnamed on both calls, or the project can never see the credential.
func mustCredential(t *testing.T, srv *Server, h http.Handler, name, token string) string {
	t.Helper()
	w := postJSON(t, h, "/credentials", map[string]any{
		"name": name, "provider": "github", "kind": "github_pat",
		"payload": map[string]string{"token": token},
	})
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create credential %s = %d: %s", name, w.Code, w.Body.String())
	}
	var got struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.ID == "" {
		t.Fatalf("create credential %s: no id in %s", name, w.Body.String())
	}
	// Introspection hits api.github.com with a fabricated token; stamp the verdict so the test
	// doesn't depend on network access.
	if err := srv.credStore.SetIntrospection(context.Background(), defaultWorkspaceID, got.ID,
		credentials.Introspection{State: credentials.StateValid, Identity: "octocat"}); err != nil {
		t.Fatalf("stamp introspection: %v", err)
	}
	return got.ID
}

func mustProject(t *testing.T, h http.Handler, body map[string]any) string {
	t.Helper()
	w := postJSON(t, h, "/projects?workspace="+defaultWorkspaceID, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", w.Code, w.Body.String())
	}
	var proj struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &proj)
	if proj.ID == "" {
		t.Fatalf("create project: no id in %s", w.Body.String())
	}
	return proj.ID
}

func projectCredentialView(t *testing.T, h http.Handler, projectID string) githubCredentialView {
	t.Helper()
	w := getJSON(t, h, "/projects/"+projectID+"/github-credential", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET github-credential = %d: %s", w.Code, w.Body.String())
	}
	var view githubCredentialView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode github-credential: %v", err)
	}
	return view
}

func TestCreateProjectWithCredentialGrantsIt(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	credID := mustCredential(t, srv, h, "GitHub deploys", "ghp_test")

	projID := mustProject(t, h, map[string]any{
		"name": "Granted", "repo_url": testgit.BareRepo(t), "github_credential_id": credID,
	})

	grants, err := srv.credStore.Grants(context.Background(), credID)
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	want := credentials.Grant{SubjectType: credentials.SubjectProject, SubjectID: projID}
	found := false
	for _, g := range grants {
		if g == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("project %s was not granted credential %s: %+v", projID, credID, grants)
	}

	view := projectCredentialView(t, h, projID)
	if view.Source != "credential" || view.CredentialID != credID {
		t.Errorf("view = %+v, want the credential it was created with", view)
	}
}

func TestProjectCredentialReachesTheTokenResolver(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	credID := mustCredential(t, srv, h, "GitHub deploys", "ghp_resolver")
	projID := mustProject(t, h, map[string]any{
		"name": "Resolved", "repo_url": testgit.BareRepo(t), "github_credential_id": credID,
	})

	rec, err := srv.githubTokenForProject(t.Context(), projID)
	if err != nil {
		t.Fatalf("githubTokenForProject: %v", err)
	}
	if rec.Token != "ghp_resolver" {
		t.Errorf("resolved token = %q, want the credential's", rec.Token)
	}
}

func TestSetProjectCredentialMovesTheGrant(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	oldID := mustCredential(t, srv, h, "old key", "ghp_old")
	newID := mustCredential(t, srv, h, "new key", "ghp_new")
	projID := mustProject(t, h, map[string]any{
		"name": "Moved", "repo_url": testgit.BareRepo(t), "github_credential_id": oldID,
	})

	w := putJSON(t, h, "/projects/"+projID+"/github-credential",
		map[string]any{"credential_id": newID}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT github-credential = %d: %s", w.Code, w.Body.String())
	}

	if view := projectCredentialView(t, h, projID); view.CredentialID != newID {
		t.Errorf("view credential = %q, want %q", view.CredentialID, newID)
	}
	grants, err := srv.credStore.Grants(context.Background(), oldID)
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	for _, g := range grants {
		if g.SubjectType == credentials.SubjectProject && g.SubjectID == projID {
			t.Errorf("the old credential still names project %s", projID)
		}
	}
}

func TestSetProjectCredentialDetaches(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	credID := mustCredential(t, srv, h, "temporary", "ghp_temp")
	projID := mustProject(t, h, map[string]any{
		"name": "Detached", "repo_url": testgit.BareRepo(t), "github_credential_id": credID,
	})

	w := putJSON(t, h, "/projects/"+projID+"/github-credential",
		map[string]any{"credential_id": ""}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT github-credential = %d: %s", w.Code, w.Body.String())
	}
	if view := projectCredentialView(t, h, projID); view.Source != "none" {
		t.Errorf("view after detach = %+v, want source none", view)
	}
}

func TestSetProjectCredentialRejectsOtherProviders(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	projID := mustProject(t, h, map[string]any{"name": "Wrong", "repo_url": testgit.BareRepo(t)})

	w := postJSON(t, h, "/credentials", map[string]any{
		"name": "prod aws", "provider": "aws", "kind": "aws_keys",
		"payload": map[string]string{"access_key_id": "AKIA1", "secret_access_key": "shh"},
	})
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create aws credential = %d: %s", w.Code, w.Body.String())
	}
	var aws struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &aws)

	w = putJSON(t, h, "/projects/"+projID+"/github-credential",
		map[string]any{"credential_id": aws.ID}, "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("aws credential on a project = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestDeleteProjectRevokesItsGrants(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	credID := mustCredential(t, srv, h, "GitHub deploys", "ghp_del")
	projID := mustProject(t, h, map[string]any{
		"name": "Doomed", "repo_url": testgit.BareRepo(t), "github_credential_id": credID,
	})

	req := httptest.NewRequest(http.MethodDelete, "/projects/"+projID, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE project = %d: %s", w.Code, w.Body.String())
	}

	grants, err := srv.credStore.Grants(context.Background(), credID)
	if err != nil {
		t.Fatalf("Grants: %v", err)
	}
	for _, g := range grants {
		if g.SubjectType == credentials.SubjectProject && g.SubjectID == projID {
			t.Errorf("deleted project %s still holds a grant", projID)
		}
	}
}
