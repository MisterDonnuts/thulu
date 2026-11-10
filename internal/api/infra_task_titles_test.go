package api

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
)

func TestWithOwnerNames(t *testing.T) {
	t.Parallel()
	s, _ := newIntegrationServer(t)
	const id, ws = "t-login", "ws-mine"
	if err := s.taskStore.Create(t.Context(), &task.Task{ID: id, WorkspaceID: ws, Title: "Fix the login page", Stage: "backlog"}); err != nil {
		t.Fatal(err)
	}
	cs := []infra.Container{
		{ID: "mine", WorkspaceID: ws, TaskID: id},
		{ID: "theirs", WorkspaceID: "other-ws", TaskID: id},
		{ID: "prewarm", WorkspaceID: ws, TaskID: "prewarm:p1"},
	}
	got := s.withOwnerNames(t.Context(), cs, ws, false)
	if got[0].TaskTitle != "Fix the login page" {
		t.Errorf("own container title = %q", got[0].TaskTitle)
	}
	if got[1].TaskTitle != "" {
		t.Errorf("another workspace's container got title %q", got[1].TaskTitle)
	}
	if got[2].TaskTitle != "" {
		t.Errorf("prewarm label got title %q", got[2].TaskTitle)
	}
}

func TestWithOwnerNames_PrewarmProject(t *testing.T) {
	t.Parallel()
	g := newSandboxGatewayRig(t)
	ctx := t.Context()
	projectID := createProject(t, g.h, g.tokenA, "tienda-api")
	owner := sandbox.PrewarmOwner(projectID)
	now := time.Now()
	if err := g.srv.sandbox.store.Upsert(ctx, &sandbox.Sandbox{
		TaskID: owner, WorkspaceID: g.wsA, ContainerID: "sandbox-prewarm-pod",
		Status: sandbox.StatusReady, CreatedAt: now, LastActivityAt: now,
	}); err != nil {
		t.Fatalf("upsert prewarm: %v", err)
	}
	cs := []infra.Container{
		{ID: "docker", Kind: infra.KindSandbox, WorkspaceID: g.wsA, TaskID: owner},
		{ID: "uid-1", Name: "sandbox-prewarm-pod", Kind: infra.KindSandbox, WorkspaceID: g.wsA, TaskID: "prewarm-sanitized"},
		{ID: "theirs", Kind: infra.KindSandbox, WorkspaceID: "other-ws", TaskID: owner},
	}
	got := g.srv.withOwnerNames(ctx, cs, g.wsA, false)
	for i, want := range []string{"tienda-api", "tienda-api", ""} {
		if got[i].ProjectName != want {
			t.Errorf("container %s project_name = %q, want %q", got[i].ID, got[i].ProjectName, want)
		}
	}
}
