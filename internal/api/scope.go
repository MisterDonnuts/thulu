package api

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Stores stay unscoped for daemon paths; workspace enforcement lives here.
// Claims bind the caller to their workspace even in dev mode; only credential-less dev mode is
// unrestricted.
func (s *Server) callerWorkspace(r *http.Request) (wid string, unrestricted bool) {
	ctx := r.Context()
	if auth.IsAPIKeyAuthed(ctx) {
		if wid := auth.APIKeyWorkspace(ctx); wid != "" {
			return wid, false
		}
		// Per-run keys minted before keys carried a workspace: use their task's; a vanished task
		// reaches nothing.
		if scope := auth.APIKeyTaskScope(ctx); scope != "" {
			if t, err := s.taskStore.GetRow(ctx, scope); err == nil {
				return t.WorkspaceID, false
			}
		}
		return "", false
	}
	if c := auth.ClaimsFromContext(ctx); c != nil {
		return c.WorkspaceID, false
	}
	if auth.DevSession(ctx) {
		return "", true
	}
	return "", false
}

// Unstamped rows are visible only to unrestricted (dev-mode) callers; daemon.workspaceIDBackfill
// stamps what it can place.
func workspaceVisible(resourceWID, wid string, unrestricted bool) bool {
	return unrestricted || (resourceWID != "" && resourceWID == wid)
}

// A run's key is never admin, whatever its row says; auth.RequireRole caps runs at developer.
func callerIsAdmin(r *http.Request) bool {
	ctx := r.Context()
	if auth.IsAPIKeyAuthed(ctx) {
		if auth.APIKeyRunScope(ctx).IsRun() {
			return false
		}
		role := auth.APIKeyRole(ctx)
		return role == auth.RoleAdmin || role == auth.RoleOwner
	}
	if c := auth.ClaimsFromContext(ctx); c != nil {
		return c.Role == auth.RoleAdmin || c.Role == auth.RoleOwner
	}
	return auth.DevSession(ctx)
}

// Unset means only an unrestricted (dev-mode) caller administers the install.
const OperatorWorkspaceEnv = "OCTOPUS_OPERATOR_WORKSPACE"

func (s *Server) callerIsOperator(r *http.Request) bool {
	wid, unrestricted := s.callerWorkspace(r)
	if unrestricted {
		return true
	}
	op := strings.TrimSpace(os.Getenv(OperatorWorkspaceEnv))
	return op != "" && op == wid && callerIsAdmin(r)
}

// Role admin is not a boundary on install-wide surfaces: every workspace has an admin.
func (s *Server) requireOperator(w http.ResponseWriter, r *http.Request, what, verb string) bool {
	if s.callerIsOperator(r) {
		return true
	}
	ctx := r.Context()
	writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.operator_only",
		"what", i18n.TCtx(ctx, what), "verb", i18n.TCtx(ctx, verb), "env", OperatorWorkspaceEnv)
	return false
}

// Fetch errors and scope misses both answer 404 so restricted callers can't probe other tenants'
// resources.
func forCaller[T any](s *Server, w http.ResponseWriter, r *http.Request,
	fetch func() (T, error),
	resourceWID func(T) string,
	visible func(resourceWID, callerWID string, unrestricted bool) bool,
	notFound func(err error) error,
) (T, bool) {
	var zero T
	res, err := fetch()
	if err != nil {
		writeError(w, r, http.StatusNotFound, notFound(err))
		return zero, false
	}
	if wid, unrestricted := s.callerWorkspace(r); !visible(resourceWID(res), wid, unrestricted) {
		writeError(w, r, http.StatusNotFound, notFound(nil))
		return zero, false
	}
	return res, true
}

func (s *Server) projectForCaller(w http.ResponseWriter, r *http.Request, id string) (*project.Project, bool) {
	return forCaller(s, w, r,
		func() (*project.Project, error) { return s.projectStore.Get(r.Context(), id) },
		func(p *project.Project) string { return p.WorkspaceID },
		workspaceVisible,
		func(err error) error {
			if err != nil {
				return err
			}
			return apperr.NotFound("api.scope.errors.project_not_found", "id", id)
		})
}

// A personal chat someone else opened answers 404 like a missing one, admins included.
func (s *Server) chatForCaller(w http.ResponseWriter, r *http.Request, id string) (*chat.Chat, bool) {
	notFound := func(error) error { return apperr.NotFound("api.scope.errors.chat_not_found", "id", id) }
	c, ok := forCaller(s, w, r,
		func() (*chat.Chat, error) { return s.chatSvc.Store().GetChat(r.Context(), id) },
		func(c *chat.Chat) string { return c.WorkspaceID },
		workspaceVisible,
		notFound)
	if ok && !c.VisibleTo(chatViewer(r)) {
		writeError(w, r, http.StatusNotFound, notFound(nil))
		return nil, false
	}
	return c, ok
}

func chatCreator(r *http.Request, author auth.Author) chat.Creator {
	return chat.Creator{Name: author.Name, Owner: auth.OwnerFromContext(r.Context())}
}

func chatViewer(r *http.Request) chat.Viewer { return chatViewerOf(r.Context()) }

func chatViewerOf(ctx context.Context) chat.Viewer {
	v := chat.Viewer{Owner: auth.OwnerFromContext(ctx), RunChatID: auth.APIKeyRunScope(ctx).ChatID}
	if a, err := auth.AuthorFromContext(ctx); err == nil && !a.Agent {
		v.Author = a.Name
	}
	return v
}

func (s *Server) taskForCaller(w http.ResponseWriter, r *http.Request, id string) (*task.Task, bool) {
	return s.fetchTaskForCaller(w, r, id, s.taskStore.Get)
}

func (s *Server) taskRowForCaller(w http.ResponseWriter, r *http.Request, id string) (*task.Task, bool) {
	return s.fetchTaskForCaller(w, r, id, s.taskStore.GetRow)
}

func (s *Server) fetchTaskForCaller(
	w http.ResponseWriter, r *http.Request, id string,
	fetch func(context.Context, string) (*task.Task, error),
) (*task.Task, bool) {
	return forCaller(s, w, r,
		func() (*task.Task, error) { return fetch(r.Context(), id) },
		func(t *task.Task) string { return t.WorkspaceID },
		workspaceVisible,
		func(err error) error {
			if err != nil {
				return err
			}
			return apperr.NotFound("api.scope.errors.task_not_found", "id", id)
		})
}

// Must equal the stage store's fallback workspace: the board it seeds on a fresh install has to be
// visible to an unscoped caller's first request.
const defaultWorkspaceID = stagepkg.FallbackWorkspaceID

// Only unrestricted callers get the floor; flooring a restricted caller with no workspace would
// hand it the sole workspace's credentials and keys.
func (s *Server) requestWorkspace(r *http.Request) (string, bool) {
	wid, unrestricted := s.callerWorkspace(r)
	if wid != "" {
		return wid, true
	}
	if !unrestricted {
		return "", false
	}
	return s.unrestrictedWorkspace(r.Context()), true
}

func (s *Server) workspaceIDFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	wid, ok := s.requestWorkspace(r)
	if !ok {
		writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.no_workspace")
	}
	return wid, ok
}

// Go half of stage.ownerlessWorkspace; change both together.
// Deliberately uncached: a stale "only one" keeps writing into the first workspace after a second
// appears.
func (s *Server) soleWorkspaceID(ctx context.Context) string {
	if s.authStore == nil {
		return ""
	}
	id, err := s.authStore.SoleWorkspaceID(ctx)
	logx.Warn(err, "api.scope.log.read_workspaces_failed")
	return id
}

func (s *Server) noWorkspaces(ctx context.Context) bool {
	if s.authStore == nil {
		return true
	}
	ids, err := s.authStore.ListWorkspaceIDs(ctx)
	logx.Warn(err, "api.scope.log.read_workspaces_failed")
	return err == nil && len(ids) == 0
}

// "default" is not a workspace anyone logs into; prefer the install's sole real workspace so writes
// stay visible.
func (s *Server) unrestrictedWorkspace(ctx context.Context) string {
	if wid := s.soleWorkspaceID(ctx); wid != "" {
		return wid
	}
	return defaultWorkspaceID
}

// ?workspace= is a destination for a caller with none; a scoped caller may only name its own (same
// rule credentialSession applies to ?task=).
func (s *Server) targetWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	named := strings.TrimSpace(r.URL.Query().Get("workspace"))
	wid, unrestricted := s.callerWorkspace(r)
	if !unrestricted {
		if named != "" && named != wid {
			writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.other_workspace")
			return "", false
		}
		if wid == "" {
			// A restricted caller with no workspace has none to aim at; "default" is not its own.
			writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.no_workspace")
			return "", false
		}
		return wid, true
	}
	if named != "" {
		return named, true
	}
	return s.unrestrictedWorkspace(r.Context()), true
}

// "" hides the task and makes the K8s launcher request namespace "tenant-"; never fall back to
// defaultWorkspaceID either, nobody logs into it.
// Only a named board counts: resolveTaskBoard's fallback can be the install's "default" board.
func (s *Server) newTaskWorkspace(w http.ResponseWriter, r *http.Request, projectIDs []string, namedBoard string) (string, bool) {
	wid, decided, ok := s.callerOwnedWorkspace(w, r)
	if !ok {
		return "", false
	}
	if decided {
		return wid, true
	}
	if pw, err := s.projectsWorkspace(r.Context(), projectIDs); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return "", false
	} else if pw != "" {
		return pw, true
	}
	if b, found := s.boardInfo(namedBoard); namedBoard != "" && found && b.WorkspaceID != "" {
		return b.WorkspaceID, true
	}
	writeErrKey(w, r, http.StatusBadRequest, "api.scope.errors.task_workspace_undetermined")
	return "", false
}

// An unowned project is hidden from every tenant and tasks derive their workspace from it, so
// refuse rather than guess.
func (s *Server) newResourceWorkspace(w http.ResponseWriter, r *http.Request, resource string) (string, bool) {
	wid, decided, ok := s.callerOwnedWorkspace(w, r)
	if !ok {
		return "", false
	}
	if decided {
		return wid, true
	}
	writeErrKey(w, r, http.StatusBadRequest, "api.scope.errors.resource_workspace_undetermined",
		"resource", resourceName(r, resource))
	return "", false
}

var resourceNames = map[string]string{
	"project":      "api.scope.resources.project",
	"chat":         "api.scope.resources.chat",
	"policy":       "api.scope.resources.policy",
	"installation": "api.scope.resources.installation",
}

func resourceName(r *http.Request, resource string) string {
	if key, ok := resourceNames[resource]; ok {
		return i18n.TCtx(r.Context(), key)
	}
	return resource
}

func (s *Server) callerOwnedWorkspace(w http.ResponseWriter, r *http.Request) (wid string, decided, ok bool) {
	named := strings.TrimSpace(r.URL.Query().Get("workspace"))
	wid, unrestricted := s.callerWorkspace(r)

	if !unrestricted {
		if named != "" && named != wid {
			writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.other_workspace")
			return "", false, false
		}
		if wid == "" {
			writeErrKey(w, r, http.StatusForbidden, "api.scope.errors.no_workspace")
			return "", false, false
		}
		return wid, true, true
	}

	if wid != "" {
		return wid, true, true
	}
	if named != "" {
		return named, true, true
	}
	// With exactly one workspace it is the only safe answer; with two, refuse rather than file a
	// task under a tenant nobody named.
	if sole := s.soleWorkspaceID(r.Context()); sole != "" {
		return sole, true, true
	}
	return "", false, true
}

// Projects from different workspaces are an error: the task would be visible to one tenant and
// launch in the other's namespace.
func (s *Server) projectsWorkspace(ctx context.Context, projectIDs []string) (string, error) {
	found := ""
	for _, pid := range projectIDs {
		proj, err := s.projectStore.Get(ctx, pid)
		if err != nil || proj.WorkspaceID == "" {
			continue
		}
		if found != "" && proj.WorkspaceID != found {
			return "", apperr.Invalid("api.scope.errors.projects_span_workspaces", "first", found, "second", proj.WorkspaceID)
		}
		found = proj.WorkspaceID
	}
	return found, nil
}
