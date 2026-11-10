package task

import (
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/project"
)

var agentPRProjects = map[string]string{
	"proj-1":  "https://github.com/acme/webshop.git",
	"proj-or": "git@github-alias:o/r.git",
	"proj-2":  "https://github.com/acme/other",
}

func newAgentPRDispatcher(t *testing.T) (*Dispatcher, *Store) {
	t.Helper()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	projects, err := project.NewStore(db, filepath.Join(t.TempDir(), "projects"))
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	for id, remote := range agentPRProjects {
		if err := projects.ImportProject(t.Context(), project.Project{
			ID: id, Name: id, Path: filepath.Join(t.TempDir(), id), RepoURL: remote,
			DefaultBranch: "main", WorkspaceID: testWS,
		}); err != nil {
			t.Fatalf("import project %s: %v", id, err)
		}
	}
	return NewDispatcher(store, nil, projects, nil, nil), store
}

func newAgentPRTask(t *testing.T, store *Store, projectIDs ...string) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "review deploy", ProjectIDs: projectIDs}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return tk
}

func TestRecordAgentPRLinksURL(t *testing.T) {
	d, store := newAgentPRDispatcher(t)
	tk := newAgentPRTask(t, store, "proj-1")

	const prURL = "https://github.com/acme/webshop/pull/53"
	updated, projectID, err := d.RecordAgentPR(t.Context(), tk, "acme/webshop", prURL)
	if err != nil {
		t.Fatalf("RecordAgentPR: %v", err)
	}
	if projectID != "proj-1" {
		t.Errorf("projectID = %q, want proj-1", projectID)
	}
	if got := updated.PRURLs["proj-1"]; got != prURL {
		t.Errorf("in-memory pr_urls = %q, want %q", got, prURL)
	}

	reloaded, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := reloaded.PRURLs["proj-1"]; got != prURL {
		t.Errorf("persisted pr_urls = %q, want %q", got, prURL)
	}
}

func TestRecordAgentPRIsIdempotent(t *testing.T) {
	d, store := newAgentPRDispatcher(t)
	tk := newAgentPRTask(t, store, "proj-or")
	const prURL = "https://github.com/o/r/pull/1"

	if _, _, err := d.RecordAgentPR(t.Context(), tk, "o/r", prURL); err != nil {
		t.Fatalf("first RecordAgentPR: %v", err)
	}
	if _, _, err := d.RecordAgentPR(t.Context(), tk, "o/r", prURL); err != nil {
		t.Fatalf("second RecordAgentPR: %v", err)
	}

	events, err := store.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == EventPROpened {
			n++
		}
	}
	if n != 1 {
		t.Errorf("pr_opened events = %d, want 1 (re-reporting must not duplicate)", n)
	}
}

func TestRecordAgentPRKeepsFirstURL(t *testing.T) {
	d, store := newAgentPRDispatcher(t)
	tk := newAgentPRTask(t, store, "proj-or")

	if _, _, err := d.RecordAgentPR(t.Context(), tk, "o/r", "https://github.com/o/r/pull/1"); err != nil {
		t.Fatalf("RecordAgentPR: %v", err)
	}
	if _, _, err := d.RecordAgentPR(t.Context(), tk, "o/r", "https://github.com/o/r/pull/2"); err != nil {
		t.Fatalf("RecordAgentPR: %v", err)
	}
	reloaded, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := reloaded.PRURLs["proj-1"]; got != "" {
		t.Errorf("pr_urls[proj-1] = %q, the PR belongs to proj-or", got)
	}
	if got := reloaded.PRURLs["proj-or"]; got != "https://github.com/o/r/pull/1" {
		t.Errorf("pr_urls = %q, want the first PR to be kept", got)
	}
}

func TestRecordAgentPRRejectsBadInput(t *testing.T) {
	d, store := newAgentPRDispatcher(t)

	if _, _, err := d.RecordAgentPR(t.Context(), nil, "o/r", "https://github.com/o/r/pull/1"); err == nil {
		t.Error("nil task: want error")
	}
	tk := newAgentPRTask(t, store, "proj-or")
	if _, _, err := d.RecordAgentPR(t.Context(), tk, "o/r", "   "); err == nil {
		t.Error("blank url: want error")
	}
	bare := newAgentPRTask(t, store)
	if _, _, err := d.RecordAgentPR(t.Context(), bare, "o/r", "https://github.com/o/r/pull/1"); err == nil {
		t.Error("no linked project: want error")
	}
}

// Accepting a PR from a repo the task does not touch would route a stranger's PR review comments
// into this tenant's automations.
func TestRecordAgentPRRefusesAPullRequestOutsideTheTask(t *testing.T) {
	d, store := newAgentPRDispatcher(t)
	tk := newAgentPRTask(t, store, "proj-1")

	for _, tc := range []struct{ name, repo, url string }{
		{"other repo", "victim/secret", "https://github.com/victim/secret/pull/7"},
		{"other repo, no repo given", "", "https://github.com/victim/secret/pull/7"},
		{"repo lies about the url", "acme/webshop", "https://github.com/victim/secret/pull/7"},
		{"url lies about the repo", "victim/secret", "https://github.com/acme/webshop/pull/7"},
		{"another host", "", "https://evil.example/acme/webshop/pull/7"},
		{"an issue, not a pull", "", "https://github.com/acme/webshop/issues/7"},
		{"trailing path", "", "https://github.com/acme/webshop/pull/7/files"},
		{"query string", "", "https://github.com/acme/webshop/pull/7?x=1"},
		{"no number", "", "https://github.com/acme/webshop/pull/abc"},
		{"userinfo", "", "https://u:p@github.com/acme/webshop/pull/7"},
		{"not a url", "", "javascript:alert(1)"},
	} {
		if _, _, err := d.RecordAgentPR(t.Context(), tk, tc.repo, tc.url); err == nil {
			t.Errorf("%s: %q was recorded, want it refused", tc.name, tc.url)
		}
	}
	reloaded, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(reloaded.PRURLs) != 0 {
		t.Errorf("pr_urls = %v after refused reports, want none", reloaded.PRURLs)
	}
}

func TestRecordAgentPRPicksTheMatchingProject(t *testing.T) {
	d, store := newAgentPRDispatcher(t)
	tk := newAgentPRTask(t, store, "proj-1", "proj-or", "proj-2")

	_, projectID, err := d.RecordAgentPR(t.Context(), tk, "", "https://github.com/O/R/pull/3")
	if err != nil {
		t.Fatalf("RecordAgentPR: %v", err)
	}
	if projectID != "proj-or" {
		t.Errorf("projectID = %q, want proj-or (owner/repo compare ignores case)", projectID)
	}
}

func TestProjectForRepoNeedsAMatch(t *testing.T) {
	d, store := newAgentPRDispatcher(t)
	tk := newAgentPRTask(t, store, "proj-1", "proj-2")

	if _, err := d.projectForRepo(t.Context(), tk, "not-a-repo"); err == nil {
		t.Error("unparseable repo: want error, not a guess")
	}
	if _, err := d.projectForRepo(t.Context(), tk, "o/r"); err == nil {
		t.Error("no matching project: want error")
	}
	sole := newAgentPRTask(t, store, "proj-1")
	if _, err := d.projectForRepo(t.Context(), sole, "o/r"); err == nil {
		t.Error("a sole linked project was accepted for a repo it does not hold")
	}
	unwired := NewDispatcher(store, nil, nil, nil, nil)
	if _, err := unwired.projectForRepo(t.Context(), sole, "acme/webshop"); err == nil {
		t.Error("no project store: want error")
	}
}

func TestParsePRURL(t *testing.T) {
	owner, repo, ok := parsePRURL("https://github.com/acme/webshop/pull/53")
	if !ok || owner != "acme" || repo != "webshop" {
		t.Errorf("parsePRURL = %q %q %v", owner, repo, ok)
	}
	t.Setenv("OCTOPUS_GITHUB_URL", "https://ghe.example.com")
	if _, _, ok := parsePRURL("https://github.com/o/r/pull/1"); ok {
		t.Error("a github.com URL parsed on a GHES install")
	}
	if _, _, ok := parsePRURL("https://ghe.example.com/o/r/pull/1"); !ok {
		t.Error("the configured GHES host was refused")
	}
}
