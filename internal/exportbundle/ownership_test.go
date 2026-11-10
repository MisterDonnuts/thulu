package exportbundle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestImportRefusesRowsAnotherWorkspaceOwns(t *testing.T) {
	ctx := context.Background()
	k := buildStores(t, testpg.New(t), t.TempDir())
	must(t, k.stage.CreateBoard(ctx, stage.BoardInfo{ID: "custom", Label: "Custom", WorkspaceID: ws, Default: true},
		stage.Static().List()), "create board")
	must(t, k.task.Create(ctx, &task.Task{ID: "task_a", WorkspaceID: ws, BoardID: "custom", Title: "A"}), "create task")

	b, files, err := exportbundle.Export(ctx, k.s, ws)
	must(t, err, "export")
	b.Retarget("ws_other")

	if _, err := exportbundle.Import(ctx, k.s, b, files, k.attachmentsDir); !errors.Is(err, apperr.New("exportbundle.errors.board_owned_elsewhere")) {
		t.Errorf("board: err = %v, want board_owned_elsewhere", err)
	}
	b.Boards = nil
	if _, err := exportbundle.Import(ctx, k.s, b, files, k.attachmentsDir); !errors.Is(err, apperr.New("exportbundle.errors.task_owned_elsewhere")) ||
		apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("task: err = %v, want a task_owned_elsewhere conflict", err)
	}

	got, err := k.task.Get(ctx, "task_a")
	must(t, err, "get task")
	if got.WorkspaceID != ws {
		t.Errorf("task moved to %q", got.WorkspaceID)
	}
	for _, info := range k.stage.BoardList() {
		if info.ID == "custom" && info.WorkspaceID != ws {
			t.Errorf("board moved to %q", info.WorkspaceID)
		}
	}
}

func TestImportNamesAFallbackOwnerWithoutSendingThere(t *testing.T) {
	ctx := context.Background()
	k := buildStores(t, testpg.New(t), t.TempDir())
	fb := stage.FallbackWorkspaceID
	must(t, k.s.Projects.ImportProject(ctx, project.Project{ID: "proj_dev", WorkspaceID: fb, Name: "p", Path: "/x", RepoURL: "https://x", DefaultBranch: "main"}), "seed project")
	must(t, k.task.Create(ctx, &task.Task{ID: "task_dev", WorkspaceID: fb, BoardID: stage.DefaultBoardID, Title: "dev"}), "create task")

	b, files, err := exportbundle.Export(ctx, k.s, fb)
	must(t, err, "export")
	b.Retarget(ws)
	b.Boards = nil

	if _, err := exportbundle.Import(ctx, k.s, b, files, k.attachmentsDir); !errors.Is(err, apperr.New("exportbundle.errors.project_owned_by_fallback")) ||
		apperr.KindOf(err) != apperr.KindConflict {
		t.Errorf("project: err = %v, want a project_owned_by_fallback conflict", err)
	}
	b.Projects = nil
	if _, err := exportbundle.Import(ctx, k.s, b, files, k.attachmentsDir); !errors.Is(err, apperr.New("exportbundle.errors.task_owned_by_fallback")) {
		t.Errorf("task: err = %v, want task_owned_by_fallback", err)
	}
}
