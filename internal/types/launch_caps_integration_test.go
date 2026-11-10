package types

import (
	"context"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/testdocker"
)

const capsTestImage = "busybox:latest"

func TestIntegrationMemoryCapApplied(t *testing.T) {
	cli := capsDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	const mem = int64(64) << 20
	hc, err := buildHostConfig(&ComposeServiceSpec{}, nil, ResourceCaps{MemoryBytes: mem, PidsLimit: 64})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &container.Config{Image: capsTestImage, Entrypoint: []string{"sleep"}, Cmd: []string{"30"}}
	resp, err := cli.ContainerCreate(ctx, cfg, hc, &network.NetworkingConfig{}, nil, "")
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	defer cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}
	info, err := cli.ContainerInspect(ctx, resp.ID)
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if info.HostConfig.Memory != mem {
		t.Errorf("inspected Memory = %d, want %d", info.HostConfig.Memory, mem)
	}
	if info.HostConfig.PidsLimit == nil || *info.HostConfig.PidsLimit != 64 {
		t.Errorf("inspected PidsLimit = %v, want 64", info.HostConfig.PidsLimit)
	}
}

func TestIntegrationMemoryCapContainsRunaway(t *testing.T) {
	cli := capsDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const mem = int64(32) << 20
	hc, err := buildHostConfig(&ComposeServiceSpec{}, nil, ResourceCaps{MemoryBytes: mem})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &container.Config{Image: capsTestImage, Entrypoint: []string{"sh"}, Cmd: []string{"-c", "tail /dev/zero"}}
	resp, err := cli.ContainerCreate(ctx, cfg, hc, &network.NetworkingConfig{}, nil, "")
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	defer cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("ContainerStart: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	var info struct {
		running   bool
		oomKilled bool
		exitCode  int
	}
	for time.Now().Before(deadline) {
		ins, err := cli.ContainerInspect(ctx, resp.ID)
		if err != nil {
			t.Fatalf("ContainerInspect: %v", err)
		}
		if ins.State != nil && !ins.State.Running {
			info.running = false
			info.oomKilled = ins.State.OOMKilled
			info.exitCode = ins.State.ExitCode
			break
		}
		info.running = true
		time.Sleep(250 * time.Millisecond)
	}
	if info.running {
		t.Fatal("runaway container still running — memory cap did not contain it")
	}
	// cgroup v2 sets OOMKilled; some setups only surface exit 137 (128+SIGKILL).
	if !info.oomKilled && info.exitCode != 137 {
		t.Errorf("expected OOM kill, got OOMKilled=%v exitCode=%d", info.oomKilled, info.exitCode)
	}
}

func capsDocker(t *testing.T) *client.Client {
	t.Helper()
	cli := testdocker.Client(t)
	testdocker.RequireImage(t, cli, capsTestImage, "docker pull "+capsTestImage)
	return cli
}
