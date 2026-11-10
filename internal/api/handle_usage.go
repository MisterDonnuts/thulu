package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

// Match the full id, not a prefix: K8s run ids ("refiner-task-<id>-<stage>-<ts>") collapse to the
// agent name under a prefix match and return another run's log.
func findLogFileByContainerID(dataDir, containerID string) string {
	if len(containerID) < 8 {
		return ""
	}
	logsDir := filepath.Join(dataDir, "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return ""
	}
	suffix := "_" + containerID + ".log"
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), suffix) {
			return filepath.Join(logsDir, e.Name())
		}
	}
	return ""
}

func (s *Server) usageScope(r *http.Request) usage.Scope {
	wid, unrestricted := s.callerWorkspace(r)
	if unrestricted {
		return usage.Everything()
	}
	return usage.InWorkspace(wid)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	sc := s.usageScope(r)
	data := map[string]any{
		"totals":   s.usageStore.Totals(r.Context(), sc),
		"by_agent": s.usageStore.ByAgent(r.Context(), sc),
		"all_time": s.usageStore.RollupTotals(r.Context(), sc),
	}
	if rl := s.usageStore.GetRateLimit(sc); rl != nil {
		data["rate_limit"] = rl
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleUsageSessions(w http.ResponseWriter, r *http.Request) {
	page, ok := pageFromQuery(w, r)
	if !ok {
		return
	}
	sessions, next, err := s.usageStore.SessionsPage(r.Context(), s.usageScope(r), page)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	setNextCursor(w, next)
	respondRows(w, r, sessions, nil)
}

// Another tenant's session answers the same 404 as a missing one, so per-session routes cannot
// probe for run ids.
func (s *Server) sessionForCaller(w http.ResponseWriter, r *http.Request, id string) (*usage.Session, bool) {
	return forCaller(s, w, r,
		func() (*usage.Session, error) {
			if sess := s.usageStore.Get(r.Context(), id); sess != nil {
				return sess, nil
			}
			return nil, errSessionNotFound
		},
		func(sess *usage.Session) string { return sess.WorkspaceID },
		workspaceVisible,
		func(error) error { return errSessionNotFound })
}

var errSessionNotFound error = apperr.NotFound("api.usage.errors.session_not_found")

// A run no session or live runtime can place is visible only to unrestricted callers: otherwise
// anyone guessing a pod name gets a live transcript.
func (s *Server) runForCaller(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	wid, unrestricted := s.callerWorkspace(r)
	if id != "" && workspaceVisible(s.runWorkspace(r.Context(), id), wid, unrestricted) {
		return id, true
	}
	writeError(w, r, http.StatusNotFound, errNoLogs)
	return "", false
}

// Wired as the usage store's WorkspaceResolver: "" leaves the session invisible to scoped callers,
// so both task and project are tried.
func (s *Server) sessionWorkspace(ctx context.Context, taskID, projectID string) string {
	if wid := s.taskWorkspace(ctx, taskID); wid != "" {
		return wid
	}
	if projectID != "" && s.projectStore != nil {
		if p, err := s.projectStore.Get(ctx, projectID); err == nil {
			return p.WorkspaceID
		}
	}
	return ""
}

func (s *Server) taskWorkspace(ctx context.Context, taskID string) string {
	if taskID == "" || s.taskStore == nil {
		return ""
	}
	t, err := s.taskStore.GetRow(ctx, taskID)
	if err != nil {
		return ""
	}
	return t.WorkspaceID
}

// The event hub's topic resolver calls this with no request deadline to borrow.
const runWorkspaceLookup = 5 * time.Second

// Pod labels are DNS-1123 sanitized: trust the label's workspace only after the task lookup misses,
// so a rewritten id resolves to nothing rather than a near match.
func (s *Server) runWorkspace(ctx context.Context, id string) string {
	if sess := s.usageStore.Get(ctx, id); sess != nil && sess.WorkspaceID != "" {
		return sess.WorkspaceID
	}
	ctx, cancel := context.WithTimeout(ctx, runWorkspaceLookup)
	defer cancel()

	if runner.IsRunnerRun(id) {
		if s.runnerStore == nil {
			return ""
		}
		j, err := s.runnerStore.GetJob(ctx, runner.JobIDFromRun(id))
		if err != nil {
			return ""
		}
		return j.WorkspaceID
	}
	if s.kube != nil {
		p, ok := s.kube.FindPod(ctx, id)
		if !ok {
			return ""
		}
		if wid := s.taskWorkspace(ctx, p.TaskID); wid != "" {
			return wid
		}
		return p.WorkspaceID
	}
	if s.registry == nil {
		return ""
	}
	a, err := s.registry.Get(id)
	if err != nil || a == nil {
		return ""
	}
	return s.dockerRunWorkspace(ctx, a)
}

func (s *Server) sessionLogPath(ctx context.Context, id string) string {
	if sess := s.usageStore.Get(ctx, id); sess != nil && sess.LogsFile != "" {
		return sess.LogsFile
	}
	return findLogFileByContainerID(s.usageStore.DataDir(), id)
}

func (s *Server) handleSessionLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := s.runForCaller(w, r)
	if !ok {
		return
	}
	logPath := s.sessionLogPath(r.Context(), id)
	if logPath == "" {
		writeError(w, r, http.StatusNotFound, errNoLogs)
		return
	}
	//nolint:gosec // G304: path is server-written (usage DB / server-scanned logs dir); request only selects a session by id, can't inject a path
	data, err := os.ReadFile(logPath)
	if err != nil {
		writeErrKey(w, r, http.StatusInternalServerError, "api.usage.errors.logs_unreadable")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func (s *Server) handleSessionLogsStream(w http.ResponseWriter, r *http.Request) {
	id, ok := s.runForCaller(w, r)
	if !ok {
		return
	}
	src, err := s.resolveSessionLogs(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	conn := newSSEConn(w, r)
	if conn == nil {
		return
	}
	stop := conn.heartbeat(r.Context())
	defer stop()
	src.Pump(r.Context(), conn.line)
}

func (s *Server) handleSessionFiles(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if sess.OutputDir == "" {
		writeErrKey(w, r, http.StatusNotFound, "api.usage.errors.no_output_dir")
		return
	}
	files, err := listFiles(sess.OutputDir)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleSessionServeFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}

	filePath := r.PathValue("path")
	if strings.Contains(filePath, "..") {
		writeErrKey(w, r, http.StatusBadRequest, "api.usage.errors.invalid_path")
		return
	}
	// safepath opens through os.Root so filePath cannot escape OutputDir via a symlink.
	f, err := safepath.Open(sess.OutputDir, filePath)
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.usage.errors.file_not_found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		writeErrKey(w, r, http.StatusNotFound, "api.usage.errors.file_not_found")
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}
