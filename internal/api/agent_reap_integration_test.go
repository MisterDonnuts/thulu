package api

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/testdocker"

	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func makeExitedContainer(t *testing.T, cli *client.Client) string {
	t.Helper()
	ctx := context.Background()
	cfg := &container.Config{Image: "busybox:latest", Entrypoint: []string{"true"}}
	resp, err := cli.ContainerCreate(ctx, cfg, &container.HostConfig{}, &network.NetworkingConfig{}, nil, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ins, err := cli.ContainerInspect(ctx, resp.ID)
		if err == nil && ins.State != nil && !ins.State.Running {
			return resp.ID
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	t.Fatal("container did not exit in time")
	return ""
}

func exists(t *testing.T, cli *client.Client, id string) bool {
	t.Helper()
	_, err := cli.ContainerInspect(context.Background(), id)
	return err == nil
}

func TestIntegrationReapAgentContainerRemoves(t *testing.T) {
	cli := reapDocker(t)
	st, err := settings.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	l := &taskLauncher{srv: &Server{docker: cli, settingsStore: st}}

	id := makeExitedContainer(t, cli)
	t.Cleanup(func() { _ = cli.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true}) })

	if !exists(t, cli, id) {
		t.Fatal("precondition: container should exist before reap")
	}
	l.reapAgentContainer(t.Context(), id)
	if exists(t, cli, id) {
		t.Error("container still present after reapAgentContainer with reaping enabled")
	}
	l.reapAgentContainer(t.Context(), id)
}

func TestIntegrationReapDisabledKeepsContainer(t *testing.T) {
	cli := reapDocker(t)
	st, err := settings.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutJSON(t.Context(), settings.AgentReapOnExitKey, false); err != nil {
		t.Fatal(err)
	}
	l := &taskLauncher{srv: &Server{docker: cli, settingsStore: st}}

	id := makeExitedContainer(t, cli)
	t.Cleanup(func() { _ = cli.ContainerRemove(context.Background(), id, container.RemoveOptions{Force: true}) })

	l.reapAgentContainer(t.Context(), id)
	if !exists(t, cli, id) {
		t.Error("container removed despite agent.reap_on_exit=false")
	}
}

func reapDocker(t *testing.T) *client.Client {
	t.Helper()
	cli := testdocker.Client(t)
	testdocker.RequireImage(t, cli, "busybox:latest", "docker pull busybox")
	return cli
}
