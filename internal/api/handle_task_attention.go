package api

import (
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// taskDetail is GET /tasks/{id}: the task plus what it needs from a person. Only the single-task
// GET carries them; lists stay as they were.
type taskDetail struct {
	*task.Task
	Attention         *task.Attention         `json:"attention"`
	UpcomingApprovals []task.UpcomingApproval `json:"upcoming_approvals"`
	// What the task view pins as waiting for a person; never null.
	Waiting task.Waiting `json:"waiting"`
}

func (s *Server) taskWithAttention(r *http.Request, t *task.Task) taskDetail {
	out := taskDetail{Task: t, UpcomingApprovals: []task.UpcomingApproval{},
		Waiting: task.Waiting{Approvals: []string{}, Questions: []string{}}}
	if s.taskDispatcher == nil {
		return out
	}
	att, upcoming := s.taskDispatcher.Attention(r.Context(), t)
	out.Attention = att
	out.Waiting = s.taskDispatcher.WaitingFor(t)
	if upcoming != nil {
		out.UpcomingApprovals = upcoming
	}
	return out
}

// A waiver is a person's decision: a run's key never records one, or an agent could skip the cases
// it was asked to test.
func waiverByPerson(w http.ResponseWriter, r *http.Request, author string) bool {
	return personOnly(w, r, author, "api.tasks.errors.waive_person_only")
}

// personOnly refuses a run's key and an agent with the 403 key names: some Testing decisions are a
// person's to take.
func personOnly(w http.ResponseWriter, r *http.Request, author, key string) bool {
	if auth.APIKeyRunScope(r.Context()).IsRun() || author == "" || strings.HasPrefix(author, "agent:") {
		writeErrKey(w, r, http.StatusForbidden, key)
		return false
	}
	return true
}

// signedInPersonOnly also refuses an operator or automation key: giving an agent access (secrets
// into an evidence environment) and approving acceptance criteria are what a signed-in person
// does, the same person personCredential asks a reason of.
func signedInPersonOnly(w http.ResponseWriter, r *http.Request, author, key string) bool {
	if auth.IsAPIKeyAuthed(r.Context()) {
		writeErrKey(w, r, http.StatusForbidden, key)
		return false
	}
	return personOnly(w, r, author, key)
}

// POST /tasks/{id}/test-cases/waive {"cases": [...], "reason": "..."}: records a waived run per case
// and, when that settles what the blocked stage left to a person, moves the task on.
func (s *Server) handleWaiveTestCases(w http.ResponseWriter, r *http.Request) {
	t, author, ok := s.testCaseWriteFor(w, r)
	if !ok || !waiverByPerson(w, r, author) {
		return
	}
	var body struct {
		Cases  []string `json:"cases"`
		Reason string   `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if s.taskDispatcher == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.server.errors.dispatcher_unavailable")
		return
	}
	// Read before the waiver: settling the cases or moving the task on resolves the stop the
	// decision answers.
	stop := s.openStop(r.Context(), t.ID)
	res, err := s.taskDispatcher.WaiveTestCases(r.Context(), t, body.Cases, author, body.Reason)
	if err != nil {
		writeTestCaseErr(w, r, err)
		return
	}
	if a, authErr := auth.AuthorFromRequest(r); authErr == nil {
		s.postDecision(r.Context(), t.ID, stop, a, strings.TrimSpace(body.Reason), task.CommentMeta{
			Action: task.DecisionWaive, FromStage: t.Stage, ToStage: res.Stage, Cases: res.Cases,
		})
	}
	writeJSON(w, http.StatusOK, res)
}
