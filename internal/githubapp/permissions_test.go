package githubapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/project"
)

type mintRequest struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}

func recordingMintServer(t *testing.T) (*httptest.Server, func() []mintRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []mintRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body mintRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("mint body: %v", err)
		}
		mu.Lock()
		got = append(got, body)
		n := len(got)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_%d","expires_at":%q}`, n, time.Now().Add(time.Hour).Format(time.RFC3339))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []mintRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]mintRequest(nil), got...)
	}
}

func TestMintRequestsOnlyAskedPermissions(t *testing.T) {
	srv, mints := recordingMintServer(t)
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, srv.URL, keyPEM)

	if _, err := svc.MintToken(t.Context(), 7, "octopus", PermsPush); err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if _, err := svc.InstallationToken(t.Context(), 7); err != nil {
		t.Fatalf("InstallationToken: %v", err)
	}
	got := mints()
	if len(got) != 2 {
		t.Fatalf("mints = %d, want 2", len(got))
	}
	if !reflect.DeepEqual(got[0].Repositories, []string{"octopus"}) ||
		!reflect.DeepEqual(got[0].Permissions, map[string]string(PermsPush)) {
		t.Errorf("repo mint = %+v", got[0])
	}
	if got[1].Repositories != nil || !reflect.DeepEqual(got[1].Permissions, map[string]string{"metadata": "read"}) {
		t.Errorf("installation mint = %+v", got[1])
	}
	for _, m := range got {
		for _, forbidden := range []string{"administration", "workflows", "secrets", "members"} {
			if _, ok := m.Permissions[forbidden]; ok {
				t.Errorf("mint asked for %s: %+v", forbidden, m)
			}
		}
	}
	if _, err := svc.MintToken(t.Context(), 7, "octopus", nil); err == nil {
		t.Error("MintToken with no permissions must fail, not mint the App's full grant")
	}
}

func TestMintCacheIsPerPermissionSet(t *testing.T) {
	srv, mints := recordingMintServer(t)
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, srv.URL, keyPEM)

	a, _ := svc.MintToken(t.Context(), 7, "octopus", PermsPush)
	b, _ := svc.MintToken(t.Context(), 7, "octopus", PermsActionsWrite)
	c, _ := svc.MintToken(t.Context(), 7, "octopus", Permissions{"pull_requests": "write", "contents": "write", "metadata": "read"})
	if a == b {
		t.Error("PermsPush and PermsActionsWrite shared a cached token")
	}
	if a != c {
		t.Error("equal permission sets in a different order missed the cache")
	}
	if n := len(mints()); n != 2 {
		t.Errorf("mints = %d, want 2", n)
	}
}

func TestResolverMintsWithCallerPermissions(t *testing.T) {
	srv, mints := recordingMintServer(t)
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, srv.URL, keyPEM)
	store := newTestStore(t)
	if err := store.UpsertInstallation(t.Context(), Installation{InstallationID: 42, WorkspaceID: "ws-a"}); err != nil {
		t.Fatalf("UpsertInstallation: %v", err)
	}
	proj := &project.Project{
		ID: "p1", RepoURL: "https://ghe.example.com/org/repo.git",
		WorkspaceID: "ws-a", GithubInstallationID: 42,
	}
	r := NewResolver(svc, store, stubProjects{p: proj}, stubCreds{})

	if _, err := r.GetGithubToken(t.Context(), "p1"); err != nil {
		t.Fatalf("GetGithubToken: %v", err)
	}
	if _, err := r.GetGithubTokenFor(t.Context(), "p1", PermsActionsWrite); err != nil {
		t.Fatalf("GetGithubTokenFor: %v", err)
	}
	got := mints()
	if len(got) != 2 {
		t.Fatalf("mints = %d", len(got))
	}
	if !reflect.DeepEqual(got[0].Permissions, map[string]string(PermsAgentTools)) {
		t.Errorf("default mint permissions = %v", got[0].Permissions)
	}
	if got[1].Permissions["actions"] != "write" {
		t.Errorf("actions write mint = %v", got[1].Permissions)
	}
	for _, m := range got {
		if !reflect.DeepEqual(m.Repositories, []string{"repo"}) {
			t.Errorf("mint not scoped to the project repo: %+v", m)
		}
	}
}

func TestResolverRefusesInstallationWideFallback(t *testing.T) {
	srv, mints := recordingMintServer(t)
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, srv.URL, keyPEM)
	store := newTestStore(t)
	if err := store.UpsertInstallation(t.Context(), Installation{InstallationID: 42, WorkspaceID: "ws-a"}); err != nil {
		t.Fatalf("UpsertInstallation: %v", err)
	}
	proj := &project.Project{ID: "p1", RepoURL: "not-a-remote", WorkspaceID: "ws-a", GithubInstallationID: 42}
	creds := stubCreds{tok: &credentials.GitToken{CredentialID: "c1", Token: "ghp_fallback"}}
	r := NewResolver(svc, store, stubProjects{p: proj}, creds)

	rec, err := r.GetGithubToken(t.Context(), "p1")
	if err != nil || rec.Token != "ghp_fallback" {
		t.Fatalf("GetGithubToken = %+v, %v; want the stored credential", rec, err)
	}
	if n := len(mints()); n != 0 {
		t.Errorf("minted %d installation tokens for an unscopable repo", n)
	}
}

func TestInstallLinksFollowGHES(t *testing.T) {
	t.Setenv("OCTOPUS_GITHUB_URL", "https://ghe.example.com")
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, "", keyPEM)
	link, err := svc.InstallURLWithState(t.Context(), "s")
	if err != nil {
		t.Fatalf("InstallURLWithState: %v", err)
	}
	if !strings.HasPrefix(link, "https://ghe.example.com/github-apps/octopus-test/installations/new?state=") {
		t.Errorf("install link = %s", link)
	}
	if got := NewService(nil).apiBase; got != "https://ghe.example.com/api/v3" {
		t.Errorf("apiBase = %q", got)
	}
}
