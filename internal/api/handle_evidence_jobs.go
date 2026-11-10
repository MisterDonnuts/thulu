package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const evidenceUploadLimit = attachmentUploadLimit

func (s *Server) evidenceFilesDir() string {
	return filepath.Join(filepath.Dir(s.taskStore.AttachmentsDir()), "evidence")
}

func (s *Server) runnerBatch(w http.ResponseWriter, r *http.Request) (ws, batch string, ok bool) {
	if s.evidenceStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.evidence.errors.unavailable")
		return "", "", false
	}
	batch = r.PathValue("batch")
	scope := auth.APIKeyRunScope(r.Context())
	if !auth.IsAPIKeyAuthed(r.Context()) || scope.Stage != evidence.RunnerStage ||
		scope.Agent != evidence.RunnerAgentPrefix+batch {
		writeErrKey(w, r, http.StatusForbidden, "api.evidence.errors.runner_only")
		return "", "", false
	}
	ws, _ = s.callerWorkspace(r)
	if ws == "" {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.no_workspace")
		return "", "", false
	}
	return ws, batch, true
}

func (s *Server) handleEvidenceJobNext(w http.ResponseWriter, r *http.Request) {
	ws, batch, ok := s.runnerBatch(w, r)
	if !ok {
		return
	}
	run, err := s.evidenceStore.ClaimNextRun(r.Context(), ws, batch)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	if run == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	env, err := s.evidenceStore.GetEnvironment(r.Context(), ws, run.EnvironmentID)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	secrets, err := s.evidenceStore.Secrets(r.Context(), ws, env.ID)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	// Batches queued before BaseURL was frozen on the batch have none; fall back to the
	// environment's.
	baseURL := env.BaseURL
	sandboxed := false
	if b, err := s.evidenceStore.BatchHeader(r.Context(), ws, batch); err == nil {
		if b.BaseURL != "" {
			baseURL = b.BaseURL
		}
		sandboxed = b.SandboxStatus != ""
	}
	writeJSON(w, http.StatusOK, evidence.Job{
		Run:  evidence.JobRun{ID: run.ID, CaseKey: run.CaseKey, Title: run.CaseTitle},
		Take: run.Take, BaseURL: baseURL, AllowedHosts: env.AllowedHosts, Secrets: secrets,
		Sandbox: sandboxed, Environment: evidence.JobEnvironment{Name: env.Label(), Kind: env.Kind},
	})
}

func (s *Server) handleEvidenceJobArtifact(w http.ResponseWriter, r *http.Request) {
	ws, batch, ok := s.runnerBatch(w, r)
	if !ok {
		return
	}
	runID := r.PathValue("run")
	if err := s.evidenceStore.RunAcceptsArtifacts(r.Context(), ws, batch, runID); err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, evidenceUploadLimit+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body"))
		return
	}
	kind := r.FormValue("kind")
	if !evidence.ValidArtifactKind(kind) {
		writeErrKey(w, r, http.StatusBadRequest, "api.evidence.errors.artifact_kind", "kind", kind)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.file_field_required")
		return
	}
	defer file.Close()
	a, err := s.storeEvidenceArtifact(r, ws, runID, kind, header.Filename, header.Header.Get("Content-Type"), file)
	if err != nil {
		status := apperr.Status(err)
		if task.IsStorageQuotaExceeded(err) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, r, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": a.ID})
}

func (s *Server) storeEvidenceArtifact(r *http.Request, ws, runID, kind, filename, mimeType string, src io.Reader) (*evidence.Artifact, error) {
	ctx := r.Context()
	root := s.evidenceFilesDir()
	rel := filepath.Join(ws, runID)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	if err := safepath.MkdirAll(root, rel, 0o750); err != nil {
		return nil, err
	}
	filename = task.SanitizeFilename(filename)
	if filename == "" {
		filename = kind
	}
	relPath := filepath.Join(rel, fmt.Sprintf("%s-%s", kind, filename))
	guard, err := s.taskStore.ReserveWorkspaceStorage(ctx, ws)
	if err != nil {
		return nil, err
	}
	defer guard.Done()
	dst, err := safepath.Create(root, relPath)
	if err != nil {
		return nil, err
	}
	written, copyErr := io.Copy(dst, guard.Limit(io.LimitReader(src, evidenceUploadLimit+1)))
	if closeErr := dst.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil && written > evidenceUploadLimit {
		copyErr = apperr.TooLarge("api.tasks.errors.file_too_large")
	}
	if copyErr == nil {
		copyErr = guard.Check(written)
	}
	full := filepath.Join(root, relPath)
	if copyErr != nil {
		logx.Warn(os.Remove(full), "api.evidence.log.remove_failed", "path", full)
		return nil, copyErr
	}
	if mimeType == "" || mimeType == "application/octet-stream" {
		if byExt := mime.TypeByExtension(filepath.Ext(filename)); byExt != "" {
			mimeType = byExt
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return s.evidenceStore.AddArtifact(ctx, evidence.Artifact{
		WorkspaceID: ws, RunID: runID, Kind: kind, Filename: filename, MimeType: mimeType, Size: written, Path: full,
	})
}

func (s *Server) handleEvidenceJobResult(w http.ResponseWriter, r *http.Request) {
	ws, batch, ok := s.runnerBatch(w, r)
	if !ok {
		return
	}
	var res evidence.RunResult
	if !decodeBody(w, r, &res) {
		return
	}
	run, err := s.evidenceStore.FinishRun(r.Context(), ws, batch, r.PathValue("run"), res)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": run.ID, "status": run.Status})
}

func (s *Server) callerRecording(w http.ResponseWriter, r *http.Request) (*evidence.Recording, bool) {
	if s.evidenceStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.evidence.errors.unavailable")
		return nil, false
	}
	scope := auth.APIKeyRunScope(r.Context())
	if scope.ChatID == "" {
		writeErrKey(w, r, http.StatusForbidden, "api.evidence.errors.recording_chat_only")
		return nil, false
	}
	ws, _ := s.callerWorkspace(r)
	rec, err := s.evidenceStore.OpenRecordingForChat(r.Context(), ws, scope.ChatID)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return nil, false
	}
	return rec, true
}

func (s *Server) handleEvidenceRecording(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.callerRecording(w, r)
	if !ok {
		return
	}
	c, err := s.evidenceStore.GetCase(r.Context(), rec.WorkspaceID, rec.CaseID, false)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	env, err := s.evidenceStore.GetEnvironment(r.Context(), rec.WorkspaceID, rec.EnvironmentID)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recording": rec,
		"case": map[string]any{"key": c.Key, "title": c.Title, "description": c.Description, "steps": c.Steps,
			"expected": c.Expected, "has_take": c.HasTake},
		"environment": map[string]any{"id": env.ID, "name": env.Name, "display_name": env.DisplayName,
			"kind": env.Kind, "project_name": env.ProjectName, "is_production": env.IsProduction,
			"base_url": env.BaseURL, "allowed_hosts": env.AllowedHosts, "secret_names": env.SecretNames},
	})
}

func (s *Server) handleEvidenceRecordingSecret(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.callerRecording(w, r)
	if !ok {
		return
	}
	secrets, err := s.evidenceStore.Secrets(r.Context(), rec.WorkspaceID, rec.EnvironmentID)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	name := r.PathValue("name")
	v, found := secrets[name]
	if !found {
		writeErrKey(w, r, http.StatusNotFound, "api.evidence.errors.secret_not_found", "name", name)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "value": v})
}

func (s *Server) handleEvidenceRecordingTake(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.callerRecording(w, r)
	if !ok {
		return
	}
	author, ok := requireCredentialAuthor(w, r)
	if !ok {
		return
	}
	var body struct {
		Take json.RawMessage `json:"take"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	take, err := evidence.ValidateTake(body.Take)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	ctx := r.Context()
	c, err := s.evidenceStore.SetTake(ctx, rec.WorkspaceID, rec.CaseID, author, take, evidence.SourceAgent)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	logx.Warn(s.evidenceStore.MarkRecordingSaved(ctx, rec.ID), "api.evidence.log.recording_save_failed", "recording", rec.ID)
	spec := evidence.BatchSpec{EnvironmentID: rec.EnvironmentID, CaseIDs: []string{c.ID},
		Trigger: evidence.TriggerRecording, TriggerRef: author}
	var b *evidence.Batch
	if s.evidenceSvc != nil {
		b, err = s.evidenceSvc.Queue(ctx, rec.WorkspaceID, author, spec)
	} else {
		b, err = s.evidenceStore.CreateBatch(ctx, rec.WorkspaceID, author, spec)
	}
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	c.Take = nil
	out := map[string]any{"case": c, "batch": b}
	if env, envErr := s.evidenceStore.GetEnvironment(ctx, rec.WorkspaceID, rec.EnvironmentID); envErr == nil {
		out["environment"] = map[string]string{"id": env.ID, "name": env.Label()}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRecordEvidenceCase(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	if s.chatSvc == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.evidence.errors.chat_unavailable")
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	var body struct {
		EnvironmentID string `json:"environment_id"`
		AgentType     string `json:"agent_type"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	ctx := r.Context()
	c, err := s.evidenceStore.GetCase(ctx, ws, r.PathValue("case"), false)
	if err != nil {
		writeEvidenceErr(w, r, err)
		return
	}
	env, ok := s.evidenceEnv(w, r, ws, body.EnvironmentID)
	if !ok || !allowedOn(w, r, env, evidence.ActionRecord) {
		return
	}
	if !evidence.CaseRunsOn(c.ProjectID, *env) {
		writeEvidenceErr(w, r, apperr.Conflict("evidence.errors.case_not_on_env", "keys", c.Key, "env", env.Label()))
		return
	}
	agentType := strings.TrimSpace(body.AgentType)
	if agentType == "" {
		_, agentType = s.recordingAgents(ctx, ws)
	}
	if agentType == "" {
		writeErrKey(w, r, http.StatusPreconditionFailed, "api.evidence.errors.no_agent")
		return
	}
	// Check credential and chat prompt before CreateChat: otherwise the first message fails and an
	// empty chat is left behind on every try.
	if at := s.lookupAgentType(ws, agentType); at != nil {
		if readyErr := s.taskDispatcher.LLMReady(ctx, ws, at, at.Name); readyErr != nil {
			writeError(w, r, http.StatusPreconditionFailed, readyErr)
			return
		}
		if stage := string(types.StageChat); !s.agentHasPrompt(ctx, ws, at.Name, stage) {
			writeError(w, r, http.StatusPreconditionFailed,
				apperr.Precondition("chatruntime.errors.no_prompt", "agent", at.Name, "stage", stage))
			return
		}
	}
	title := i18n.TCtx(ctx, "api.evidence.recording.chat_title", "key", c.Key, "title", c.Title)
	ch, err := s.chatSvc.CreateChat(ctx, ws, agentType, "", title, chatCreator(r, author))
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	dropChat := func() {
		logx.Warn(s.chatSvc.DeleteChat(ctx, ch.ID), "api.evidence.log.drop_recording_chat_failed", "chat", ch.ID)
	}
	rec, err := s.evidenceStore.CreateRecording(ctx, ws, c.ID, env.ID, ch.ID, author.Name)
	if err != nil {
		dropChat()
		writeEvidenceErr(w, r, err)
		return
	}
	msg := recordingInstructions(r, c, env)
	brief := map[string]string{"brief": "evidence_recording", "case": c.Key, "env": env.Label()}
	if _, err := s.chatSvc.PostMessageWith(ctx, ch.ID, author.Name, msg, brief); err != nil {
		dropChat()
		writeError(w, r, apperr.Status(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"recording": rec, "chat_id": ch.ID})
}

type recordingAgent struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Browser     bool   `json:"browser"`
	Ready       bool   `json:"ready"`
	Reason      string `json:"reason,omitempty"`
	PromptReady bool   `json:"prompt_ready"`
}

func (a recordingAgent) usable() bool { return a.Ready && a.PromptReady }

func (s *Server) recordingAgents(ctx context.Context, ws string) (agents []recordingAgent, auto string) {
	agents = []recordingAgent{}
	src := s.agents(ws)
	if src == nil {
		return agents, ""
	}
	all, err := src.All()
	if err != nil {
		return agents, ""
	}
	for i := range all {
		at := &all[i]
		if !at.SupportsChat() {
			continue
		}
		a := recordingAgent{Type: at.Type, Name: at.Name, Ready: true,
			Browser:     types.MCPWired(at.MCPByStage[string(types.StageChat)], "thulu-browser"),
			PromptReady: s.agentHasPrompt(ctx, ws, at.Name, string(types.StageChat))}
		if s.taskDispatcher != nil {
			if err := s.taskDispatcher.LLMReady(ctx, ws, at, at.Name); err != nil {
				a.Ready, a.Reason = false, apperr.LocalizeCtx(ctx, err)
			}
		}
		agents = append(agents, a)
	}
	return agents, pickRecordingAgent(agents)
}

func pickRecordingAgent(agents []recordingAgent) string {
	for _, want := range []func(recordingAgent) bool{
		func(a recordingAgent) bool { return a.usable() && a.Browser },
		recordingAgent.usable,
		func(a recordingAgent) bool { return a.Ready && a.Browser },
		func(a recordingAgent) bool { return a.Ready },
		func(recordingAgent) bool { return true },
	} {
		for _, a := range agents {
			if want(a) {
				return a.Type
			}
		}
	}
	return ""
}

func (s *Server) handleEvidenceRecordingAgents(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.evidenceWorkspace(w, r)
	if !ok {
		return
	}
	agents, auto := s.recordingAgents(r.Context(), ws)
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents, "auto": auto})
}

func recordingInstructions(r *http.Request, c *evidence.Case, env *evidence.Environment) string {
	ctx := r.Context()
	var b strings.Builder
	b.WriteString(i18n.TCtx(ctx, "api.evidence.recording.intro", "key", c.Key, "title", c.Title, "env", env.Label(), "url", env.BaseURL))
	b.WriteString("\n\n")
	for _, part := range []struct{ key, text string }{
		{"api.evidence.recording.context", c.Description},
		{"api.evidence.recording.steps", c.Steps},
		{"api.evidence.recording.expected", c.Expected},
	} {
		if strings.TrimSpace(part.text) == "" {
			continue
		}
		b.WriteString("**" + i18n.TCtx(ctx, part.key) + "**\n" + strings.TrimSpace(part.text) + "\n\n")
	}
	if len(env.SecretNames) > 0 {
		b.WriteString(i18n.TCtx(ctx, "api.evidence.recording.secrets", "names", strings.Join(env.SecretNames, ", ")) + "\n\n")
	}
	b.WriteString(i18n.TCtx(ctx, "api.evidence.recording.howto"))
	return b.String()
}
