package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// A deploy watch is registered from a task: the release agent's run, through octopus-api's
// deploy_watch_* tools, which the daemon lets through only when the run's grant names them
// (run_scope.go). The watch lands on one of the task's projects, and the release tasks it opens go
// to the task's board, type and stage. The daemon owns the automation it generates from the watch.

const managedByDeployWatch = "deploy_watch:"

type deployWatchBody struct {
	ProjectID string `json:"project_id"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	// A Go duration ("15m", "1h"); polled sources only.
	Interval string `json:"interval"`
}

type deployWatchView struct {
	project.DeployWatch
	ProjectName string `json:"project_name,omitempty"`
}

// handleListTaskDeployWatches answers the watches of every project of the task.
func (s *Server) handleListTaskDeployWatches(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	out := []deployWatchView{}
	for _, pid := range t.ProjectIDs {
		proj, err := s.projectStore.Get(r.Context(), pid)
		if err != nil || !workspaceVisible(proj.WorkspaceID, t.WorkspaceID, false) {
			continue
		}
		watches, err := s.projectStore.ListDeployWatches(r.Context(), pid)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		for i := range watches {
			out = append(out, deployWatchView{DeployWatch: watches[i], ProjectName: proj.Name})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"watches": out})
}

// handleListProjectDeployWatches answers the project's watches, for its settings page.
func (s *Server) handleListProjectDeployWatches(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	watches, err := s.projectStore.ListDeployWatches(r.Context(), proj.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"watches": watches})
}

// A person registers or deletes a watch only as an admin, the role that creates automations by hand;
// a run gets here only with the tool named in its grant.
func deployWatchWriterOK(w http.ResponseWriter, r *http.Request) bool {
	if auth.APIKeyRunScope(r.Context()).IsRun() || auth.CallerRole(r.Context()).AtLeast(auth.RoleAdmin) {
		return true
	}
	writeErrKey(w, r, http.StatusForbidden, "api.deploy_watches.errors.admin_only")
	return false
}

// handleSetDeployWatch registers a watch on a project of the task, or updates the one with the same
// source and target, and (re)writes its automation.
func (s *Server) handleSetDeployWatch(w http.ResponseWriter, r *http.Request) {
	if !deployWatchWriterOK(w, r) || !s.automationsAvailable(w, r) {
		return
	}
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	var body deployWatchBody
	if !decodeBody(w, r, &body) {
		return
	}
	in := project.DeployWatchInput{Source: body.Source, Target: body.Target}
	if v := strings.TrimSpace(body.Interval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			writeErrKey(w, r, http.StatusBadRequest, "api.deploy_watches.errors.interval_unreadable", "value", v)
			return
		}
		in.Interval = d
	}
	in, err := project.ValidateDeployWatch(in)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	proj, err := s.deployWatchProject(r.Context(), t, body.ProjectID)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	if srcErr := s.checkDeployWatchSource(r.Context(), proj, in); srcErr != nil {
		writeError(w, r, 0, srcErr)
		return
	}
	watch, created, err := s.projectStore.UpsertDeployWatch(r.Context(), &project.DeployWatch{
		WorkspaceID: proj.WorkspaceID, ProjectID: proj.ID, Source: in.Source, Target: in.Target,
		IntervalSeconds: int(in.Interval / time.Second), BoardID: t.BoardID, TypeID: s.deployWatchType(t),
		CreatedBy: deployWatchAuthor(r), CreatedByTask: t.ID,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	automationID, err := s.armDeployWatch(r.Context(), watch, proj, t.Stage)
	if err != nil {
		if created {
			logx.Warn(s.projectStore.DeleteDeployWatch(context.WithoutCancel(r.Context()), watch.ID),
				"api.deploy_watches.log.rollback_failed", "watch", watch.ID)
		}
		writeError(w, r, 0, err)
		return
	}
	watch.AutomationID = automationID
	s.emit(r, audit.EventKind("deploy_watch.set"), "", watch.ID, "deploy_watch",
		i18n.Text("api.deploy_watches.audit.set", "project", proj.Name, "source", watch.Source, "target", watch.Target),
		map[string]string{"watch_id": watch.ID, "project_id": proj.ID, "automation_id": automationID, "task_id": t.ID})
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"watch": deployWatchView{DeployWatch: *watch, ProjectName: proj.Name}, "created": created})
}

// handleDeleteTaskDeployWatch deletes a watch of one of the task's projects.
func (s *Server) handleDeleteTaskDeployWatch(w http.ResponseWriter, r *http.Request) {
	if !deployWatchWriterOK(w, r) {
		return
	}
	t, ok := s.taskForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	watch, err := s.projectStore.GetDeployWatch(r.Context(), r.PathValue("watchId"))
	if err != nil || !slices.Contains(t.ProjectIDs, watch.ProjectID) || !workspaceVisible(watch.WorkspaceID, t.WorkspaceID, false) {
		writeErrKey(w, r, http.StatusNotFound, "project.errors.deploy_watch_not_found")
		return
	}
	s.deleteDeployWatch(w, r, watch)
}

// handleDeleteProjectDeployWatch deletes a watch from the project's settings page.
func (s *Server) handleDeleteProjectDeployWatch(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	watch, err := s.projectStore.GetDeployWatch(r.Context(), r.PathValue("watchId"))
	if err != nil || watch.ProjectID != proj.ID {
		writeErrKey(w, r, http.StatusNotFound, "project.errors.deploy_watch_not_found")
		return
	}
	s.deleteDeployWatch(w, r, watch)
}

func (s *Server) deleteDeployWatch(w http.ResponseWriter, r *http.Request, watch *project.DeployWatch) {
	if err := s.disarmDeployWatch(r.Context(), watch); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if err := s.projectStore.DeleteDeployWatch(r.Context(), watch.ID); err != nil && !errors.Is(err, project.ErrDeployWatchNotFound) {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.emit(r, audit.EventKind("deploy_watch.deleted"), "", watch.ID, "deploy_watch",
		i18n.Text("api.deploy_watches.audit.deleted", "source", watch.Source, "target", watch.Target),
		map[string]string{"watch_id": watch.ID, "project_id": watch.ProjectID})
	writeStatus(w, "deleted")
}

func (s *Server) disarmDeployWatch(ctx context.Context, watch *project.DeployWatch) error {
	if watch.AutomationID == "" || s.auto.store == nil {
		return nil
	}
	err := s.auto.store.Delete(ctx, watch.AutomationID, automation.InWorkspace(watch.WorkspaceID))
	if errors.Is(err, automation.ErrNotFound) {
		return nil
	}
	return err
}

// deleteProjectDeployWatches removes the automations of a project's watches; the project store
// drops the rows with the project.
func (s *Server) deleteProjectDeployWatches(ctx context.Context, projectID string) {
	watches, err := s.projectStore.ListDeployWatches(ctx, projectID)
	if err != nil {
		logx.Warn(err, "api.deploy_watches.log.list_failed", "project", projectID)
		return
	}
	for i := range watches {
		logx.Warn(s.disarmDeployWatch(ctx, &watches[i]), "api.deploy_watches.log.disarm_failed", "watch", watches[i].ID)
	}
}

// The tasks a watch opens are release tasks: the board's release type when it has one, which skips
// the columns before Release and hands the task to the release agent, else the type of the task
// that registered the watch.
func (s *Server) deployWatchType(t *task.Task) string {
	if s.boards != nil {
		types := s.boards.BoardTypes(t.BoardID)
		for i := range types {
			if types[i].ID == releaseTaskType {
				return releaseTaskType
			}
		}
	}
	return t.TypeID
}

const releaseTaskType = "release"

func deployWatchAuthor(r *http.Request) string {
	if scope := auth.APIKeyRunScope(r.Context()); scope.IsRun() && scope.Agent != "" {
		return scope.Agent
	}
	return auth.ActorFromRequest(r).Label
}

// deployWatchProject picks the task's project the watch is for: the one named, which has to be one
// of the task's, or the only one it has.
func (s *Server) deployWatchProject(ctx context.Context, t *task.Task, projectID string) (*project.Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		if len(t.ProjectIDs) != 1 {
			return nil, apperr.Invalid("api.deploy_watches.errors.project_required", "count", len(t.ProjectIDs))
		}
		projectID = t.ProjectIDs[0]
	}
	if !slices.Contains(t.ProjectIDs, projectID) {
		return nil, apperr.Forbidden("api.deploy_watches.errors.project_not_of_task", "project", projectID)
	}
	proj, err := s.projectStore.Get(ctx, projectID)
	if err != nil || !workspaceVisible(proj.WorkspaceID, t.WorkspaceID, false) {
		return nil, apperr.NotFound("api.scope.errors.project_not_found", "id", projectID)
	}
	return proj, nil
}

// checkDeployWatchSource refuses a watch Thulu could never read: a GitHub source on a repo that is
// not on GitHub, a cloud source on an environment of another project, or one with no deploy markers.
func (s *Server) checkDeployWatchSource(ctx context.Context, proj *project.Project, in project.DeployWatchInput) error {
	switch in.Source {
	case project.WatchGitHubWorkflow, project.WatchGitHubDeployment:
		if _, _, ok := ghrest.ParseRemote(proj.RepoURL); !ok {
			return apperr.Invalid("api.deploy_watches.errors.not_on_github", "project", proj.Name)
		}
	case project.WatchCloud:
		env, err := s.projectStore.FindEnvironment(ctx, in.Target)
		if err != nil || env.ProjectID != proj.ID {
			return apperr.Invalid("project.errors.deploy_watch_cloud_target", "target", in.Target)
		}
		if env.Provider != "gcp" {
			return apperr.Invalid("api.deploy_watches.errors.cloud_provider", "environment", env.Name, "provider", env.Provider)
		}
		if s.envCollector == nil {
			return apperr.Unavailable("api.deploy_watches.errors.cloud_off")
		}
	}
	return nil
}

// armDeployWatch writes the watch's automation from the fixed template, creating it or replacing
// the one the watch already owns, and returns its id.
func (s *Server) armDeployWatch(ctx context.Context, watch *project.DeployWatch, proj *project.Project, stage task.Stage) (string, error) {
	spec, err := s.deployWatchSpec(watch, proj, stage)
	if err != nil {
		return "", err
	}
	parsed, err := automation.ParseSpec(spec)
	if err != nil {
		return "", apperr.Wrap(err, "api.deploy_watches.errors.spec_invalid")
	}
	if refErr := s.checkAutomationRefs(ctx, proj.WorkspaceID, parsed); refErr != nil {
		return "", refErr
	}
	if watch.AutomationID != "" {
		updErr := s.rewriteDeployWatchAutomation(ctx, watch.WorkspaceID, watch.AutomationID, spec)
		if updErr == nil {
			s.auto.scheduler.Nudge()
			return watch.AutomationID, nil
		}
		// The automation is gone (deleted from the store directly): write a new one.
		if !errors.Is(updErr, automation.ErrNotFound) {
			return "", updErr
		}
	}
	a, err := s.auto.store.CreateManaged(ctx, watch.WorkspaceID, spec, managedByDeployWatch+watch.ID)
	if err != nil {
		return "", err
	}
	owner, err := s.projectStore.SwapDeployWatchAutomation(ctx, watch.ID, watch.AutomationID, a.ID)
	if err != nil || owner != a.ID {
		// Either the swap failed or another set armed the watch since this one read it; that
		// set's automation is the watch's, and this one would open every release task twice.
		logx.Warn(s.auto.store.Delete(context.WithoutCancel(ctx), a.ID, automation.InWorkspace(watch.WorkspaceID)),
			"api.deploy_watches.log.rollback_failed", "watch", watch.ID)
		if err != nil {
			return "", err
		}
		if owner == "" {
			return "", apperr.Conflict("api.deploy_watches.errors.arm_conflict")
		}
		if err := s.rewriteDeployWatchAutomation(ctx, watch.WorkspaceID, owner, spec); err != nil {
			return "", err
		}
	}
	s.auto.scheduler.Nudge()
	return owner, nil
}

// rewriteDeployWatchAutomation writes spec into the watch's automation and keeps it suspended or
// running as it is: a person who suspended it decides, not the next set of the same watch.
func (s *Server) rewriteDeployWatchAutomation(ctx context.Context, workspaceID, automationID, spec string) error {
	scope := automation.InWorkspace(workspaceID)
	cur, err := s.auto.store.Get(ctx, automationID, scope)
	if err != nil {
		return err
	}
	_, err = s.auto.store.Update(ctx, automationID, scope, spec, cur.Enabled)
	return err
}

// deployWatchOwner names the watch that owns a managed automation, or "" for one a person wrote.
func deployWatchOwner(a *automation.Automation) string {
	id, ok := strings.CutPrefix(a.ManagedBy, managedByDeployWatch)
	if !ok {
		return ""
	}
	return id
}

// managedAutomationRefusal is the conflict a hand edit or delete of a generated automation gets.
// An automation its watch no longer owns (the watch is gone, or points at another automation) has
// no owner left to change it through, so a person may.
func (s *Server) managedAutomationRefusal(ctx context.Context, a *automation.Automation) error {
	if a.ManagedBy == "" {
		return nil
	}
	name := ""
	if id := deployWatchOwner(a); id != "" {
		watch, err := s.projectStore.GetDeployWatch(ctx, id)
		switch {
		case errors.Is(err, project.ErrDeployWatchNotFound):
			return nil
		case err != nil:
			return err
		case watch.AutomationID != a.ID:
			return nil
		}
		if proj, err := s.projectStore.Get(ctx, watch.ProjectID); err == nil {
			name = proj.Name
		}
	}
	return apperr.Conflict("api.automations.errors.managed_by_deploy_watch", "project", name)
}
