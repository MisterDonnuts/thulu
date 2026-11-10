package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

const (
	prewarmEvery     = 10 * time.Minute
	prewarmFirstWait = time.Minute
	prewarmGlobalCap = 2
	prewarmMaxRun    = 75 * time.Minute
)

type prewarmState struct {
	mu      sync.Mutex
	running map[string]bool
	sem     chan struct{}
	root    context.Context
}

func (p *prewarmState) reserve(projectID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running == nil {
		p.running = map[string]bool{}
	}
	if p.running[projectID] {
		return false
	}
	p.running[projectID] = true
	return true
}

func (p *prewarmState) release(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, projectID)
}

func (p *prewarmState) isRunning(projectID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running[projectID]
}

func (p *prewarmState) slots() chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sem == nil {
		p.sem = make(chan struct{}, prewarmGlobalCap)
	}
	return p.sem
}

func (p *prewarmState) rootCtx() context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.root
}

func (s *Server) StartSetupPrewarms(ctx context.Context) {
	if s.sandbox.mgr == nil || s.projectStore == nil {
		return
	}
	s.prewarm.mu.Lock()
	s.prewarm.root = ctx
	s.prewarm.mu.Unlock()
	go func() {
		s.cleanupPrewarms(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(prewarmFirstWait):
		}
		s.prewarmRound(ctx)
		t := time.NewTicker(prewarmEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.prewarmRound(ctx)
			}
		}
	}()
}

func (s *Server) cleanupPrewarms(ctx context.Context) {
	n, err := s.projectStore.FailStalePrewarms(ctx, time.Now(), nil, i18n.Text("api.prewarm.interrupted"))
	logx.Warn(err, "api.prewarm.log.cleanup_failed")
	if n > 0 {
		log.Printf("[prewarm] %s", i18n.Log("api.prewarm.log.closed_stale", "count", n))
	}
	if s.sandbox.store == nil {
		return
	}
	all, err := s.sandbox.store.ListAll(ctx)
	if err != nil {
		logx.Warn(err, "api.prewarm.log.cleanup_failed")
		return
	}
	for _, sb := range all {
		if _, ok := sandbox.PrewarmProjectID(sb.TaskID); ok {
			logx.Warn(s.sandbox.mgr.Destroy(ctx, sb.TaskID), "api.prewarm.log.cleanup_failed")
		}
	}
}

func (s *Server) prewarmRound(ctx context.Context) {
	projects, err := s.projectStore.List(ctx)
	if err != nil {
		logx.Warn(err, "api.prewarm.log.list_failed")
		return
	}
	for i := range projects {
		id := projects[i].ID
		sc, err := s.projectStore.InitScript(ctx, id)
		if err != nil || !sc.Manifest.HasSetupSnapshot() {
			continue
		}
		s.startPrewarm(id, project.PrewarmReasonLoop, false)
	}
}

func (s *Server) prewarmAfterSave(projectID string) {
	s.startPrewarm(projectID, project.PrewarmReasonSave, false)
}

// Runs under the loop's context, never a request's: a prewarm outlives the request and stops with
// the daemon.
func (s *Server) startPrewarm(projectID, reason string, manual bool) bool {
	ctx := s.prewarm.rootCtx()
	if ctx == nil || s.sandbox.mgr == nil {
		return false
	}
	if !s.prewarm.reserve(projectID) {
		return false
	}
	go func() {
		defer s.prewarm.release(projectID)
		sem := s.prewarm.slots()
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-sem }()
		runCtx, cancel := context.WithTimeout(ctx, prewarmMaxRun)
		defer cancel()
		s.runPrewarm(runCtx, projectID, reason, manual)
	}()
	return true
}

func (s *Server) runPrewarm(ctx context.Context, projectID, reason string, manual bool) {
	proj, err := s.projectStore.Get(ctx, projectID)
	if err != nil {
		return
	}
	sc, err := s.projectStore.InitScript(ctx, projectID)
	if err != nil || !sc.Manifest.HasSetupSnapshot() {
		return
	}
	// The project's read-only token is used here in the daemon and never handed to the sandbox.
	token := s.projectGithubTokenWith(ctx, projectID, githubapp.PermsClone)
	logx.Warn(s.projectStore.FetchDefaultBranch(ctx, projectID, token), "api.prewarm.log.fetch_failed", "project", projectID)

	key, commit, err := s.projectStore.SetupKeyAtDefault(ctx, projectID, sc.Manifest)
	if err != nil {
		logx.Warn(err, "api.prewarm.log.key_failed", "project", projectID)
		return
	}
	history, err := s.projectStore.ListPrewarms(ctx, projectID, 0)
	if err != nil {
		logx.Warn(err, "api.prewarm.log.list_failed")
		return
	}
	if due, _ := project.PrewarmDue(sc.Manifest, key, history, time.Now(), manual); !due {
		return
	}
	rec, err := s.projectStore.StartPrewarm(ctx, projectID, key, commit, reason)
	if err != nil {
		if !errors.Is(err, project.ErrPrewarmRunning) {
			logx.Warn(err, "api.prewarm.log.record_failed", "project", projectID)
		}
		return
	}
	log.Printf("[prewarm] %s", i18n.Log("api.prewarm.log.started", "project", proj.Name, "key", shortSetupKey(key), "reason", reason))
	started := time.Now()
	out, res := s.prewarmInSandbox(ctx, proj, sc.Manifest, commit)
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	logx.Warn(s.projectStore.FinishPrewarm(finishCtx, rec.ID, out), "api.prewarm.log.record_failed", "project", projectID)
	s.logPrewarmRun(finishCtx, rec, out, res, time.Since(started))
	log.Printf("[prewarm] %s", i18n.Log("api.prewarm.log.finished", "project", proj.Name, "status", out.Status, "error", out.Error))
}

func (s *Server) logPrewarmRun(ctx context.Context, rec *project.SetupPrewarm, out project.PrewarmOutcome,
	res sandbox.PrewarmResult, took time.Duration) {
	if s.sandbox.store == nil {
		return
	}
	row := sandbox.SetupRun{
		RunKey:      sandbox.SetupRunPrewarm + ":" + rec.ID,
		WorkspaceID: rec.WorkspaceID, ProjectID: rec.ProjectID, Kind: sandbox.SetupRunPrewarm,
		SetupKey: res.Key, ToolsDownloaded: res.ToolsDownloaded, ToolsUsed: res.ToolsUsed,
		ToolsMs: res.ToolsMs, SetupMs: res.SetupMs, TotalMs: took.Milliseconds(),
		ToolsStatus: prewarmToolsStatus(res),
	}
	if row.SetupKey == "" {
		row.SetupKey = rec.SetupKey
	}
	switch {
	case out.Status == project.PrewarmOK && res.Reused:
		row.Status = sandbox.SetupStateSkipped
	case out.Status == project.PrewarmOK:
		row.Status = sandbox.SetupStateRan
	case out.Status == project.PrewarmFailed:
		row.Status, row.Error = sandbox.SetupStateFailed, out.Error
	default:
		return
	}
	_, err := s.sandbox.store.RecordSetupRun(ctx, row)
	logx.Warn(err, "api.prewarm.log.record_failed", "project", rec.ProjectID)
}

func prewarmToolsStatus(res sandbox.PrewarmResult) string {
	switch {
	case res.ToolsFailed:
		return sandbox.ToolsFailed
	case res.ToolsUnavailable:
		return sandbox.ToolsUnavailable
	case len(res.ToolsUsed) == 0 && len(res.ToolsDownloaded) == 0:
		return ""
	}
	return sandbox.ToolsStatusOf("installed", res.ToolsDownloaded)
}

func (s *Server) prewarmInSandbox(ctx context.Context, proj *project.Project, m project.InitManifest,
	commit string) (project.PrewarmOutcome, sandbox.PrewarmResult) {
	spec := sandbox.PrewarmSpec{
		WorkspaceID: proj.WorkspaceID,
		ProjectID:   proj.ID,
		Name:        proj.Name,
		Dir:         filepath.Base(proj.Path),
		Manifest:    m,
	}
	if s.sandbox.mgr.MountsHostPaths() {
		dir, err := s.projectStore.PrewarmSourceDir(proj.ID)
		if err == nil {
			err = s.projectStore.ExtractDefaultBranch(ctx, proj.ID, commit, dir)
		}
		if dir != "" {
			defer func() { logx.Warn(os.RemoveAll(dir), "api.prewarm.log.cleanup_failed") }()
		}
		if err != nil {
			return project.PrewarmOutcome{Status: project.PrewarmFailed, Error: apperr.Text(err)}, sandbox.PrewarmResult{}
		}
		spec.HostPath = dir
	} else {
		spec.Archive = func() (io.ReadCloser, error) {
			return s.projectStore.OpenDefaultBranchArchive(ctx, proj.ID, commit)
		}
	}
	res, err := s.sandbox.mgr.Prewarm(ctx, spec)
	switch {
	case errors.Is(err, sandbox.ErrPrewarmQueued):
		return project.PrewarmOutcome{Status: project.PrewarmSkipped, Error: apperr.Text(err)}, res
	case err != nil:
		return project.PrewarmOutcome{Status: project.PrewarmFailed, Error: apperr.Text(err)}, res
	}
	out := project.PrewarmOutcome{Status: project.PrewarmOK, SnapshotKey: res.Key, SizeBytes: res.SizeBytes, Pruned: res.Pruned}
	if res.ToolsUnavailable {
		out.Error = i18n.Text("api.prewarm.tools_unavailable")
	}
	return out, res
}

func shortSetupKey(k string) string {
	if len(k) > 12 {
		return k[:12]
	}
	return k
}

type prewarmStatus struct {
	Available bool                   `json:"available"`
	Eligible  bool                   `json:"eligible"`
	Running   bool                   `json:"running"`
	Prewarms  []project.SetupPrewarm `json:"prewarms"`
}

func (s *Server) handleGetProjectPrewarm(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	out := prewarmStatus{Available: s.sandbox.mgr != nil, Prewarms: []project.SetupPrewarm{}}
	if sc, err := s.projectStore.InitScript(r.Context(), proj.ID); err == nil {
		out.Eligible = sc.Manifest.HasSetupSnapshot()
	} else if !errors.Is(err, project.ErrNoInitScript) {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	list, err := s.projectStore.ListPrewarms(r.Context(), proj.ID, 10)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	out.Prewarms = list
	out.Running = s.prewarm.isRunning(proj.ID)
	for i := range list {
		if list[i].Status == project.PrewarmRunning {
			out.Running = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePostProjectPrewarm(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if s.sandbox.mgr == nil || s.prewarm.rootCtx() == nil {
		writeError(w, r, http.StatusServiceUnavailable, apperr.Unavailable("api.prewarm.errors.unavailable"))
		return
	}
	sc, err := s.projectStore.InitScript(r.Context(), proj.ID)
	if err != nil && !errors.Is(err, project.ErrNoInitScript) {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if err != nil || !sc.Manifest.HasSetupSnapshot() {
		writeError(w, r, http.StatusUnprocessableEntity, apperr.Invalid("api.prewarm.errors.not_eligible"))
		return
	}
	if !s.startPrewarm(proj.ID, project.PrewarmReasonManual, true) {
		writeError(w, r, http.StatusConflict, project.ErrPrewarmRunning)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "project_id": proj.ID})
}
