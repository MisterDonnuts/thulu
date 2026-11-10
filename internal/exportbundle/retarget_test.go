package exportbundle_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestRetargetMovesEveryScopedRow(t *testing.T) {
	const from, to = "bbbb000000000006", "bbbb000000000007"
	b := &exportbundle.Bundle{
		WorkspaceID: from,
		Boards: []exportbundle.BoardSection{{
			Info: stage.BoardInfo{ID: "b1", WorkspaceID: from},
			TaskTypes: []stage.TaskTypeInfo{
				{ID: "bug", WorkspaceID: from},
				{ID: "shared"},
			},
		}},
		Projects:    []exportbundle.ProjectSection{{Project: project.Project{ID: "p1", WorkspaceID: from}}},
		Tasks:       []exportbundle.TaskSection{{Task: task.Task{ID: "t1", WorkspaceID: from}}},
		Credentials: []credentials.Credential{{ID: "c1", WorkspaceID: from}},
	}

	b.Retarget(to)

	if b.WorkspaceID != to {
		t.Errorf("bundle workspace = %q", b.WorkspaceID)
	}
	if got := b.Boards[0].Info.WorkspaceID; got != to {
		t.Errorf("board workspace = %q", got)
	}
	if got := b.Boards[0].TaskTypes[0].WorkspaceID; got != to {
		t.Errorf("task type workspace = %q", got)
	}
	if got := b.Boards[0].TaskTypes[1].WorkspaceID; got != "" {
		t.Errorf("a shared task type was moved to %q", got)
	}
	if got := b.Projects[0].Project.WorkspaceID; got != to {
		t.Errorf("project workspace = %q", got)
	}
	if got := b.Tasks[0].Task.WorkspaceID; got != to {
		t.Errorf("task workspace = %q", got)
	}
	if got := b.Credentials[0].WorkspaceID; got != to {
		t.Errorf("credential workspace = %q", got)
	}
	if b.Tasks[0].Task.ID != "t1" || b.Projects[0].Project.ID != "p1" {
		t.Error("ids changed")
	}
}

func TestRetargetToTheSameWorkspaceIsANoOp(t *testing.T) {
	b := &exportbundle.Bundle{WorkspaceID: "w", Tasks: []exportbundle.TaskSection{{Task: task.Task{WorkspaceID: "w"}}}}
	b.Retarget("")
	b.Retarget("w")
	if b.WorkspaceID != "w" || b.Tasks[0].Task.WorkspaceID != "w" {
		t.Errorf("bundle changed: %+v", b)
	}
}
