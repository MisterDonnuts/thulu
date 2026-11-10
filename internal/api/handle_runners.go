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
	logsDir := ""
	if s.usageStore != nil {
		logsDir = filepath.Join(s.usageStore.DataDir(), "logs")
	}
	l := newRunnerLauncher(store, s.projectStore, s.agents, s.runsDir, logsDir)
	if s.usageStore != nil {
		l.logSink = usage.NewCollector(nil, s.usageStore, s.usageStore.DataDir()).RecordRun
	}
	if d := s.taskDispatcher; d != nil {
		l.report = d.RecordRunnerReport
		l.reconcile = d.Reconcile
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

	mux.HandleFunc("POST "+runnerAPIPrefix+"register", s.handleRunnerRegister)
	mux.HandleFunc("POST "+runnerAPIPrefix+"claim", s.runnerAuth(s.handleRunnerClaim))
	mux.HandleFunc("POST "+runnerAPIPrefix+"jobs/{id}/heartbeat", s.runnerAuth(s.handleRunnerHeartbeat))
	mux.HandleFunc("POST "+runnerAPIPrefix+"jobs/{id}/log", s.runnerAuth(s.handleRunnerLog))
	mux.HandleFunc("PUT "+runnerAPIPrefix+"jobs/{id}/outputs", s.runnerAuth(s.handleRunnerOutputs))
	mux.HandleFunc("POST "+runnerAPIPrefix+"jobs/{id}/finish", s.runnerAuth(s.handleRunnerFinish))
}

const runnerAPIPrefix = "/runner/v1/"

// Routes under this prefix bypass the auth middleware; each must go through runnerAuth (register
// checks its token itself).
func isRunnerAPI(p string) bool { return strings.HasPrefix(p, runnerAPIPrefix) }

func isRunnerLongCall(r *http.Request) bool {
	p := r.URL.Path
	return p == runnerAPIPrefix+"claim" ||
		(isRunnerAPI(p) && (strings.HasSuffix(p, "/outputs") || strings.HasSuffix(p, "/log")))
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

func (s *Server) handleCreateRunnerProject(w http.ResponseWriter, r *http.Request) {
	if !s.runnersReady(w, r) {
		return
	}
	ws, ok := s.targetWorkspace(w, r)
	if !ok {
		return
	}
	var body struct {
		Name           string `json:"name"`
		RepoURL        string `json:"repo_url,omitempty"`
		Description    string `json:"description,omitempty"`
		DefaultBranch  string `json:"default_branch,omitempty"`
		DefaultBoardID string `json:"default_board_id,omitempty"`
	}
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
	if body.DefaultBoardID != "" && !s.boardVisibleToCaller(r, body.DefaultBoardID) {
		writeErrKey(w, r, http.StatusBadRequest, "api.boards.errors.unknown_board", "board", body.DefaultBoardID)
		return
	}
	proj, err := s.projectStore.CreateOnRunner(r.Context(), body.Name, body.RepoURL, rn.ID, project.CreateOpts{
		Description: body.Description, DefaultBranch: body.DefaultBranch,
		DefaultBoardID: body.DefaultBoardID, WorkspaceID: ws,
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
	if proj.RunnerID != "" {
		summary = i18n.Text("api.runners.audit.project_runner_set", "project", proj.Name, "runner", runnerName)
	}
	s.auditStore.LogRequestIn(r, proj.WorkspaceID, audit.KindProjectRunnerChanged, proj.ID, "project", summary,
		map[string]string{"runner_id": proj.RunnerID, "previous_runner_id": previous})
}

type runnerCtxKey struct{}

func (s *Server) runnerAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.runnerProtocolOK(w, r) {
			return
		}
		cred, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ") //i18n:ignore HTTP auth scheme
		rn, err := s.runnerStore.Authenticate(r.Context(), strings.TrimSpace(cred), r.Header.Get("User-Agent"))
		if err != nil {
			writeError(w, r, apperr.Status(err), err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), runnerCtxKey{}, rn)))
	}
}

// Exact match on purpose: a runner on another protocol version must fail loudly, not misread a job.
func (s *Server) runnerProtocolOK(w http.ResponseWriter, r *http.Request) bool {
	if !s.runnersReady(w, r) {
		return false
	}
	if got := r.Header.Get(runner.ProtocolHeader); got != runner.ProtocolVersion {
		writeErrKey(w, r, http.StatusPreconditionFailed, "api.runners.errors.protocol",
			"got", got, "want", runner.ProtocolVersion)
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

const claimPoll = time.Second

func (s *Server) handleRunnerClaim(w http.ResponseWriter, r *http.Request) {
	rn := callingRunner(r)
	var req runner.ClaimRequest
	if !decodeBody(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), runner.ClaimWait)
	defer cancel()
	tick := time.NewTicker(claimPoll)
	defer tick.Stop()
	for {
		spec, err := s.runnerStore.Claim(ctx, rn.ID, req.Projects, runner.DefaultLease)
		if err != nil && ctx.Err() == nil {
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
		case <-ctx.Done():
			writeJSON(w, http.StatusOK, runner.ClaimResponse{})
			return
		case <-tick.C:
		}
	}
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
