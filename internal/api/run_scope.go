package api

import (
	"context"
	"net/http"
	"slices"
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
	case route == "/tasks/batch-move":
		return s.runMayMoveBatch(w, r, scope)

	case route == "/tasks/{id}/deploy-watches", route == "/tasks/{id}/deploy-watches/{watchId}":
		return s.runMayUseDeployWatches(w, r, scope)

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
		return runMayUseOwnChat(w, r, scope, read)

	case strings.HasPrefix(route, "/evidence/") && !read:
		return s.runMayWriteEvidence(w, r, scope, route)

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

// No lookup: "not your chat" and "no such chat" answer alike so a run can't enumerate chat ids.
func runMayUseOwnChat(w http.ResponseWriter, r *http.Request, scope auth.RunScope, read bool) bool {
	if scope.ChatID == "" || r.PathValue("id") != scope.ChatID {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_own_chat_only")
		return false
	}
	if !read {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_chat_read_only")
		return false
	}
	return true
}

// Evidence environments hold sealed logins and the URL the next batch types them into, so a
// prompt-injected run that could repoint base_url would collect them. Runs only read Evidence;
// the writers are a batch's runner key on its own batch, a recording chat saving its take, and a
// run whose octopus-api grant names evidence_batch_run queueing a batch (its handler narrows it).
// Their handlers check the exact credential again.
func (s *Server) runMayWriteEvidence(w http.ResponseWriter, r *http.Request, scope auth.RunScope, route string) bool {
	switch {
	case strings.HasPrefix(route, "/evidence/jobs/{batch}/"):
		if scope.Agent == evidence.RunnerAgentPrefix+r.PathValue("batch") {
			return true
		}
	case route == "/evidence/recording/take":
		if scope.ChatID != "" {
			return true
		}
	case route == "/evidence/environments/{id}/run":
		if s.runNamesTool(r.Context(), scope, octopusAPIServer, toolEvidenceBatchRun) {
			return true
		}
	}
	writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_evidence_scope")
	return false
}

// A batch move reaches other people's tasks, so a run needs octopus-api's tasks_move_batch named in
// its grant (no scope implies it). The board travels in the body: the handler holds a run to the
// board of its own task (runOwnBoard).
func (s *Server) runMayMoveBatch(w http.ResponseWriter, r *http.Request, scope auth.RunScope) bool {
	if !s.runNamesTool(r.Context(), scope, octopusAPIServer, toolTasksMoveBatch) {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_tool_not_named",
			"tool", toolTasksMoveBatch, "server", octopusAPIServer)
		return false
	}
	return true
}

// A deploy watch opens release tasks from then on without anyone asking, so a run reaches the
// watches only with the matching deploy_watch_* tool named in its grant, and only through its own
// task: the projects the watch can land on are that task's.
func (s *Server) runMayUseDeployWatches(w http.ResponseWriter, r *http.Request, scope auth.RunScope) bool {
	tool := toolDeployWatchList
	switch r.Method {
	case http.MethodPut:
		tool = toolDeployWatchSet
	case http.MethodDelete:
		tool = toolDeployWatchDelete
	}
	if !s.runNamesTool(r.Context(), scope, octopusAPIServer, tool) {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.run_tool_not_named", "tool", tool, "server", octopusAPIServer)
		return false
	}
	if scope.TaskID == "" || r.PathValue("id") != scope.TaskID {
		writeErrKey(w, r, http.StatusForbidden, errRunTaskScope)
		return false
	}
	return true
}

// runOwnBoard is the board of the run's own task, or "" for a key that is not a task run's.
func (s *Server) runOwnBoard(ctx context.Context, scope auth.RunScope) string {
	if !scope.IsRun() || scope.TaskID == "" || s.taskStore == nil {
		return ""
	}
	own, err := s.taskStore.GetRow(ctx, scope.TaskID)
	if err != nil || own == nil {
		return ""
	}
	return own.BoardID
}

// The octopus-api tools a run must be granted by name for the daemon to let its key through:
// granting a scope never reaches them.
const (
	octopusAPIServer     = "octopus-api"
	toolTasksMoveBatch   = "tasks_move_batch"
	toolEvidenceBatchRun = "evidence_batch_run"

	toolDeployWatchSet    = "deploy_watch_set"
	toolDeployWatchList   = "deploy_watch_list"
	toolDeployWatchDelete = "deploy_watch_delete"
)

// runNamesTool reports whether the run's grant for server lists tool by name. It reads the grant the
// run launched with, as the MCP proxy enforces it; a scope ("read", "write") never counts.
func (s *Server) runNamesTool(ctx context.Context, scope auth.RunScope, server, tool string) bool {
	entry, _, ok := s.mcpProxyGrant(ctx, scope, auth.APIKeyWorkspace(ctx), server)
	return ok && slices.Contains(entry.Tools, tool)
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
