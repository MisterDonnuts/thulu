package githubapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type checklessGitHub struct {
	mu    sync.Mutex
	asked []Permissions
}

func (g *checklessGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Permissions Permissions `json:"permissions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	g.mu.Lock()
	g.asked = append(g.asked, body.Permissions)
	g.mu.Unlock()
	if _, ok := body.Permissions["checks"]; ok {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"The permissions requested are not granted to this installation."}`)
		return
	}
	w.WriteHeader(http.StatusCreated)
	fmt.Fprint(w, `{"token":"ghs_actions","expires_at":"2099-01-01T00:00:00Z"}`)
}

func (g *checklessGitHub) withChecks() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, p := range g.asked {
		if _, ok := p["checks"]; ok {
			n++
		}
	}
	return n
}

func TestGetPRStatusToken_FallsBackWithoutChecks(t *testing.T) {
	gh := &checklessGitHub{}
	fake := httptest.NewServer(gh)
	defer fake.Close()
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, fake.URL, keyPEM)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	store := newTestStore(t)
	if err := store.UpsertInstallation(t.Context(), Installation{InstallationID: 42, WorkspaceID: "ws-a", AccountLogin: "org"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	proj := &project.Project{ID: "p1", RepoURL: "https://github.com/org/repo", WorkspaceID: "ws-a", GithubInstallationID: 42}
	creds := stubCreds{tok: &credentials.GitToken{CredentialID: "c1", Token: "ghp_fallback"}}
	r := NewResolver(svc, store, stubProjects{p: proj}, creds)

	tok, checks, err := r.GetPRStatusToken(t.Context(), "p1")
	if err != nil || tok.Token != "ghs_actions" || tok.Source != SourceApp || checks {
		t.Fatalf("first: got %+v checks=%v err=%v, want the Actions-only App token", tok, checks, err)
	}
	if gh.withChecks() != 1 {
		t.Fatalf("asked for checks %d times, want 1", gh.withChecks())
	}
	last := gh.asked[len(gh.asked)-1]
	if !samePermissions(last, PermsPRStatusActions) {
		t.Errorf("fallback minted %v, want %v", last, PermsPRStatusActions)
	}

	if _, checks, err = r.GetPRStatusToken(t.Context(), "p1"); err != nil || checks {
		t.Fatalf("second: checks=%v err=%v", checks, err)
	}
	if gh.withChecks() != 1 {
		t.Errorf("asked for checks again within the TTL (%d times)", gh.withChecks())
	}

	now = now.Add(noChecksTTL + time.Second)
	if _, _, err = r.GetPRStatusToken(t.Context(), "p1"); err != nil {
		t.Fatal(err)
	}
	if gh.withChecks() != 2 {
		t.Errorf("asked for checks %d times after the TTL, want 2", gh.withChecks())
	}
}

func TestGetPRStatusToken_StoredCredentialTriesChecks(t *testing.T) {
	proj := &project.Project{ID: "p1", RepoURL: "https://github.com/org/repo", WorkspaceID: "ws-a"}
	creds := stubCreds{tok: &credentials.GitToken{CredentialID: "c1", Token: "ghp_managed"}}
	r := NewResolver(nil, nil, stubProjects{p: proj}, creds)
	tok, checks, err := r.GetPRStatusToken(t.Context(), "p1")
	if err != nil || tok.Token != "ghp_managed" || !checks {
		t.Fatalf("got %+v checks=%v err=%v", tok, checks, err)
	}
}

func TestGetGithubTokenFor_ChecksReadRefused(t *testing.T) {
	gh := &checklessGitHub{}
	fake := httptest.NewServer(gh)
	defer fake.Close()
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, fake.URL, keyPEM)
	store := newTestStore(t)
	if err := store.UpsertInstallation(t.Context(), Installation{InstallationID: 42, WorkspaceID: "ws-a", AccountLogin: "org"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	proj := &project.Project{ID: "p1", RepoURL: "https://github.com/org/repo", WorkspaceID: "ws-a", GithubInstallationID: 42}
	withChecks := PermsAgentTools.With(PermChecksRead)

	noCreds := NewResolver(svc, store, stubProjects{p: proj}, stubCreds{err: credentials.ErrNotFound})
	if tok, err := noCreds.GetGithubTokenFor(t.Context(), "p1", withChecks); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("no stored credential: got %+v, %v; want ErrNoCredential", tok, err)
	}
	if gh.withChecks() != 1 {
		t.Errorf("asked for checks %d times, want 1", gh.withChecks())
	}
	if tok, err := noCreds.GetGithubTokenFor(t.Context(), "p1", PermsAgentTools); err != nil || tok.Source != SourceApp {
		t.Fatalf("default set: got %+v, %v; want an App token", tok, err)
	}

	stored := NewResolver(svc, store, stubProjects{p: proj},
		stubCreds{tok: &credentials.GitToken{CredentialID: "c1", Token: "ghp_fallback"}})
	if tok, err := stored.GetGithubTokenFor(t.Context(), "p1", withChecks); err != nil || tok.Source != SourceStored {
		t.Fatalf("stored credential: got %+v, %v; want the stored credential", tok, err)
	}
}

func samePermissions(a, b Permissions) bool {
	return a.String() == b.String()
}
