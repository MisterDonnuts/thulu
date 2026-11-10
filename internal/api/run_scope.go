package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// callerWorkspace and RequireRole don't limit which task a run key acts on; this file does, so a
// prompt-injected run can't act on other tasks.
// Board reads stay workspace-wide on purpose: planners and reviewers read neighboring tasks for
// context.

const errRunTaskScope = "api.scope.errors.run_task_scope"

func (s *Server) confineRunKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := auth.APIKeyRunScope(r.Context())
		if !scope.IsRun() || s.runKeyMayProceed(w, r, scope) {
			next(w, r)
		}
	}
}

func personOnlyWrite(route string) string {
	switch {
	case strings.HasPrefix(route, "/tasks/{id}/flags/{flagId}"),
		strings.HasPrefix(route, "/tasks/{id}/approvals/{aid}"):
		return "api.scope.errors.run_flag_decision"
	case strings.HasPrefix(route, "/tasks/{id}/threads/"):
		return "api.scope.errors.run_thread_decision"
	case strings.HasPrefix(route, "/tasks/{id}/questions/{qid}"):
		return "api.scope.errors.run_question_decision"
	case route == "/tasks/{id}/converse" || strings.HasPrefix(route, "/tasks/{id}/conversation/"):
		return "api.scope.errors.run_conversation"
	case route == "/tasks/{id}/merge", route == "/projects/upload", route == "/projects/{id}/upload":
		// What lands on an upload project's branches is a person's call, like merging a PR.
		return "api.scope.errors.run_merge"
	case heldGithubTool(route) != "":
		// A delegated chat's merge or deployment review is held by holdForConfirmation before
		// reaching here; other runs use their own token.
		return "api.scope.errors.run_github_confirmation"
	}
	return ""
}

// Matches r.Pattern, not the path, so routes added under /tasks/{id} inherit the rule while /tasks
// and /task-types stay out.
func (s *Server) runKeyMayProceed(w http.ResponseWriter, r *http.Request, scope auth.RunScope) bool {
	_, route, _ := strings.Cut(r.Pattern, " ")
	read := isReadMethod(r.Method)
	if !read {
		if refusal := personOnlyWrite(route); refusal != "" {
			writeErrKey(w, r, http.StatusForbidden, refusal)
			return false
		}
	}
	switch {
	case route == "/tasks/{id}/session-snapshot", route == "/tasks/{id}/conversation-transcript":
		return s.requireRunOwnSession(w, r, scope, read)

	case strings.HasPrefix(route, "/tasks/{id}"):
		if read {
			return true
		}
		return s.requireRunTaskAccess(w, r, scope, r.PathValue("id"))

	case route == "/task-attachments/{attachmentId}":
		if read {
			return true
		}
		a, err := s.taskStore.GetAttachment(r.Context(), r.PathValue("attachmentId"))
		if err != nil || a == nil {
			return true // the handler answers the 404
		}
		return s.requireRunTaskAccess(w, r, scope, a.TaskID)

	case route == "/chats" && !read:
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_cannot_open_chats")
		return false

	case strings.HasPrefix(route, "/chats/{id}"):
		// No lookup: "not your chat" and "no such chat" answer alike so a run can't enumerate chat
		// ids.
		if scope.ChatID == "" || r.PathValue("id") != scope.ChatID {
			writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_own_chat_only")
			return false
		}
		if !read {
			writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_chat_read_only")
			return false
		}
		return true

	case strings.HasPrefix(route, "/evidence/") && !read:
		return runMayWriteEvidence(w, r, scope, route)

	case strings.HasPrefix(route, "/usage/sessions/{id}/"),
		strings.HasPrefix(route, "/agents/{id}/logs"),
		strings.HasPrefix(route, "/agents/{id}/files"):
		if s.runMayReadRun(r.Context(), scope, r.PathValue("id")) {
			return true
		}
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_transcripts_scope")
		return false
	}
	return true
}

// Evidence environments hold sealed logins and the URL the next batch types them into, so a
// prompt-injected run that could repoint base_url would collect them. Runs only read Evidence;
// the two writers are a batch's runner key on its own batch and a recording chat saving its take,
// and their handlers check the exact credential again.
func runMayWriteEvidence(w http.ResponseWriter, r *http.Request, scope auth.RunScope, route string) bool {
	switch {
	case strings.HasPrefix(route, "/evidence/jobs/{batch}/"):
		if scope.Agent == evidence.RunnerAgentPrefix+r.PathValue("batch") {
			return true
		}
	case route == "/evidence/recording/take":
		if scope.ChatID != "" {
			return true
		}
	}
	writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_evidence_scope")
	return false
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// A task invisible to the caller passes through so the handler's 404 never reveals another tenant's
// task exists.
func (s *Server) requireRunTaskAccess(w http.ResponseWriter, r *http.Request, scope auth.RunScope, taskID string) bool {
	if s.taskStore == nil || taskID == "" {
		return true
	}
	target, err := s.taskStore.GetRow(r.Context(), taskID)
	if err != nil || target == nil {
		return true
	}
	if wid, unrestricted := s.callerWorkspace(r); !workspaceVisible(target.WorkspaceID, wid, unrestricted) {
		return true
	}
	if s.runAnswersFor(r.Context(), scope, target) {
		return true
	}
	writeErrKey(w, r, http.StatusForbidden, errRunTaskScope)
	return false
}

func (s *Server) requireRunOwnSession(w http.ResponseWriter, r *http.Request, scope auth.RunScope, read bool) bool {
	id := r.PathValue("id")
	agent := r.URL.Query().Get("agent")
	if scope.ChatID == "" && scope.Agent != "" && scope.TaskID != "" && (agent == "" || agent == scope.Agent) {
		if scope.TaskID == id {
			return true
		}
		if read && s.taskStore != nil {
			if t, err := s.taskStore.GetRow(r.Context(), id); err == nil && t != nil && t.SandboxRootID() == scope.TaskID {
				return true
			}
		}
	}
	writeErrKey(w, r, http.StatusForbidden, "api.sessions.errors.run_session_scope")
	return false
}

func (s *Server) runAnswersFor(ctx context.Context, scope auth.RunScope, target *task.Task) bool {
	if target == nil {
		return false
	}
	if scope.Delegated() {
		// Workspace checked here too: runMayReadRun reaches this without a workspace check of its
		// own.
		return target.WorkspaceID != "" && target.WorkspaceID == auth.APIKeyWorkspace(ctx)
	}
	if scope.TaskID == "" {
		return false
	}
	if target.ID == scope.TaskID || target.ParentID == scope.TaskID || target.OriginTaskID == scope.TaskID {
		return true
	}
	own, err := s.taskStore.GetRow(ctx, scope.TaskID)
	if err != nil || own == nil {
		return false
	}
	return sandboxOwnerID(own) == sandboxOwnerID(target)
}

func (s *Server) runMayReadRun(ctx context.Context, scope auth.RunScope, id string) bool {
	taskID := s.runTaskID(ctx, id)
	if taskID == "" || s.taskStore == nil {
		return false
	}
	t, err := s.taskStore.GetRow(ctx, taskID)
	if err != nil || t == nil {
		return false
	}
	return s.runAnswersFor(ctx, scope, t)
}

func (s *Server) runTaskID(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	if s.usageStore != nil {
		if sess := s.usageStore.Get(ctx, id); sess != nil && sess.TaskID != "" {
			return sess.TaskID
		}
	}
	ctx, cancel := context.WithTimeout(ctx, runWorkspaceLookup)
	defer cancel()
	if s.kube != nil {
		if p, ok := s.kube.FindPod(ctx, id); ok {
			return p.TaskID
		}
		return ""
	}
	if s.registry == nil {
		return ""
	}
	if a, err := s.registry.Get(id); err == nil && a != nil {
		return a.TaskID
	}
	return ""
}

// Read from the dispatcher-minted key, never from the request body.
func runOriginTask(r *http.Request) string {
	if scope := auth.APIKeyRunScope(r.Context()); scope.IsRun() {
		return scope.TaskID
	}
	return ""
}
