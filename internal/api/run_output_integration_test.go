package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/testdocker"
)

const runOutputTestImage = "busybox:latest"

func TestIntegrationCollectRunOutputs(t *testing.T) {
	cli := testdocker.Client(t)
	testdocker.RequireImage(t, cli, runOutputTestImage, "docker pull "+runOutputTestImage)
	stamp := time.Now().UnixNano()

	cases := []struct {
		name      string
		withLabel bool
		write     bool
	}{
		{name: "instance from label", withLabel: true, write: true},
		{name: "instance from name", withLabel: false, write: true},
		{name: "nothing written", withLabel: true, write: false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			inst := fmt.Sprintf("octopus-test-dev-task-%x-in_progress-%d", stamp, i)
			script := "true"
			if tc.write {
				script = "mkdir -p /workspace/runs/" + inst + "/shots" +
					" && echo 'the report' > /workspace/runs/" + inst + "/_index.md" +
					" && echo sess-42 > /workspace/runs/" + inst + "/.session_id" +
					" && echo png > /workspace/runs/" + inst + "/shots/home.png"
			}
			labels := map[string]string{"thulu.workspace-worktree.app": "/data/projects/app/.worktrees/x"}
			if tc.withLabel {
				labels[agent.LabelInstance] = inst
			}
			resp, err := cli.ContainerCreate(ctx,
				&container.Config{Image: runOutputTestImage, Cmd: []string{"sh", "-c", script}, Labels: labels},
				&container.HostConfig{}, &network.NetworkingConfig{}, nil, inst)
			if err != nil {
				t.Fatalf("ContainerCreate: %v", err)
			}
			t.Cleanup(func() {
				_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
			})
			if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
				t.Fatalf("ContainerStart: %v", err)
			}
			waitC, errC := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
			select {
			case <-waitC:
			case err := <-errC:
				t.Fatalf("ContainerWait: %v", err)
			}

			runs := filepath.Join(t.TempDir(), "runs")
			l := &taskLauncher{srv: &Server{docker: cli, runsDir: runs}}
			outDir, wtPath := l.collectRunOutputs(ctx, resp.ID[:12])

			if want := filepath.Join(runs, inst); outDir != want {
				t.Fatalf("outDir = %q, want %q", outDir, want)
			}
			if wtPath != "/data/projects/app/.worktrees/x" {
				t.Errorf("wtPath = %q, want the worktree label", wtPath)
			}
			if !tc.write {
				if _, err := os.Stat(outDir); !os.IsNotExist(err) {
					t.Errorf("a run that wrote nothing left a dir behind (stat err = %v)", err)
				}
				return
			}
			for rel, want := range map[string]string{
				"_index.md":      "the report\n",
				".session_id":    "sess-42\n",
				"shots/home.png": "png\n",
			} {
				got, err := os.ReadFile(filepath.Join(outDir, rel))
				if err != nil {
					t.Errorf("%s not copied out: %v", rel, err)
					continue
				}
				if string(got) != want {
					t.Errorf("%s = %q, want %q", rel, got, want)
				}
			}
		})
	}
}
