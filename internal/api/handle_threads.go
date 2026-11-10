package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Applying and resolving threads are people's calls: runKeyMayProceed keeps agent runs off these
// routes.

func applyThreadMessage(ctx context.Context, th task.Thread, note string) string {
	var b strings.Builder
	b.WriteString(i18n.TCtx(ctx, "api.threads.prompts.apply_intro", "thread", th.Title(threadTitleRunes)))
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString(i18n.TCtx(ctx, "api.threads.prompts.apply_note", "note", note))
	}
	b.WriteString(i18n.TCtx(ctx, "api.threads.prompts.apply_transcript",
		"transcript", task.TranscriptIn(i18n.FromContext(ctx), th.Messages(), "", maxThreadTranscriptBytes)))
	return b.String()
}

const threadTitleRunes = 80

func (s *Server) handleApplyThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	th, found := s.taskThread(ctx, t.ID, r.PathValue("threadId"))
	if !found {
		writeErrKey(w, r, http.StatusNotFound, "api.threads.errors.no_such_thread")
		return
	}
	if _, gone, reachable := s.taskDispatcher.ConverseTarget(t); !reachable {
		if gone != "" {
			writeErrKey(w, r, http.StatusConflict, "api.threads.errors.agent_gone", "agent", gone)
			return
		}
		writeErrKey(w, r, http.StatusConflict, "api.threads.errors.no_session_yet")
		return
	}
	res, err := s.taskDispatcher.Converse(launchCtx(r.Context()), t, applyThreadMessage(ctx, th, body.Note), author.Name, false)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	title := th.Title(threadTitleRunes)
	_, err = s.taskStore.AddNote(ctx, t.ID, th.Root.ID, task.SourceSystem, "octopus",
		task.NoteOf("api.threads.comments.applied", "author", author.Name, "detail", res.Detail))
	logx.Warn(err, "api.threads.log.note_applied", "thread", th.Root.ID, "task", t.ID)
	logx.Warn(s.taskStore.RecordEvent(ctx, t.ID, task.EventThreadApplied, th.Root.ID, author.Label(),
		i18n.TCtx(ctx, "api.threads.timeline.applied", "title", title, "agent", res.Agent, "detail", res.Detail),
		map[string]string{"thread_id": th.Root.ID, "delivery": string(res.Delivery), "agent": res.Agent}),
		"api.threads.log.record_applied_event", "task", t.ID)
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleResolveThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	var body struct {
		Resolved *bool `json:"resolved"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Resolved == nil {
		writeErrKey(w, r, http.StatusBadRequest, "api.threads.errors.resolved_required")
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	threadID := r.PathValue("threadId")
	err := s.taskStore.ResolveThread(r.Context(), t.ID, threadID, author.Label(), *body.Resolved)
	if errors.Is(err, task.ErrNoSuchThread) {
		writeErrKey(w, r, http.StatusNotFound, "api.threads.errors.no_such_thread")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	th, _ := s.taskThread(r.Context(), t.ID, threadID)
	title := th.Title(threadTitleRunes)
	summary := i18n.TCtx(r.Context(), "api.threads.timeline.reopened", "who", author.Label(), "title", title)
	if *body.Resolved {
		summary = i18n.TCtx(r.Context(), "api.threads.timeline.resolved", "who", author.Label(), "title", title)
	}
	logx.Warn(s.taskStore.RecordEvent(r.Context(), t.ID, task.EventThreadResolved, threadID, author.Label(),
		summary, map[string]any{"thread_id": threadID, "resolved": *body.Resolved}),
		"api.threads.log.record_resolved_event", "task", t.ID)
	writeJSON(w, http.StatusOK, th.Root)
}
