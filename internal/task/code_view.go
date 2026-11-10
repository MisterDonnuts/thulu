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

func (t *Task) BranchAt(path string) string {
	for _, wt := range t.WorktreePaths {
		if wt != "" && wt == path {
			return t.BranchName
		}
	}
	return ""
}
