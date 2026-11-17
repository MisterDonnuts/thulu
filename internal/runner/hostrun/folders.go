package hostrun

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// The folders a person allows are the runner's whole reach on their machine: the dashboard picks a
// project's folder from what the runner reports inside them, and the runner refuses any job whose
// folder falls outside them, whatever the daemon sends.

// The folders the person allowed plus the checkouts mapped in the config: those were allowed one by
// one with `thulu runner add-project` (or `thulu init`) before folders lived in the dashboard.
func (c *Config) allowedRoots() []string {
	roots, projects := c.folders()
	out := make([]string, 0, len(roots)+len(projects))
	out = append(out, roots...)
	for _, p := range projects {
		if !runner.Within(roots, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// resolved follows symlinks so a link inside an allowed folder cannot point the agent outside it.
func resolved(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func (c *Config) folderAllowed(path string) (string, bool) {
	target, err := resolved(path)
	if err != nil {
		return "", false
	}
	roots := make([]string, 0, len(c.allowedRoots()))
	for _, r := range c.allowedRoots() {
		if rr, err := resolved(r); err == nil {
			roots = append(roots, rr)
		}
	}
	return target, runner.Within(roots, target)
}

// checkoutFor picks the folder a job's project runs from: the one recorded in the dashboard, if this
// machine allows it, else the one mapped in this runner's config.
func (c *Config) checkoutFor(ctx context.Context, p runner.JobProject) (string, error) {
	if p.Path == "" {
		_, projects := c.folders()
		if local := projects[p.ID]; local != "" {
			return local, nil
		}
		return "", apperr.Precondition("hostrun.errors.project_unmapped", "project", p.Name, "id", p.ID)
	}
	checkout, ok := c.folderAllowed(p.Path)
	if !ok {
		return "", apperr.Precondition("hostrun.errors.folder_not_allowed", "project", p.Name, "path", p.Path)
	}
	if _, err := git(ctx, checkout, "rev-parse", "--git-dir"); err != nil {
		return "", apperr.Precondition("hostrun.errors.folder_not_a_repo", "project", p.Name, "path", p.Path)
	}
	return checkout, nil
}

func Allow(path string, dirs []string) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		abs, err := filepath.Abs(strings.TrimSpace(d))
		if err != nil {
			return apperr.Wrap(err, "hostrun.errors.project_dir", "path", d)
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return apperr.Invalid("hostrun.errors.not_a_folder", "path", abs)
		}
		if !slices.Contains(cfg.Roots, abs) {
			cfg.Roots = append(cfg.Roots, abs)
		}
	}
	return cfg.Save(path)
}

func Disallow(path string, dirs []string) error {
	cfg, err := LoadConfig(path)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		abs, err := filepath.Abs(strings.TrimSpace(d))
		if err != nil {
			return apperr.Wrap(err, "hostrun.errors.project_dir", "path", d)
		}
		cfg.Roots = slices.DeleteFunc(cfg.Roots, func(r string) bool { return r == abs })
	}
	return cfg.Save(path)
}

func AllowedFolders(path string) ([]string, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return cfg.allowedRoots(), nil
}

// How deep under an allowed folder a repository is looked for: ~/code/client/app is found from ~/code.
const scanDepth = 3

//i18n:ignore directory names, never shown
var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "Library": true, "target": true, "dist": true, "build": true}

func isRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// Scan lists the git repositories inside the allowed folders. A repository is not searched further:
// its submodules and vendored checkouts are part of it.
func (c *Config) Scan(ctx context.Context) runner.Inventory {
	roots := c.allowedRoots()
	_, projects := c.folders()
	inv := runner.Inventory{Roots: roots, Repos: []runner.InventoryRepo{}, Mapped: projects}
	if inv.Mapped == nil {
		inv.Mapped = map[string]string{}
	}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if ctx.Err() != nil || inv.Truncated {
			return
		}
		if isRepo(dir) {
			if len(inv.Repos) >= runner.MaxInventoryRepos {
				inv.Truncated = true
				return
			}
			inv.Repos = append(inv.Repos, describeRepo(ctx, dir))
			return
		}
		if depth >= scanDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || skipDirs[name] {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return inv
}

func describeRepo(ctx context.Context, dir string) runner.InventoryRepo {
	repo := runner.InventoryRepo{Path: dir, Name: filepath.Base(dir)}
	if b, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		repo.Branch = b
	}
	if o, err := git(ctx, dir, "remote", "get-url", "origin"); err == nil {
		repo.Origin = redactRemote(o)
	}
	return repo
}

const inventoryEvery = time.Minute

// reportFolders keeps the daemon's copy of this machine's folders and engines fresh while the runner
// runs, and sweeps idle chat folders on the same beat, the first one at start. A daemon from before
// folders in the dashboard answers 404; the runner then stops reporting and works as it did, from its
// config.
func (r *Runner) reportFolders(ctx context.Context) {
	t := time.NewTicker(inventoryEvery)
	defer t.Stop()
	for {
		r.sweepChats(time.Now())
		logx.Warn(r.cfg.Reload(), "hostrun.log.reload_failed")
		inv := r.cfg.Scan(ctx)
		inv.Engines = probeEngines(ctx)
		err := r.client.Inventory(ctx, inv)
		var ce *callError
		if asCallError(err, &ce) && ce.Status == http.StatusNotFound {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.folders_unsupported"))
			return
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.folders_report_failed", "err", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Added is what `thulu add` did: the project the folder is now, and whether the folder had to be
// allowed first.
type Added struct {
	ProjectID   string
	ProjectName string
	Path        string
	URL         string
	Created     bool
	Allowed     bool
}

// AddFolder turns the repository that holds dir into a project on this runner, from the machine
// itself: running the command there is the consent, so the folder is allowed if it was not.
func AddFolder(ctx context.Context, path, dir, name, version string) (*Added, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		if apperr.KindOf(err) == apperr.KindNotFound {
			return nil, apperr.Precondition("hostrun.errors.add_not_registered")
		}
		return nil, err
	}
	abs, err := resolved(strings.TrimSpace(dir))
	if err != nil {
		return nil, apperr.Invalid("hostrun.errors.not_a_folder", "path", dir)
	}
	top, err := git(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, apperr.Invalid("hostrun.errors.add_not_a_repo", "path", abs)
	}
	if top, err = resolved(top); err != nil {
		return nil, apperr.Invalid("hostrun.errors.add_not_a_repo", "path", abs)
	}
	out := &Added{Path: top}
	inv := cfg.Scan(ctx)
	reported := reportedPath(inv, top)
	// Deeper than the scan goes, or under a folder reached through a symlink: allowing the
	// repository itself makes it a root, which the scan always lists.
	if reported == "" {
		cfg.Roots = append(cfg.Roots, top)
		if serr := cfg.Save(path); serr != nil {
			return nil, serr
		}
		out.Allowed = true
		inv = cfg.Scan(ctx)
		if reported = reportedPath(inv, top); reported == "" {
			return nil, apperr.Precondition("hostrun.errors.add_not_listed", "path", top)
		}
	}
	client := NewClient(cfg.DaemonURL, cfg.Credential, version)
	inv.Engines = probeEngines(ctx)
	if ierr := client.Inventory(ctx, inv); ierr != nil {
		return nil, addCallError(ierr)
	}
	resp, err := client.AddProject(ctx, runner.AddProjectRequest{Path: reported, Name: strings.TrimSpace(name)})
	if err != nil {
		return nil, addCallError(err)
	}
	out.ProjectID, out.ProjectName, out.Created = resp.ProjectID, resp.ProjectName, resp.Created
	out.URL = cfg.DaemonURL + "/project/?id=" + url.QueryEscape(resp.ProjectID)
	return out, nil
}

// reportedPath is top as the scan lists it: the daemon compares paths as text, against the folders
// as the person allowed them, symlinks included.
func reportedPath(inv runner.Inventory, top string) string {
	for _, r := range inv.Repos {
		if r.Path == top {
			return r.Path
		}
	}
	for _, r := range inv.Repos {
		if p, err := resolved(r.Path); err == nil && p == top {
			return r.Path
		}
	}
	return ""
}

// The daemon answers with a message for the person; a daemon from before this command answers 404.
func addCallError(err error) error {
	var ce *callError
	if !asCallError(err, &ce) {
		return err
	}
	switch ce.Status {
	case http.StatusNotFound:
		return apperr.Precondition("hostrun.errors.add_unsupported")
	case http.StatusUnauthorized:
		return apperr.Precondition("hostrun.errors.add_revoked")
	}
	if msg := daemonMessage(ce.Body); msg != "" {
		return apperr.Invalid("hostrun.errors.add_refused", "err", msg)
	}
	return err
}

func daemonMessage(body string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(body), &e) == nil {
		return strings.TrimSpace(e.Error)
	}
	return ""
}
