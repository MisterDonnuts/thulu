package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// The folders a runner allows and the repositories inside them reach the dashboard only through
// what the runner reports here; the daemon never sees the machine's disk.

const maxInventoryBody = 2 << 20

func (s *Server) handleRunnerInventory(w http.ResponseWriter, r *http.Request) {
	rn := callingRunner(r)
	var inv runner.Inventory
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxInventoryBody)).Decode(&inv); err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.server.errors.invalid_body")
		return
	}
	inv = cleanInventory(inv, time.Now().UTC())
	if err := s.runnerStore.SaveInventory(r.Context(), rn.ID, inv); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	s.adoptMappedFolders(r.Context(), rn, inv.Mapped)
	w.WriteHeader(http.StatusNoContent)
}

// Projects a runner maps in its local config (`thulu runner add-project`, or `thulu init` before
// folders lived in the dashboard) get that folder recorded once, so they show it and move to the new
// flow without anyone touching them.
func (s *Server) adoptMappedFolders(ctx context.Context, rn *runner.Runner, mapped map[string]string) {
	if s.projectStore == nil {
		return
	}
	for id, path := range mapped {
		_, err := s.projectStore.AdoptRunnerPath(ctx, rn.WorkspaceID, rn.ID, id, path)
		logx.Warn(err, "api.runners.log.adopt_folder_failed", "project", id, "runner", rn.ID)
	}
}

func cleanPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) {
		return "", false
	}
	return filepath.Clean(p), true
}

func cleanInventory(in runner.Inventory, now time.Time) runner.Inventory {
	out := runner.Inventory{Roots: []string{}, Repos: []runner.InventoryRepo{}, Mapped: map[string]string{},
		Truncated: in.Truncated, ScannedAt: now}
	for _, root := range in.Roots {
		if c, ok := cleanPath(root); ok && !slices.Contains(out.Roots, c) && len(out.Roots) < runner.MaxInventoryRoots {
			out.Roots = append(out.Roots, c)
		}
	}
	for id, path := range in.Mapped {
		if c, ok := cleanPath(path); ok && strings.TrimSpace(id) != "" && len(out.Mapped) < runner.MaxInventoryRepos {
			out.Mapped[strings.TrimSpace(id)] = c
		}
	}
	seen := map[string]bool{}
	for _, repo := range in.Repos {
		c, ok := cleanPath(repo.Path)
		if !ok || seen[c] || !runner.Within(out.Roots, c) {
			continue
		}
		if len(out.Repos) >= runner.MaxInventoryRepos {
			out.Truncated = true
			break
		}
		seen[c] = true
		out.Repos = append(out.Repos, runner.InventoryRepo{
			Path:   c,
			Name:   clip(strings.TrimSpace(repo.Name), 200),
			Branch: clip(strings.TrimSpace(repo.Branch), 250),
			Origin: clip(strings.TrimSpace(repo.Origin), 500),
		})
	}
	slices.SortFunc(out.Repos, func(a, b runner.InventoryRepo) int { return strings.Compare(a.Path, b.Path) })
	out.Engines = cleanEngines(in.Engines)
	out.LocalTools = cleanLocalTools(in.LocalTools)
	out.Admission = cleanAdmission(in.Admission)
	return out
}

// cleanAdmission keeps whether the machine takes runs, to show it; a state the daemon does not know
// is dropped.
func cleanAdmission(in *runner.AdmissionState) *runner.AdmissionState {
	if in == nil {
		return nil
	}
	switch in.State {
	case runner.AdmissionAccepting, runner.AdmissionPausedLowMemory:
	default:
		return nil
	}
	return &runner.AdmissionState{State: in.State, AvailableMB: max(in.AvailableMB, 0), MinFreeMB: max(in.MinFreeMB, 0),
		Since: in.Since.UTC()}
}

// The daemon keeps what the machine says about its local_tools only to show it; a value it does not
// know is dropped.
func cleanLocalTools(in *runner.LocalToolsState) *runner.LocalToolsState {
	if in == nil {
		return nil
	}
	switch in.Mode {
	case runner.LocalToolsThulu, runner.LocalToolsSelected, runner.LocalToolsAll:
	default:
		return nil
	}
	out := &runner.LocalToolsState{Mode: in.Mode, BypassDisabled: in.BypassDisabled, AllowedTools: max(in.AllowedTools, 0)}
	for _, n := range in.MCP {
		if n = clip(strings.TrimSpace(n), 200); n != "" && !slices.Contains(out.MCP, n) && len(out.MCP) < maxLocalMCP {
			out.MCP = append(out.MCP, n)
		}
	}
	return out
}

const maxLocalMCP = 100

// Only engines this daemon could send to a machine are kept, once each.
func cleanEngines(in []runner.EngineState) []runner.EngineState {
	var out []runner.EngineState
	seen := map[string]bool{}
	for i := range in {
		st := in[i]
		if st.Engine == "" || seen[st.Engine] || engine.MachineLoginOf(st.Engine) == nil {
			continue
		}
		seen[st.Engine] = true
		st.Version = clip(strings.TrimSpace(st.Version), 100)
		st.Email = clip(strings.TrimSpace(st.Email), 320)
		st.Org = clip(strings.TrimSpace(st.Org), 200)
		st.Method = clip(strings.TrimSpace(st.Method), 50)
		st.Error = clip(strings.TrimSpace(st.Error), 500)
		out = append(out, st)
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type runnerRepoView struct {
	runner.InventoryRepo
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
}

type runnerReposView struct {
	Roots     []string         `json:"roots"`
	Repos     []runnerRepoView `json:"repos"`
	Truncated bool             `json:"truncated,omitempty"`
	// False for a runner that never reported its folders: it has not started since it registered,
	// or it predates folders in the dashboard.
	Reported  bool       `json:"reported"`
	ScannedAt *time.Time `json:"scanned_at,omitempty"`
	// Set on the machine (runner.json); shown here, never changed from the daemon.
	LocalTools *runner.LocalToolsState `json:"local_tools,omitempty"`
	// Whether the machine takes runs now, as of its last report.
	Admission *runner.AdmissionState `json:"admission,omitempty"`
}

func (s *Server) handleRunnerRepos(w http.ResponseWriter, r *http.Request) {
	if !s.runnersReady(w, r) {
		return
	}
	ws, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	inv, err := s.runnerStore.Inventory(r.Context(), ws, id)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	used := map[string][2]string{}
	if projects, perr := s.projectStore.ListByWorkspace(r.Context(), ws); perr == nil {
		for i := range projects {
			if p := &projects[i]; p.RunnerID == id && p.RunnerPath != "" {
				used[p.RunnerPath] = [2]string{p.ID, p.Name}
			}
		}
	}
	out := runnerReposView{Roots: inv.Roots, Repos: []runnerRepoView{}, Truncated: inv.Truncated,
		Reported: !inv.ScannedAt.IsZero(), LocalTools: inv.LocalTools, Admission: inv.Admission}
	if out.Reported {
		t := inv.ScannedAt
		out.ScannedAt = &t
	}
	for _, repo := range inv.Repos {
		v := runnerRepoView{InventoryRepo: repo}
		if p, ok := used[repo.Path]; ok {
			v.ProjectID, v.ProjectName = p[0], p[1]
		}
		out.Repos = append(out.Repos, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// checkRunnerFolder accepts a folder for a project on rn: absolute, inside a folder the runner
// reported as allowed, and not already another project's on that machine. The runner checks it
// again before a run, against its own config, so this check is for the person, not the defense.
func (s *Server) checkRunnerFolder(ctx context.Context, rn *runner.Runner, path, projectID string) (string, *runner.InventoryRepo, error) {
	clean, ok := cleanPath(path)
	if !ok {
		return "", nil, apperr.Invalid("api.runners.errors.folder_not_absolute", "path", path)
	}
	inv, err := s.runnerStore.Inventory(ctx, rn.WorkspaceID, rn.ID)
	if err != nil {
		return "", nil, err
	}
	if inv.ScannedAt.IsZero() {
		return "", nil, apperr.Precondition("api.runners.errors.no_folders_reported", "runner", rn.Name)
	}
	if !runner.Within(inv.Roots, clean) {
		return "", nil, apperr.Invalid("api.runners.errors.folder_not_allowed", "path", clean, "runner", rn.Name,
			"roots", strings.Join(inv.Roots, ", "))
	}
	if projects, perr := s.projectStore.ListByWorkspace(ctx, rn.WorkspaceID); perr == nil {
		for i := range projects {
			if p := &projects[i]; p.ID != projectID && p.RunnerID == rn.ID && p.RunnerPath == clean {
				return "", nil, apperr.Conflict("api.runners.errors.folder_taken", "path", clean, "project", p.Name)
			}
		}
	}
	for i := range inv.Repos {
		if inv.Repos[i].Path == clean {
			return clean, &inv.Repos[i], nil
		}
	}
	return clean, nil, nil
}

// handleRunnerAddProject is `thulu add`: someone at the machine made one of its repositories a
// project. The runner reported its folders just before, so the path goes through the same check as
// the dashboard's; a folder that already is a project on this runner answers with that project.
func (s *Server) handleRunnerAddProject(w http.ResponseWriter, r *http.Request) {
	rn := callingRunner(r)
	if s.projectStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.runners.errors.unavailable")
		return
	}
	var req runner.AddProjectRequest
	if !decodeBody(w, r, &req) {
		return
	}
	clean, ok := cleanPath(req.Path)
	if !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.runners.errors.folder_not_absolute", "path", req.Path)
		return
	}
	projects, err := s.projectStore.ListByWorkspace(r.Context(), rn.WorkspaceID)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	body := runnerProjectBody{Name: strings.TrimSpace(req.Name), Path: clean}
	for i := range projects {
		if p := &projects[i]; p.RunnerID == rn.ID && p.RunnerPath == clean {
			writeJSON(w, http.StatusOK, runner.AddProjectResponse{ProjectID: p.ID, ProjectName: p.Name})
			return
		}
	}
	if ferr := s.fillFromRunnerFolder(r.Context(), rn, &body); ferr != nil {
		writeError(w, r, apperr.Status(ferr), ferr)
		return
	}
	for i := range projects {
		if projects[i].Name == body.Name {
			writeErrKey(w, r, http.StatusConflict, "api.runners.errors.add_name_taken", "name", body.Name)
			return
		}
	}
	proj, err := s.projectStore.CreateOnRunner(r.Context(), body.Name, body.RepoURL, rn.ID, project.CreateOpts{
		DefaultBranch: body.DefaultBranch, WorkspaceID: rn.WorkspaceID, RunnerPath: body.Path,
	})
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if s.auditStore != nil {
		actor := auth.Actor{Type: auth.ActorRunner, ID: rn.ID, Label: rn.Name, WorkspaceID: rn.WorkspaceID}
		s.auditStore.LogRequestAs(r, actor, rn.WorkspaceID, audit.KindProjectCreated, proj.ID, "project",
			i18n.Text("api.runners.audit.project_added", "project", proj.Name, "runner", rn.Name, "path", clean),
			map[string]string{"runner_id": rn.ID, "runner_path": clean})
	}
	writeJSON(w, http.StatusCreated, runner.AddProjectResponse{ProjectID: proj.ID, ProjectName: proj.Name, Created: true})
}

// handleRunnerWorktrees tells a runner which task branches are still open, so it can remove the
// worktrees of the closed ones: a runner never hears that a task ended or was deleted. Every project
// of the workspace is listed, so the runner also knows which of its clones still have a project. A
// branch is open while one task of its group (the root task and its subtasks, which share the
// branch) sits outside a terminal stage; a deleted task has no row and no branch. Any failure
// answers an error, never a partial list: the runner reads a missing branch as closed.
func (s *Server) handleRunnerWorktrees(w http.ResponseWriter, r *http.Request) {
	rn := callingRunner(r)
	if s.projectStore == nil || s.taskStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.runners.errors.unavailable")
		return
	}
	ctx := r.Context()
	projects, err := s.projectStore.ListByWorkspace(ctx, rn.WorkspaceID)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	out := runner.WorktreesResponse{Projects: make(map[string][]string, len(projects))}
	openRoot := map[string]bool{}
	for i := range projects {
		branches := []string{}
		if projects[i].RunnerID == rn.ID {
			if branches, err = s.openTaskBranches(ctx, projects[i].ID, openRoot); err != nil {
				writeError(w, r, apperr.Status(err), err)
				return
			}
		}
		out.Projects[projects[i].ID] = branches
	}
	writeJSON(w, http.StatusOK, out)
}

// openTaskBranches lists the task branches of a project whose group still has a task outside a
// terminal stage. openRoot caches the answer by root task across projects.
func (s *Server) openTaskBranches(ctx context.Context, projectID string, openRoot map[string]bool) ([]string, error) {
	tasks, err := s.taskStore.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	branches := []string{}
	seen := map[string]bool{}
	for i := range tasks {
		root := tasks[i].SandboxRootID()
		if seen[root] {
			continue
		}
		seen[root] = true
		open, known := openRoot[root]
		if !known {
			done, derr := s.taskStore.AllInGroupDone(ctx, root)
			if derr != nil {
				return nil, derr
			}
			open = !done
			openRoot[root] = open
		}
		if open {
			branches = append(branches, project.TaskBranchName(root))
		}
	}
	slices.Sort(branches)
	return branches, nil
}
