package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type conversationItem struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`

	Message *task.Message `json:"message,omitempty"`

	RunID      string     `json:"run_id,omitempty"`
	Agent      string     `json:"agent,omitempty"`
	Stage      string     `json:"stage,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Turns      int        `json:"turns,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
	Live       bool       `json:"live,omitempty"`
}

type conversationResponse struct {
	Items     []conversationItem `json:"items"`
	LiveRunID string             `json:"live_run_id,omitempty"`
}

func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.taskForCaller(w, r, id)
	if !ok {
		return
	}
	msgs, err := s.taskStore.ListMessages(r.Context(), id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	items := make([]conversationItem, 0, len(msgs)+4)
	for i := range msgs {
		items = append(items, conversationItem{Kind: "message", At: msgs[i].CreatedAt, Message: &msgs[i]})
	}

	live := t.CurrentRunID
	seen := map[string]bool{}
	if s.usageStore != nil {
		runs := s.usageStore.SessionsForTask(r.Context(), s.usageScope(r), id)
		for i := range runs {
			seen[runs[i].ID] = true
			items = append(items, runItem(&runs[i], runs[i].ID == live))
		}
	}
	// A running run has no usage row until it ends, so it is added from the task.
	if live != "" && !seen[live] {
		items = append(items, conversationItem{
			Kind: "run", At: time.Now().UTC(), RunID: live,
			Agent: t.AssignedAgent(t.Stage), Stage: string(t.Stage), Live: true,
		})
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].At.Before(items[j].At) })
	writeJSON(w, http.StatusOK, conversationResponse{Items: items, LiveRunID: live})
}

func runItem(sess *usage.Session, live bool) conversationItem {
	// Prefer AgentType: Agent is the container name, e.g. "developer-mention-<task>-<hash>-<epoch>"
	// for mention runs.
	it := conversationItem{
		Kind: "run", At: sess.StartedAt, RunID: sess.ID,
		Agent: sess.AgentType, Stage: sess.Stage,
		Turns: sess.Turns, StopReason: sess.StopReason, Live: live,
	}
	if !sess.FinishedAt.IsZero() {
		f := sess.FinishedAt
		it.FinishedAt = &f
	}
	if it.Agent == "" {
		it.Agent = sess.Agent
	}
	return it
}

func (s *Server) handleMessageToComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.taskForCaller(w, r, id); !ok {
		return
	}
	m, err := s.taskStore.GetMessage(r.Context(), id, r.PathValue("msg"))
	if err != nil {
		writeErrKey(w, r, http.StatusNotFound, "api.tasks.errors.message_not_found")
		return
	}
	// Only a person's own message may become their comment; it keeps the signature from the
	// writer's credential.
	if m.Role != task.RoleUser || m.Author == "" {
		writeErrKey(w, r, http.StatusConflict, "api.tasks.errors.message_not_from_person")
		return
	}
	c, err := s.taskStore.AddComment(r.Context(), id, task.SourceUser, m.Author, m.Body)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	logx.Warn(s.taskStore.UpdateMessageDelivery(r.Context(), m.ID, "comment", m.Agent, m.RunID, m.ChatID),
		"api.tasks.log.stamp_comment_delivery", "message", m.ID)
	writeJSON(w, http.StatusCreated, c)
}
