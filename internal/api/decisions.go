package api

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// A person's action on a task (move, send back, relaunch, waive) may carry a reason. With one, the
// daemon posts it as a comment of kind decision, as the person, answering the task's open stop when
// there is one, so the next agent and Activity read why without matching comments to moves by time.

const maxDecisionReasonRunes = 4000

// personCredential: a person signed in (session or dev mode), not an agent's run key nor an
// operator key an automation uses.
func personCredential(r *http.Request, a auth.Author) bool {
	return !a.Agent && !auth.IsAPIKeyAuthed(r.Context())
}

// decisionReason trims the reason and checks it: too long is refused for anyone, missing is refused
// only when required and the caller is a person. requiredKey is the error that names the action.
func decisionReason(w http.ResponseWriter, r *http.Request, author auth.Author, raw *string, required bool,
	requiredKey string,
) (string, bool) {
	reason := ""
	if raw != nil {
		reason = strings.TrimSpace(*raw)
	}
	if utf8.RuneCountInString(reason) > maxDecisionReasonRunes {
		writeErrKey(w, r, http.StatusBadRequest, "api.tasks.errors.reason_too_long", "max", maxDecisionReasonRunes)
		return "", false
	}
	if reason == "" && required && personCredential(r, author) {
		writeErrKey(w, r, http.StatusBadRequest, requiredKey)
		return "", false
	}
	return reason, true
}

// postDecision publishes reason as a decision comment answering stopID (a root comment when "").
// It returns the comment id, "" when there was no reason or the write failed.
func (s *Server) postDecision(ctx context.Context, taskID, stopID string, author auth.Author, reason string,
	meta task.CommentMeta,
) string {
	if reason == "" {
		return ""
	}
	c, err := s.taskStore.PostComment(ctx, task.NewComment{
		TaskID: taskID, ThreadID: stopID, Source: commentSource(author), Author: author.Name, Body: reason,
		Kind: task.CommentDecision, Meta: &meta,
	})
	if err != nil {
		logx.Warn(err, "api.tasks.log.post_decision", "task", taskID)
		return ""
	}
	return c.ID
}

func (s *Server) openStop(ctx context.Context, taskID string) string {
	id, err := s.taskStore.OpenStopThread(ctx, taskID)
	logx.Warn(err, "api.tasks.log.open_stop", "task", taskID)
	return id
}

// movesBackOn reports whether moving the task from one stage to another goes back on its board. A
// stage the board does not know, or a virtual one, never counts as back.
func (s *Server) movesBackOn(t *task.Task, from, to task.Stage) bool {
	stages := s.stagesFor(t)
	f, okFrom := stages.Get(from)
	tt, okTo := stages.Get(to)
	return okFrom && okTo && !f.Virtual && !tt.Virtual && tt.Position < f.Position
}

// agentTurnsPerThread is the workspace's cap on agents replying to each other in one thread.
func (s *Server) agentTurnsPerThread(ctx context.Context, workspaceID string) int {
	if s.settingsStore == nil {
		return settings.DefaultAgentTurnsPerThread
	}
	return s.settingsStore.AgentTurnsPerThread(ctx, workspaceID)
}
