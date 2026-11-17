package hostrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

// fakeDaemon answers inventory and projects like the daemon would, and keeps what it was sent.
type fakeDaemon struct {
	mu        sync.Mutex
	inventory runner.Inventory
	added     []runner.AddProjectRequest
	status    int
	body      string
}

func (d *fakeDaemon) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch r.URL.Path {
		case "/runner/v1/inventory":
			_ = json.NewDecoder(r.Body).Decode(&d.inventory)
			w.WriteHeader(http.StatusNoContent)
		case "/runner/v1/projects":
			var req runner.AddProjectRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			d.added = append(d.added, req)
			if d.status != 0 {
				w.WriteHeader(d.status)
				_, _ = w.Write([]byte(d.body))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(runner.AddProjectResponse{ProjectID: "p1", ProjectName: filepath.Base(req.Path), Created: true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeRunnerConfig(t *testing.T, daemon string, roots ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner.json")
	cfg := &Config{DaemonURL: daemon, RunnerID: "r1", Credential: "c", Projects: map[string]string{}, Roots: roots}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAddFolderAllowsTheRepoAndCreatesTheProject(t *testing.T) {
	root, outside := newTree(t)
	d := &fakeDaemon{}
	srv := d.serve(t)
	path := writeRunnerConfig(t, srv.URL, root)

	// From a subfolder of a repository outside the allowed folders.
	sub := filepath.Join(outside, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := AddFolder(context.Background(), path, sub, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !added.Allowed || !added.Created || added.Path != outside || added.ProjectName != "secrets" {
		t.Errorf("added = %+v", added)
	}
	if added.URL != srv.URL+"/project/?id=p1" {
		t.Errorf("URL = %q", added.URL)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Roots, outside) || !slices.Contains(cfg.Roots, root) {
		t.Errorf("roots after add = %v, want the old one and the repository", cfg.Roots)
	}
	if len(d.added) != 1 || d.added[0].Path != outside {
		t.Errorf("sent %+v", d.added)
	}
	if !slices.ContainsFunc(d.inventory.Repos, func(r runner.InventoryRepo) bool { return r.Path == outside }) {
		t.Errorf("the inventory sent before the project did not list it: %+v", d.inventory.Repos)
	}
}

func TestAddFolderInsideAnAllowedFolderChangesNothingLocally(t *testing.T) {
	root, _ := newTree(t)
	d := &fakeDaemon{}
	path := writeRunnerConfig(t, d.serve(t).URL, root)
	added, err := AddFolder(context.Background(), path, filepath.Join(root, "client", "app"), "web", "test")
	if err != nil {
		t.Fatal(err)
	}
	if added.Allowed {
		t.Error("a repository the scan already lists was allowed again")
	}
	if cfg, _ := LoadConfig(path); len(cfg.Roots) != 1 {
		t.Errorf("roots = %v", cfg.Roots)
	}
	if d.added[0].Name != "web" {
		t.Errorf("name not sent: %+v", d.added[0])
	}
}

func TestAddFolderExplainsWhatWentWrong(t *testing.T) {
	root, _ := newTree(t)
	notes := filepath.Join(root, "notes")
	if _, err := AddFolder(context.Background(), filepath.Join(t.TempDir(), "none.json"), notes, "", "test"); err == nil ||
		!strings.Contains(err.Error(), "Settings") {
		t.Errorf("not registered: %v", err)
	}
	d := &fakeDaemon{}
	path := writeRunnerConfig(t, d.serve(t).URL, root)
	if _, err := AddFolder(context.Background(), path, notes, "", "test"); err == nil || !strings.Contains(err.Error(), "git init") {
		t.Errorf("not a repository: %v", err)
	}
	d.status, d.body = http.StatusConflict, `{"error":"there is already a project called shop"}`
	if _, err := AddFolder(context.Background(), path, filepath.Join(root, "shop"), "", "test"); err == nil ||
		!strings.Contains(err.Error(), "already a project called shop") {
		t.Errorf("the daemon's refusal should reach the person: %v", err)
	}
	d.status, d.body = http.StatusNotFound, "404 page not found"
	if _, err := AddFolder(context.Background(), path, filepath.Join(root, "shop"), "", "test"); err == nil ||
		!strings.Contains(err.Error(), "update it") {
		t.Errorf("an older daemon: %v", err)
	}
}

func TestReloadPicksUpFoldersAllowedMeanwhile(t *testing.T) {
	root, outside := newTree(t)
	path := writeRunnerConfig(t, "http://unused", root)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Allow(path, []string{outside}); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.folderAllowed(outside); ok {
		t.Fatal("allowed before the reload")
	}
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.folderAllowed(outside); !ok {
		t.Error("a folder allowed while the runner runs did not count after Reload")
	}
	if cfg.Credential != "c" || cfg.DaemonURL != "http://unused" {
		t.Errorf("Reload touched more than the folders: %+v", cfg)
	}
}
