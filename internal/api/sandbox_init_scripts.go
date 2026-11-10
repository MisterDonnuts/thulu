package api

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// The task's projects here are the sandbox owner's, the same set resolveTaskWorkspace mounts; keep
// them in sync.
type sandboxInitScripts struct{ s *Server }

func (b sandboxInitScripts) sandboxOwner(ctx context.Context, taskID string) (*task.Task, error) {
	s := b.s
	if s.taskStore == nil || s.projectStore == nil {
		return nil, apperr.Unavailable("sandbox.errors.init_unavailable")
	}
	t, err := s.taskStore.Get(ctx, taskID)
	if err != nil || t == nil {
		return nil, apperr.NotFound("api.sandbox.errors.task_id_not_found", "task", taskID)
	}
	if t.IsSubtask() {
		if parent, perr := s.taskStore.Get(ctx, t.ParentID); perr == nil && parent != nil {
			return parent, nil
		}
	}
	return t, nil
}

// The task's workspace is the boundary, not its project id list: skip projects from other
// workspaces.
func (b sandboxInitScripts) ownerProject(ctx context.Context, owner *task.Task, projectID string) (*project.Project, bool) {
	proj, err := b.s.projectStore.Get(ctx, projectID)
	if err != nil || proj == nil || proj.WorkspaceID != owner.WorkspaceID {
		return nil, false
	}
	return proj, true
}

func (b sandboxInitScripts) TaskProjects(ctx context.Context, taskID string) ([]sandbox.InitProject, error) {
	owner, err := b.sandboxOwner(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]sandbox.InitProject, 0, len(owner.ProjectIDs))
	for _, id := range owner.ProjectIDs {
		proj, ok := b.ownerProject(ctx, owner, id)
		if !ok {
			continue
		}
		ip := sandbox.InitProject{
			ID:      proj.ID,
			Name:    proj.Name,
			Dir:     filepath.Base(proj.Path),
			Mounted: owner.WorktreePaths[proj.ID] != "",
		}
		sc, err := b.s.projectStore.InitScript(ctx, proj.ID)
		switch {
		case errors.Is(err, project.ErrNoInitScript):
		case err != nil:
			return nil, err
		default:
			ip.Script = sc
		}
		out = append(out, ip)
	}
	return out, nil
}

func (b sandboxInitScripts) SaveInitScript(ctx context.Context, taskID, projectID string, in project.InitScriptInput) (*project.InitScript, error) {
	owner, err := b.sandboxOwner(ctx, taskID)
	if err != nil {
		return nil, err
	}
	onTask := false
	for _, id := range owner.ProjectIDs {
		if id == projectID {
			onTask = true
			break
		}
	}
	proj, ok := b.ownerProject(ctx, owner, projectID)
	if !onTask || !ok {
		return nil, apperr.NotFound("sandbox.errors.init_project_not_in_task", "project", projectID, "projects", "-")
	}
	author, err := initScriptAuthor(ctx)
	if err != nil {
		return nil, err
	}
	sc, err := b.s.projectStore.SaveInitScript(ctx, proj.ID, in, author)
	if err != nil {
		return nil, err
	}
	b.s.auditInitScript(ctx, proj, sc)
	b.s.prewarmAfterSave(proj.ID)
	return sc, nil
}
