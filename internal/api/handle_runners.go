package api

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

// Call before Start: runnerRoute.runner is read without a lock.
func (s *Server) SetRunners(store *runner.Store) {
	s.runnerStore = store
	if store == nil || s.runnerRoute == nil {
		return
	}
	// The profile API and the dispatcher must agree: a machine profile is accepted only where its
	// runs can launch.
	s.runners = store
	logsDir := ""
	if s.usageStore != nil {
		logsDir = filepath.Join(s.usageStore.DataDir(), "logs")
	}
	l := newRunnerLauncher(store, s.projectStore, s.agents, s.runsDir, logsDir)
	if s.usageStore != nil {
		l.logSink = usage.NewCollector(nil, s.usageStore, s.usageStore.DataDir()).RecordRun
	}
	if d := s.taskDispatcher; d != nil {
		d.WithRunners(store)
		l.report = d.RecordRunnerReport
		l.reconcile = d.Reconcile
	}
	if s.taskStore != nil {
		l.task = s.taskStore.GetRow
	}
	if s.chatSvc != nil {
		s.chatSvc.Runtime().SetMachineTurns(l)
	}
	s.runnerRoute.runner = l
}

func (s *Server) StartRunners(ctx context.Context) {
	if s.runnerRoute != nil && s.runnerRoute.runner != nil {
		s.runnerRoute.runner.start(ctx)
	}
}

func (s *Server) registerRunnerRoutes(mux *http.ServeMux, rr roleGate) {
	mux.HandleFunc("GET /runners", rr(auth.RoleViewer, s.handleListRunners))
	mux.HandleFunc("POST /runners/tokens", rr(auth.RoleAdmin, s.handleCreateRunnerToken))
	mux.HandleFunc("DELETE /runners/{id}", rr(auth.RoleAdmin, s.handleRevokeRunner))
	mux.HandleFunc("POST /runners/{id}/projects", rr(auth.RoleAdmin, s.handleCreateRunnerProject))
	mux.HandleFunc("GET /runners/{id}/repos", rr(auth.RoleDeveloper, s.handleRunnerRepos))

	mux.HandleFunc("POST "+runnerAPIPrefix+"register", s.handleRunnerRegister)
	mux.HandleFunc("POST "+runnerAPIPrefix+"claim", s.runnerAuth(s.handleRunnerClaim))
	mux.HandleFunc("POST "+runnerAPIPrefix+"jobs/{id}/heartbeat", s.runnerAuth(s.handleRunnerHeartbeat))
	mux.HandleFunc("POST "+runnerAPIPrefix+"jobs/{id}/log", s.runnerAuth(s.handleRunnerLog))
	mux.HandleFunc("PUT "+runnerAPIPrefix+"jobs/{id}/outputs", s.runnerAuth(s.handleRunnerOutputs))
	mux.HandleFunc("POST "+runnerAPIPrefix+"jobs/{id}/steer", s.runnerAuth(s.handleRunnerSteer))
	mux.HandleFunc("PUT "+runnerAPIPrefix+"inventory", s.runnerAuth(s.handleRunnerInventory))
	mux.HandleFunc("POST "+runnerAPIPrefix+"projects", s.runnerAuth(s.handleRunnerAddProject))
	mux.HandleFunc("POST "+runnerAPIPrefix+"jobs/{id}/finish", s.runnerAuth(s.handleRunnerFinish))
}

const runnerAPIPrefix = "/runner/v1/"

// Routes under this prefix bypass the auth middleware; each must go through runnerAuth (register
// checks its token itself).
func isRunnerAPI(p string) bool { return strings.HasPrefix(p, runnerAPIPrefix) }

func isRunnerLongCall(r *http.Request) bool {
	p := r.URL.Path
	return p == runnerAPIPrefix+"claim" ||
		(isRunnerAPI(p) && (strings.HasSuffix(p, "/outputs") || strings.HasSuffix(p, "/log") ||
			strings.HasSuffix(p, "/steer")))
}

func (s *Server) runnersReady(w http.ResponseWriter, r *http.Request) bool {
	if s.runnerStore == nil || s.runnerRoute == nil || s.runnerRoute.runner == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.runners.errors.unavailable")
		return false
	}
	return true
}

func (s *Server) handleListRunners(w http.ResponseWriter, r *http.Request) {
	if !s.runnersReady(w, r) {
		return
	}
	ws, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	list, err := s.runnerStore.List(r.Context(), ws)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if list == nil {
		list = []runner.Runner{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateRunnerToken(w http.ResponseWriter, r *http.Request) {
	if !s.runnersReady(w, r) {
		return
	}
	ws, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	token, expires, err := s.runnerStore.CreateRegistration(r.Context(), ws, body.Name, credentialAuthor(r))
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if s.auditStore != nil {
		s.auditStore.LogRequestIn(r, ws, audit.KindRunnerTokenCreated, "", "runner",
			i18n.Text("api.runners.audit.token_created", "name", strings.TrimSpace(body.Name)), nil)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expires_at": expires, "name": strings.TrimSpace(body.Name)})
}

func (s *Server) handleRevokeRunner(w http.ResponseWriter, r *http.Request) {
	if !s.runnersReady(w, r) {
		return
	}
	ws, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := s.runnerStore.Revoke(r.Context(), ws, id); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if s.auditStore != nil {
		s.auditStore.LogRequestIn(r, ws, audit.KindRunnerRevoked, id, "runner",
			i18n.Text("api.runners.audit.revoked", "runner", id), nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

type runnerProjectBody struct {
	Name           string `json:"name"`
	RepoURL        string `json:"repo_url,omitempty"`
	Description    string `json:"description,omitempty"`
	DefaultBranch  string `json:"default_branch,omitempty"`
	DefaultBoardID string `json:"default_board_id,omitempty"`
	// A folder on the runner's machine. Without it the project waits for
	// `thulu runner add-project` on that machine, as before folders lived here.
	Path string `json:"path,omitempty"`
}

// fillFromRunnerFolder checks the body's folder against what the runner reported and fills what the
// person left empty from it: the name from the folder, the branch and origin from the repository.
func (s *Server) fillFromRunnerFolder(ctx context.Context, rn *runner.Runner, body *runnerProjectBody) error {
	if strings.TrimSpace(body.Path) == "" {
		return nil
	}
	path, repo, err := s.checkRunnerFolder(ctx, rn, body.Path, "")
	if err != nil {
		return err
	}
	body.Path = path
	if strings.TrimSpace(body.Name) == "" {
		body.Name = filepath.Base(path)
	}
	if repo == nil {
		return nil
	}
	if body.DefaultBranch == "" {
		body.DefaultBranch = repo.Branch
	}
	if body.RepoURL == "" {
		body.RepoURL = repo.Origin
	}
	return nil
}

func (s *Server) handleCreateRunnerProject(w http.ResponseWriter, r *http.Request) {
	if !s.runnersReady(w, r) {
		return
	}
	ws, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	var body runnerProjectBody
	if !decodeBody(w, r, &body) {
		return
	}
	rn, err := s.runnerStore.Get(r.Context(), ws, r.PathValue("id"))
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if rn.RevokedAt != nil {
		writeErrKey(w, r, http.StatusConflict, "api.runners.errors.runner_revoked", "runner", rn.Name)
		return
	}
	if ferr := s.fillFromRunnerFolder(r.Context(), rn, &body); ferr != nil {
		writeError(w, r, apperr.Status(ferr), ferr)
		return
	}
	if body.DefaultBoardID != "" && !s.boardVisibleToCaller(r, body.DefaultBoardID) {
		writeErrKey(w, r, http.StatusBadRequest, "api.boards.errors.unknown_board", "board", body.DefaultBoardID)
		return
	}
	proj, err := s.projectStore.CreateOnRunner(r.Context(), body.Name, body.RepoURL, rn.ID, project.CreateOpts{
		Description: body.Description, DefaultBranch: body.DefaultBranch,
		DefaultBoardID: body.DefaultBoardID, WorkspaceID: ws, RunnerPath: body.Path,
	})
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if s.auditStore != nil {
		s.auditStore.LogRequestIn(r, ws, audit.KindProjectCreated, proj.ID, "project",
			i18n.Text("api.runners.audit.project_created", "project", proj.Name, "runner", rn.Name),
			map[string]string{"runner_id": rn.ID})
	}
	writeJSON(w, http.StatusCreated, proj)
}

// patchRunnerFolder checks a new folder against the runner the project will have after the patch.
func (s *Server) patchRunnerFolder(w http.ResponseWriter, r *http.Request, current *project.Project, p *project.Patch) bool {
	if p.RunnerPath == nil || strings.TrimSpace(*p.RunnerPath) == "" {
		return true
	}
	target := current.RunnerID
	if p.RunnerID != nil {
		target = strings.TrimSpace(*p.RunnerID)
	}
	if target == "" {
		writeErrKey(w, r, http.StatusBadRequest, "project.errors.runner_path_without_runner", "name", current.Name)
		return false
	}
	if !s.runnersReady(w, r) {
		return false
	}
	rn, err := s.runnerStore.Get(r.Context(), current.WorkspaceID, target)
	if err == nil && rn.WorkspaceID != current.WorkspaceID {
		err = apperr.NotFound("runner.errors.not_found", "id", target)
	}
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return false
	}
	clean, _, err := s.checkRunnerFolder(r.Context(), rn, *p.RunnerPath, current.ID)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return false
	}
	p.RunnerPath = &clean
	return true
}

func (s *Server) patchRunnerAllowed(w http.ResponseWriter, r *http.Request,
	current *project.Project, runnerID *string,
) (string, bool) {
	if runnerID == nil {
		return "", true
	}
	id := strings.TrimSpace(*runnerID)
	if id == "" || id == current.RunnerID {
		return "", true
	}
	if !s.runnersReady(w, r) {
		return "", false
	}
	rn, err := s.runnerStore.Get(r.Context(), current.WorkspaceID, id)
	if err == nil && rn.WorkspaceID != current.WorkspaceID {
		// Get with an empty workspace is unscoped, so without this an operator project (no
		// workspace) could take any tenant's runner.
		err = apperr.NotFound("runner.errors.not_found", "id", id)
	}
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return "", false
	}
	if rn.RevokedAt != nil {
		writeErrKey(w, r, http.StatusConflict, "api.runners.errors.runner_revoked", "runner", rn.Name)
		return "", false
	}
	return rn.Name, true
}

func (s *Server) auditRunnerChange(r *http.Request, proj *project.Project, previous, runnerName string) {
	if s.auditStore == nil {
		return
	}
	summary := i18n.Text("api.runners.audit.project_runner_cleared", "project", proj.Name)
	switch {
	case proj.RunnerID != "" && proj.RunnerID == previous:
		summary = i18n.Text("api.runners.audit.project_folder_set", "project", proj.Name, "path", proj.RunnerPath)
	case proj.RunnerID != "":
		summary = i18n.Text("api.runners.audit.project_runner_set", "project", proj.Name, "runner", runnerName)
	}
	s.auditStore.LogRequestIn(r, proj.WorkspaceID, audit.KindProjectRunnerChanged, proj.ID, "project", summary,
		map[string]string{"runner_id": proj.RunnerID, "previous_runner_id": previous, "runner_path": proj.RunnerPath})
}

type runnerCtxKey struct{}

func (s *Server) runnerAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.runnerProtocolOK(w, r) {
			return
		}
		cred, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ") //i18n:ignore HTTP auth scheme
		rn, err := s.runnerStore.Authenticate(r.Context(), strings.TrimSpace(cred), r.Header.Get("User-Agent"),
			runner.AcceptedProtocol(r.Header.Get(runner.ProtocolHeader)))
		if err != nil {
			writeError(w, r, apperr.Status(err), err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), runnerCtxKey{}, rn)))
	}
}

// A runner on a protocol the daemon cannot serve must fail loudly, not misread a job. Older ones in
// range keep working: Claim hands them only the jobs they understand.
func (s *Server) runnerProtocolOK(w http.ResponseWriter, r *http.Request) bool {
	if !s.runnersReady(w, r) {
		return false
	}
	if got := r.Header.Get(runner.ProtocolHeader); runner.AcceptedProtocol(got) == 0 {
		writeErrKey(w, r, http.StatusPreconditionFailed, "api.runners.errors.protocol",
			"got", got, "min", runner.ProtocolFolders, "max", runner.ProtocolVersion)
		return false
	}
	return true
}

func callingRunner(r *http.Request) *runner.Runner {
	rn, ok := r.Context().Value(runnerCtxKey{}).(*runner.Runner)
	if !ok {
		return nil
	}
	return rn
}

func (s *Server) handleRunnerRegister(w http.ResponseWriter, r *http.Request) {
	if !s.runnerProtocolOK(w, r) {
		return
	}
	var req runner.RegisterRequest
	if !decodeBody(w, r, &req) {
		return
	}
	resp, err := s.runnerStore.Register(r.Context(), req)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	if s.auditStore != nil {
		s.auditStore.LogRequestAs(r, auth.Actor{Type: auth.ActorRunner, ID: "runner:" + resp.RunnerID, Label: resp.Name},
			resp.WorkspaceID, audit.KindRunnerRegistered, resp.RunnerID, "runner",
			i18n.Text("api.runners.audit.registered", "runner", resp.Name, "host", req.Hostname), nil)
	}
	writeJSON(w, http.StatusCreated, resp)
}

const (
	claimPoll         = time.Second
	claimQueryTimeout = 10 * time.Second
)

func (s *Server) handleRunnerClaim(w http.ResponseWriter, r *http.Request) {
	rn := callingRunner(r)
	var req runner.ClaimRequest
	if !decodeBody(w, r, &req) {
		return
	}
	wait := time.NewTimer(runner.ClaimWait)
	defer wait.Stop()
	tick := time.NewTicker(claimPoll)
	defer tick.Stop()
	for {
		// A runner that hung up must not take a job it can't receive.
		if r.Context().Err() != nil {
			return
		}
		spec, err := s.claimJob(r.Context(), rn, req)
		if err != nil {
			writeError(w, r, apperr.Status(err), err)
			return
		}
		if spec != nil {
			writeJSON(w, http.StatusOK, runner.ClaimResponse{Job: spec,
				LeaseSeconds:     int(runner.DefaultLease / time.Second),
				HeartbeatSeconds: int(runner.DefaultHeartbeat / time.Second)})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wait.C:
			writeJSON(w, http.StatusOK, runner.ClaimResponse{})
			return
		case <-tick.C:
		}
	}
}

// claimJob runs one claim to its end, even if the long poll's wait runs out meanwhile. With the
// wait's context, a claim that committed as the wait ended came back as a timeout: the job stayed
// taken, the runner got an empty answer, and the run was lost a lease later.
func (s *Server) claimJob(ctx context.Context, rn *runner.Runner, req runner.ClaimRequest) (*runner.JobSpec, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimQueryTimeout)
	defer cancel()
	return s.runnerStore.Claim(ctx, rn.ID, rn.Protocol, req.Projects, runner.DefaultLease, req.Kinds...)
}

func (s *Server) handleRunnerHeartbeat(w http.ResponseWriter, r *http.Request) {
	cancel, err := s.runnerStore.Heartbeat(r.Context(), callingRunner(r).ID, r.PathValue("id"), runner.DefaultLease)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	writeJSON(w, http.StatusOK, runner.HeartbeatResponse{Cancel: cancel, LeaseSeconds: int(runner.DefaultLease / time.Second)})
}

const maxLogChunk = 4 << 20

func (s *Server) handleRunnerLog(w http.ResponseWriter, r *http.Request) {
	j, err := s.runnerStore.Held(r.Context(), callingRunner(r).ID, r.PathValue("id"))
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	chunk, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLogChunk))
	if err != nil {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.runners.errors.log_too_large")
		return
	}
	s.runnerRoute.runner.appendLog(j, chunk)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunnerOutputs(w http.ResponseWriter, r *http.Request) {
	j, err := s.runnerStore.Held(r.Context(), callingRunner(r).ID, r.PathValue("id"))
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	dst := runOutputDir(s.runsDir, j.InstanceID)
	if dst == "" {
		writeErrKey(w, r, http.StatusConflict, "api.runners.errors.no_output_dir")
		return
	}
	body := http.MaxBytesReader(w, r.Body, runner.MaxOutputBytes)
	if err := runner.ExtractOutputs(body, dst); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunnerFinish(w http.ResponseWriter, r *http.Request) {
	var req runner.FinishRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.runnerStore.Finish(r.Context(), callingRunner(r).ID, r.PathValue("id"), req); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stopRunnerRun(w http.ResponseWriter, r *http.Request) {
	if !s.runnersReady(w, r) {
		return
	}
	id := r.PathValue("id")
	j, err := s.runnerStore.GetJob(r.Context(), runner.JobIDFromRun(id))
	if err == nil {
		wid, unrestricted := s.callerWorkspace(r)
		if !workspaceVisible(j.WorkspaceID, wid, unrestricted) {
			err = apperr.NotFound("api.agents.errors.agent_not_found", "name", id)
		}
	}
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	s.markStopped(r, id)
	if err := s.runnerStore.RequestCancel(r.Context(), j.ID); err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "agent": j.AgentName})
}
