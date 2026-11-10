//go:build !hostcli

package cli

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/exportbundle"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

const nuevaID = "bbbb000000000005"

func devBundle(t *testing.T) (*exportbundle.Bundle, map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	src, err := wireBundleStores(testpg.New(t), t.TempDir())
	if err != nil {
		t.Fatalf("source stores: %v", err)
	}
	fb := stage.FallbackWorkspaceID
	if err := src.s.Projects.ImportProject(ctx, project.Project{
		ID: "p-tienda", WorkspaceID: fb, Name: "tienda-api", Path: "/x", RepoURL: "https://x", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := src.s.Tasks.Create(ctx, &task.Task{ID: "task_a", WorkspaceID: fb, BoardID: stage.DefaultBoardID, Title: "A"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	b, files, err := exportbundle.Export(ctx, src.s, fb)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return b, files
}

func freshInstall(t *testing.T) (*bundleStores, *sql.DB) {
	t.Helper()
	handle := testpg.New(t)
	bs, err := wireBundleStores(handle, t.TempDir())
	if err != nil {
		t.Fatalf("stores: %v", err)
	}
	if _, err := handle.Exec(`CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatalf("create workspaces: %v", err)
	}
	if _, err := handle.Exec(`INSERT INTO workspaces (id, slug) VALUES ($1, 'nueva')`, nuevaID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	return bs, handle
}

func workspaceOf(t *testing.T, handle *sql.DB, table, id string) string {
	t.Helper()
	var wid string
	if err := handle.QueryRow(`SELECT workspace_id FROM `+table+` WHERE id = $1`, id).Scan(&wid); err != nil {
		t.Fatalf("read %s %s: %v", table, id, err)
	}
	return wid
}

func TestFreshInstallResolvesToItsOnlyWorkspace(t *testing.T) {
	ctx := context.Background()
	_, handle := freshInstall(t)
	if got := workspaceOf(t, handle, "boards", stage.DefaultBoardID); got != stage.FallbackWorkspaceID {
		t.Fatalf("seed board under %q, want the fallback: the test no longer sets up a fresh install", got)
	}

	holds, err := fallbackHoldsData(ctx, handle)
	if err != nil || holds {
		t.Errorf("fallbackHoldsData = %v, %v; want false with only the seed board", holds, err)
	}
	if got, err := resolveWorkspace(ctx, handle, "", true); err != nil || got != nuevaID {
		t.Errorf("resolveWorkspace = %q, %v; want the only workspace", got, err)
	}
	if got, _, _, err := importTarget(ctx, handle, stage.FallbackWorkspaceID, ""); err != nil || got != nuevaID {
		t.Errorf("importTarget = %q, %v; want the only workspace", got, err)
	}
}

func TestImportIntoTheOnlyWorkspaceOfAFreshInstall(t *testing.T) {
	for _, named := range []string{"", "nueva"} {
		t.Run("workspace="+named, func(t *testing.T) {
			ctx := context.Background()
			b, files := devBundle(t)
			dst, handle := freshInstall(t)

			target, _, stats, err := importInto(ctx, dst, b, files, named)
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if target != nuevaID || stats.Tasks != 1 || stats.Projects != 1 {
				t.Errorf("target = %q, stats = %+v", target, stats)
			}
			for table, id := range map[string]string{"projects": "p-tienda", "tasks": "task_a", "boards": stage.DefaultBoardID} {
				if got := workspaceOf(t, handle, table, id); got != nuevaID {
					t.Errorf("%s %s in %q, want nueva", table, id, got)
				}
			}
			if got, err := resolveWorkspace(ctx, handle, "", true); err != nil || got != nuevaID {
				t.Errorf("export after import resolves to %q, %v", got, err)
			}
		})
	}
}

func TestImportLeavesAFallbackInUseAlone(t *testing.T) {
	ctx := context.Background()
	b, files := devBundle(t)
	dst, handle := freshInstall(t)
	if err := dst.s.Tasks.Create(ctx, &task.Task{ID: "task_dev", WorkspaceID: stage.FallbackWorkspaceID, BoardID: stage.DefaultBoardID, Title: "dev"}); err != nil {
		t.Fatalf("seed dev task: %v", err)
	}

	_, _, _, err := importInto(ctx, dst, b, files, "nueva")
	if !errors.Is(err, apperr.New("exportbundle.errors.board_owned_by_fallback")) {
		t.Errorf("err = %v, want board_owned_by_fallback", err)
	}
	if got := workspaceOf(t, handle, "boards", stage.DefaultBoardID); got != stage.FallbackWorkspaceID {
		t.Errorf("board moved to %q with a task still on it under the fallback", got)
	}
}
