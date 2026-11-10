package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type stubProjects struct{ p *project.Project }

func (s stubProjects) Get(_ context.Context, id string) (*project.Project, error) {
	if s.p != nil && s.p.ID == id {
		return s.p, nil
	}
	return nil, errors.New("project not found")
}

func fakeMintServer(t *testing.T, mints *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/access_tokens") {
			http.NotFound(w, r)
			return
		}
		*mints++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"ghs_live","expires_at":"2099-01-01T00:00:00Z"}`)
	}))
}

func TestResolverAuthorizesInstallationAtMintTime(t *testing.T) {
	var mints int
	fake := fakeMintServer(t, &mints)
	defer fake.Close()
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, fake.URL, keyPEM)
	store := newTestStore(t)

	proj := &project.Project{
		ID: "p1", RepoURL: "https://github.com/org/repo",
		WorkspaceID: "ws-a", GithubInstallationID: 42,
	}
	creds := stubCreds{tok: &credentials.GitToken{CredentialID: "c1", Token: "ghp_fallback"}}
	r := NewResolver(svc, store, stubProjects{p: proj}, creds)

	rec, err := r.GetGithubToken(t.Context(), "p1")
	if err != nil || rec.Token != "ghp_fallback" {
		t.Fatalf("missing row: want the stored credential, got %+v err=%v", rec, err)
	}
	if mints != 0 {
		t.Fatalf("minted %d tokens for an unlinked installation", mints)
	}

	if err := store.UpsertInstallation(t.Context(), Installation{InstallationID: 42, WorkspaceID: "ws-a", AccountLogin: "org"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec, err = r.GetGithubToken(t.Context(), "p1")
	if err != nil || rec.Token != "ghs_live" {
		t.Fatalf("authorized: want minted token, got %+v err=%v", rec, err)
	}
	if mints != 1 {
		t.Fatalf("want 1 mint, got %d", mints)
	}

	if err := store.SetSuspended(t.Context(), 42, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if rec, _ = r.GetGithubToken(t.Context(), "p1"); rec.Token != "ghp_fallback" {
		t.Errorf("suspended installation still served App token %q", rec.Token)
	}
	if mints != 1 {
		t.Errorf("suspended installation minted (mints=%d)", mints)
	}
	if err := store.SetSuspended(t.Context(), 42, false); err != nil {
		t.Fatalf("unsuspend: %v", err)
	}

	if _, err := store.ReassignInstallation(t.Context(), 42, "ws-b"); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if rec, _ = r.GetGithubToken(t.Context(), "p1"); rec.Token != "ghp_fallback" {
		t.Errorf("cross-workspace re-claim still served App token %q", rec.Token)
	}

	if err := store.DeleteInstallation(t.Context(), 42); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rec, _ = r.GetGithubToken(t.Context(), "p1"); rec.Token != "ghp_fallback" {
		t.Errorf("deleted installation still served App token %q", rec.Token)
	}
}

func TestResolverNilLegs(t *testing.T) {
	r := NewResolver(nil, nil, nil, nil)
	if _, err := r.GetGithubToken(t.Context(), "p1"); !errors.Is(err, ErrNoCredential) {
		t.Errorf("all-nil resolver: want ErrNoCredential, got %v", err)
	}
}

func TestResolverReadsTheCredentialManager(t *testing.T) {
	proj := &project.Project{ID: "p1", RepoURL: "https://github.com/org/repo", WorkspaceID: "ws-a"}
	var askedWorkspace string
	creds := stubCreds{
		tok: &credentials.GitToken{CredentialID: "cred-1", Identity: "octocat", Token: "ghp_managed"},
		ws:  &askedWorkspace,
	}

	r := NewResolver(nil, nil, stubProjects{p: proj}, creds)
	rec, err := r.GetGithubToken(t.Context(), "p1")
	if err != nil || rec.Token != "ghp_managed" {
		t.Fatalf("want the managed credential, got %+v err=%v", rec, err)
	}
	if rec.Login != "octocat" || rec.Source != "credential" || rec.CredentialID != "cred-1" {
		t.Errorf("resolved token = %+v, want the credential's identity and id", rec)
	}
	if askedWorkspace != "ws-a" {
		t.Errorf("asked workspace %q, want the project's own", askedWorkspace)
	}
}

func TestResolverReportsNoCredential(t *testing.T) {
	proj := &project.Project{ID: "p1", RepoURL: "https://github.com/org/repo", WorkspaceID: "ws-a"}

	for name, creds := range map[string]stubCreds{
		"nothing granted": {err: credentials.ErrNotFound},
		"read failed":     {err: errors.New("decrypt credential: cipher: message authentication failed")},
	} {
		r := NewResolver(nil, nil, stubProjects{p: proj}, creds)
		if _, err := r.GetGithubToken(t.Context(), "p1"); !errors.Is(err, ErrNoCredential) {
			t.Errorf("%s: want ErrNoCredential, got %v", name, err)
		}
	}
}

func TestResolverSkipsTheCredentialLegWithoutAWorkspace(t *testing.T) {
	proj := &project.Project{ID: "p1", RepoURL: "https://github.com/org/repo"}
	creds := stubCreds{tok: &credentials.GitToken{Token: "ghp_managed"}}

	r := NewResolver(nil, nil, stubProjects{p: proj}, creds)
	if _, err := r.GetGithubToken(t.Context(), "p1"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("workspace-less project: want ErrNoCredential, got %v", err)
	}
}

type stubCreds struct {
	tok *credentials.GitToken
	err error
	ws  *string
}

func (s stubCreds) GithubTokenForProject(_ context.Context, workspaceID, _ string) (*credentials.GitToken, error) {
	if s.ws != nil {
		*s.ws = workspaceID
	}
	return s.tok, s.err
}
