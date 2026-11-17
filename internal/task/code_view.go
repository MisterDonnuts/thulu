package task

import (
	"context"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Keys are the mount names under /workspace/projects/ (filepath.Base of the project path).
func ReadOnlyCode(ctx context.Context, t *Task, projects *project.Store) map[string]types.CodeView {
	out := map[string]types.CodeView{}
	if projects == nil {
		return out
	}
	for _, projectID := range t.ProjectIDs {
		proj, err := projects.Get(ctx, projectID)
		if err != nil || proj == nil || proj.Path == "" || proj.CodeOnRunner() {
			continue
		}
		view := types.CodeView{Path: proj.Path}
		if wt := t.WorktreePaths[projectID]; wt != "" {
			view = types.CodeView{Path: wt, Branch: t.BranchName}
		}
		out[filepath.Base(proj.Path)] = view
	}
	return out
}

// CodeSource is one project of a task as a run outside the daemon reaches it. Each consumer builds
// the address where it runs: a machine its folder or <daemon>/git/<task>/<Name>, a pod its clone URL.
type CodeSource struct {
	Project *project.Project
	// The /git/ path segment and the mount name under projects/ (filepath.Base of the project path).
	Name string
	// The task branch; empty when a read-only run has no branch to read and takes the default.
	Branch string
}

// OnMachine reports whether the code lives on a runner's machine rather than in the daemon.
func (c CodeSource) OnMachine() bool { return c.Project.RunsOnRunner() }

// CodeSources lists the task's projects for a run outside the daemon. A machine keeps its own copy of
// the task branch. A daemon project the daemon has no worktree for has no task branch either: a
// read-only run reads its default branch, and a run that writes goes without it, as in Docker.
func CodeSources(ctx context.Context, t *Task, projects *project.Store, readOnly bool) ([]CodeSource, error) {
	branch := project.TaskBranchName(t.SandboxRootID())
	out := make([]CodeSource, 0, len(t.ProjectIDs))
	for _, id := range t.ProjectIDs {
		p, err := projects.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		src := CodeSource{Project: p, Name: filepath.Base(p.Path), Branch: branch}
		if !src.OnMachine() && t.WorktreePaths[id] == "" {
			if !readOnly {
				continue
			}
			src.Branch = ""
		}
		out = append(out, src)
	}
	return out, nil
}

func (t *Task) BranchAt(path string) string {
	for _, wt := range t.WorktreePaths {
		if wt != "" && wt == path {
			return t.BranchName
		}
	}
	return ""
}
