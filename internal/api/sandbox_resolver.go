package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Mirrors the dispatcher's mount logic so the sandbox sees the same worktrees and paths as the
// agent.
// Unknown tasks error instead of getting a container; covers tasks deleted while a session is live.
func (s *Server) resolveTaskWorkspace(ctx context.Context, taskID string) (string, string, string, map[string]string, error) {
	if s.taskStore == nil || s.projectStore == nil {
		return taskID, "", "", nil, nil
	}
	t, err := s.taskStore.Get(ctx, taskID)
	if err != nil || t == nil {
		return "", "", "", nil, apperr.NotFound("api.sandbox.errors.task_id_not_found", "task", taskID)
	}
	owner := t
	if t.IsSubtask() {
		if parent, perr := s.taskStore.Get(ctx, t.ParentID); perr == nil && parent != nil {
			owner = parent
		}
	}
	projects := map[string]string{}
	for _, projectID := range owner.ProjectIDs {
		path := owner.WorktreePaths[projectID]
		if path == "" {
			continue
		}
		proj, err := s.projectStore.Get(ctx, projectID)
		if err != nil {
			continue
		}
		name := filepath.Base(proj.Path)
		projects[name] = path
	}
	return owner.ID, "", owner.WorkspaceID, projects, nil
}

func (s *Server) newSandboxMCPHandler(mgr *sandbox.Manager) *sandbox.MCPHandler {
	return sandbox.NewMCPHandler(mgr, sandbox.WorkspaceResolverFunc(s.resolveTaskWorkspace)).
		WithAttacher(s.attachBytesToTask).
		WithAuthorizer(sandboxAuthorizer{s}).
		WithInitScripts(sandboxInitScripts{s}).
		WithPreviewRecorder(s.recordSandboxPreview)
}

func (s *Server) recordSandboxPreview(ctx context.Context, taskID string, info sandbox.PreviewInfo) {
	if s.taskStore == nil {
		return
	}
	s.recordEvent(ctx, taskID, task.EventPreviewURL, "", "octopus",
		i18n.Text("api.sandbox.timeline.preview_url", "port", info.Port, "url", info.HumanURL),
		map[string]any{"port": info.Port, "url": info.HumanURL})
}

type sandboxAuthorizer struct{ s *Server }

// One refusal for "no such task" and "not your task" so the gateway can't probe other tenants' task
// ids.
var errSandboxTaskNotFound error = apperr.NotFound("api.sandbox.errors.task_not_found")

func (a sandboxAuthorizer) Authorize(r *http.Request, taskID string) (string, int, error) {
	s := a.s
	if s.taskStore == nil {
		return "", http.StatusServiceUnavailable, apperr.Unavailable("api.sandbox.errors.task_store_unavailable")
	}
	ctx := r.Context()
	t, err := s.taskStore.GetRow(ctx, taskID)
	if scope := auth.APIKeyRunScope(ctx); scope.IsRun() {
		if scope.TaskID == "" {
			return "", http.StatusForbidden, apperr.Forbidden("api.sandbox.errors.run_without_task")
		}
		// One answer for "not yours" and "does not exist" so a run can't tell real task ids from
		// garbage.
		wrongTask := apperr.Forbidden("api.sandbox.errors.key_other_task")
		if err != nil || t == nil {
			return "", http.StatusForbidden, wrongTask
		}
		if scope.TaskID != taskID && !s.sameSandbox(ctx, scope.TaskID, t) {
			return "", http.StatusForbidden, wrongTask
		}
		wid, unrestricted := s.callerWorkspace(r)
		if !workspaceVisible(t.WorkspaceID, wid, unrestricted) {
			return "", http.StatusForbidden, wrongTask
		}
		return a.Principal(r), http.StatusOK, nil
	}
	if err != nil || t == nil {
		return "", http.StatusNotFound, errSandboxTaskNotFound
	}
	wid, unrestricted := s.callerWorkspace(r)
	if !workspaceVisible(t.WorkspaceID, wid, unrestricted) {
		return "", http.StatusNotFound, errSandboxTaskNotFound
	}
	return a.Principal(r), http.StatusOK, nil
}

func (s *Server) sameSandbox(ctx context.Context, keyTaskID string, target *task.Task) bool {
	keyTask, err := s.taskStore.GetRow(ctx, keyTaskID)
	if err != nil || keyTask == nil {
		return false
	}
	return sandboxOwnerID(keyTask) == sandboxOwnerID(target)
}

// Same rule resolveTaskWorkspace applies; keep them in sync.
func sandboxOwnerID(t *task.Task) string {
	if t.IsSubtask() {
		return t.ParentID
	}
	return t.ID
}

func (sandboxAuthorizer) Principal(r *http.Request) string {
	return callerPrincipal(r)
}

// API keys bind by key hash; sessions by user and workspace, because the access token rotates every
// few minutes while an SSE stream stays open longer.
func callerPrincipal(r *http.Request) string {
	ctx := r.Context()
	if auth.IsAPIKeyAuthed(ctx) {
		sum := sha256.Sum256([]byte(presentedAPIKey(r)))
		return "key:" + hex.EncodeToString(sum[:])
	}
	if c := auth.ClaimsFromContext(ctx); c != nil {
		return "user:" + c.UserID + "@" + c.WorkspaceID
	}
	return "anonymous"
}

// Must read the key the same way the auth middleware does.
func presentedAPIKey(r *http.Request) string {
	//i18n:ignore HTTP auth scheme, not a message
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
