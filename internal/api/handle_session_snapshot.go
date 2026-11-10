package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

// A snapshot is an agent's full conversation, tool output included: only that agent's run on that
// task may move it
// (run_scope.go requireRunOwnSession, plus the run-only checks here).

const (
	sessionSnapshotMaxEnv     = "OCTOPUS_SESSION_SNAPSHOT_MAX_BYTES"
	defaultSessionSnapshotMax = 50 << 20
	transcriptMaxEnv          = "OCTOPUS_TRANSCRIPT_MAX_BYTES"
	defaultTranscriptMax      = 200 << 10
)

func envBytes(name string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func (s *Server) sessionsRoot() string {
	return task.SessionsRootBeside(s.runsDir)
}

func sessionRunScope(w http.ResponseWriter, r *http.Request) (auth.RunScope, bool) {
	scope := auth.APIKeyRunScope(r.Context())
	if !scope.IsRun() || !task.ValidSessionName(scope.Agent) {
		writeErrKey(w, r, http.StatusForbidden, "api.sessions.errors.run_key_only")
		return auth.RunScope{}, false
	}
	return scope, true
}

func sessionRef(w http.ResponseWriter, r *http.Request, t *task.Task, agent string) (task.SessionRef, bool) {
	sid := r.URL.Query().Get("sid")
	if !task.ValidSessionName(sid) {
		writeErrKey(w, r, http.StatusBadRequest, "api.sessions.errors.bad_sid")
		return task.SessionRef{}, false
	}
	return task.SessionRef{RootTaskID: t.SandboxRootID(), TaskID: t.ID, Agent: agent, SessionID: sid}, true
}

func (s *Server) handlePutSessionSnapshot(w http.ResponseWriter, r *http.Request) {
	scope, ok := sessionRunScope(w, r)
	if !ok {
		return
	}
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	ref, ok := sessionRef(w, r, t, scope.Agent)
	if !ok {
		return
	}
	maxBytes := envBytes(sessionSnapshotMaxEnv, defaultSessionSnapshotMax)
	if r.ContentLength > maxBytes {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.sessions.errors.snapshot_too_large", "max", maxBytes)
		return
	}
	// maxBytes+1 lets the store detect an oversized body.
	body := http.MaxBytesReader(w, r.Body, maxBytes+1)
	n, err := task.SaveSessionSnapshot(s.sessionsRoot(), ref, body, maxBytes)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sid": ref.SessionID, "bytes": n})
}

// The entrypoint reads 404 as "rehydrate from the transcript".
func (s *Server) handleGetSessionSnapshot(w http.ResponseWriter, r *http.Request) {
	scope, ok := sessionRunScope(w, r)
	if !ok {
		return
	}
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	ref, ok := sessionRef(w, r, t, scope.Agent)
	if !ok {
		return
	}
	f, err := task.OpenSessionSnapshot(s.sessionsRoot(), ref)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, ref.SessionID+".tar.gz", info.ModTime(), f)
}

func (s *Server) handleConversationTranscript(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	agent := r.URL.Query().Get("agent")
	scope := auth.APIKeyRunScope(r.Context())
	if scope.IsRun() && agent == "" {
		agent = scope.Agent // run_scope.go refused any other agent
	}
	// The transcript is made of run logs, which an operator key reads under /usage/sessions only
	// with runs:read; this route must not bypass that.
	if !scope.IsRun() && auth.IsAPIKeyAuthed(r.Context()) && !auth.APIKeyGrants(r.Context(), auth.PermRunsRead) {
		writeErrKey(w, r, http.StatusForbidden, "api.sessions.errors.runs_read_required")
		return
	}
	if !task.ValidSessionName(agent) {
		writeErrKey(w, r, http.StatusBadRequest, "api.sessions.errors.bad_agent")
		return
	}
	msgs, err := s.taskStore.ListMessages(r.Context(), t.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	md := task.BuildTranscript(agent, s.transcriptRuns(r, t.ID, agent), msgs,
		int(envBytes(transcriptMaxEnv, defaultTranscriptMax)))
	if md == "" {
		writeErrKey(w, r, http.StatusNotFound, "api.sessions.errors.no_transcript")
		return
	}
	// nosniff comes from middleware so a browser never renders it. ServeContent sets
	// Content-Length, which the entrypoint uses to detect a cut download.
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8") //i18n:ignore HTTP header value
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, strings.NewReader(md))
}

// Docker rows hold the agent type in AgentType and the container name in Agent; Kubernetes rows
// hold the name. Match either.
func (s *Server) transcriptRuns(r *http.Request, taskID, agent string) []task.TranscriptRun {
	if s.usageStore == nil || s.usageStore.DataDir() == "" {
		return nil
	}
	logsDir := filepath.Join(s.usageStore.DataDir(), "logs")
	sessions := s.usageStore.SessionsForTask(r.Context(), s.usageScope(r), taskID)
	var out []task.TranscriptRun
	for i := range sessions {
		sess := &sessions[i]
		if sess.AgentType != agent && sess.Agent != agent {
			continue
		}
		if sess.LogsFile == "" {
			continue
		}
		out = append(out, task.TranscriptRun{
			Stage:     sess.Stage,
			StartedAt: sess.StartedAt,
			Engine:    sess.Engine,
			Open:      runLogOpener(logsDir, sess),
		})
	}
	return out
}

// LogsFile comes from the database: confine it to <data>/logs so a poisoned row cannot read
// arbitrary files.
func runLogOpener(logsDir string, sess *usage.Session) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		rel, err := safepath.Rel(logsDir, sess.LogsFile)
		if err != nil {
			return nil, err
		}
		return safepath.Open(logsDir, rel)
	}
}
