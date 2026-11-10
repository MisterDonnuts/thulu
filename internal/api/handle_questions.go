package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

// run_scope.go refuses answer and cancel to every run key, so an agent cannot answer its own
// question.

func (s *Server) handleAskQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	scope := auth.APIKeyRunScope(r.Context())
	if !scope.IsRun() || scope.ChatID != "" || scope.Agent == "" || scope.Agent == auth.AuditorAgent {
		writeErrKey(w, r, http.StatusForbidden, "api.questions.errors.stage_run_only")
		return
	}
	if scope.TaskID != id {
		writeErrKey(w, r, http.StatusForbidden, "api.questions.errors.own_task_only")
		return
	}
	t, ok := s.taskRowForCaller(w, r, id)
	if !ok {
		return
	}
	// A key outliving its run, or a run of a stage the task left, would park a task nobody is
	// working on.
	if t.Status != task.StatusRunning || t.CurrentRunID == "" || string(t.Stage) != scope.Stage ||
		!strings.HasPrefix(t.CurrentInstance, scope.Agent+"-task-") {
		writeErrKey(w, r, http.StatusConflict, "api.questions.errors.not_running", "status", t.Status)
		return
	}
	// Parking a continuation would resume it as the stage's work, audited and moving the task; it
	// must ask in its chat reply instead.
	if s.taskStore.RunLaunchKind(r.Context(), t.CurrentInstance) == task.LaunchContinuation {
		writeErrKey(w, r, http.StatusConflict, "api.questions.errors.continuation_run")
		return
	}
	var in task.AskInput
	if !decodeBody(w, r, &in) {
		return
	}
	target := task.AskTarget{RunID: t.CurrentRunID, Agent: scope.Agent, Stage: t.Stage}
	if t.SessionAgent == scope.Agent && t.SessionStage == scope.Stage {
		target.SessionID = t.SessionID
	}
	q, err := s.taskStore.AskQuestion(r.Context(), t, target, in)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	webhook.EmitBackground(r.Context(), s.webhookDisp, t.WorkspaceID, webhook.EventQuestionAsked, questionHookData(t, q))
	instruction := i18n.Text("api.questions.prompts.instruction_blocking")
	if !q.Blocking {
		instruction = i18n.Text("api.questions.prompts.instruction_non_blocking", "default", q.Payload.Default)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"question":    q,
		"comment_id":  q.CommentID,
		"instruction": instruction,
	})
}

func (s *Server) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var in task.QuestionAnswers
	if !decodeBody(w, r, &in) {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	q, err := s.taskDispatcher.AnswerQuestion(context.WithoutCancel(r.Context()), t, r.PathValue("qid"), in, author.Name)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	s.emit(r, audit.KindQuestionAnswered, webhook.EventQuestionAnswered, q.ID, "question",
		i18n.Text("api.questions.audit.answered", "task", t.ID, "agent", q.Agent), questionHookData(t, q))
	writeJSON(w, http.StatusOK, q)
}

func (s *Server) handleCancelQuestion(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	author, ok := requireAuthor(w, r)
	if !ok {
		return
	}
	q, err := s.taskDispatcher.CancelQuestion(context.WithoutCancel(r.Context()), t, r.PathValue("qid"), author.Name)
	if err != nil {
		writeError(w, r, apperr.Status(err), err)
		return
	}
	s.emit(r, audit.KindQuestionCanceled, webhook.EventQuestionCanceled, q.ID, "question",
		i18n.Text("api.questions.audit.canceled", "task", t.ID, "agent", q.Agent), questionHookData(t, q))
	writeJSON(w, http.StatusOK, q)
}

// When true the reply is the answer: the caller must not also hand it to the run or launch the
// agents it mentions.
func (s *Server) answerFromThreadReply(r *http.Request, t *task.Task, c *task.Comment) bool {
	if s.taskDispatcher == nil || c.ParentID == "" || c.Source != task.SourceUser {
		return false
	}
	answered, err := s.taskDispatcher.AnswerFromThreadReply(context.WithoutCancel(r.Context()), t, c)
	if err != nil || len(answered) == 0 {
		return false
	}
	for i := range answered {
		q := &answered[i]
		s.emit(r, audit.KindQuestionAnswered, webhook.EventQuestionAnswered, q.ID, "question",
			i18n.Text("api.questions.audit.answered", "task", t.ID, "agent", q.Agent), questionHookData(t, q))
	}
	return true
}

func (s *Server) cancelQuestionsFor(r *http.Request, t *task.Task, reasonKey string) {
	if s.taskDispatcher == nil {
		return
	}
	by := authorLabel(r)
	closed := s.taskDispatcher.CancelOpenQuestions(context.WithoutCancel(r.Context()), t.ID, by, reasonKey)
	for i := range closed {
		q := &closed[i]
		s.emit(r, audit.KindQuestionCanceled, webhook.EventQuestionCanceled, q.ID, "question",
			i18n.Text("api.questions.audit.canceled", "task", t.ID, "agent", q.Agent), questionHookData(t, q))
	}
}

// Leaves out the answers' text on purpose.
func questionHookData(t *task.Task, q *task.Question) map[string]any {
	return map[string]any{
		"task_id":     t.ID,
		"task_title":  t.Title,
		"question_id": q.ID,
		"comment_id":  q.CommentID,
		"agent":       q.Agent,
		"stage":       string(q.Stage),
		"kind":        q.Kind,
		"blocking":    q.Blocking,
		"status":      q.Status,
		"answered_by": q.AnsweredBy,
		"delivery":    q.Delivery,
	}
}

func writeBriefQuestions(b *strings.Builder, r *http.Request, t *task.Task) {
	open := 0
	for i := range t.Questions {
		if t.Questions[i].Status == task.QuestionOpen {
			open++
		}
	}
	if open > 0 {
		b.WriteString(i18n.Text("api.questions.prompts.brief_open_heading"))
		for i := range t.Questions {
			q := &t.Questions[i]
			if q.Status != task.QuestionOpen {
				continue
			}
			for _, item := range q.Payload.Questions {
				b.WriteString(i18n.Text("api.questions.prompts.brief_open_line", "agent", q.Agent, "question", briefLine(item.Question)))
			}
		}
		b.WriteString("\n")
	}
	scope := auth.APIKeyRunScope(r.Context())
	if scope.IsRun() && scope.ChatID == "" && scope.Agent != "" && scope.Agent != auth.AuditorAgent &&
		scope.TaskID == t.ID && scope.Stage == string(t.Stage) {
		b.WriteString(i18n.Text("api.questions.prompts.brief_how_to_ask",
			"per_run", task.MaxQuestionsPerRun, "open", task.MaxOpenQuestionsPerTask))
	}
}
