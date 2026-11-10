package dockergc_test

import (
	"testing"

	"github.com/docker/docker/api/types"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/sandbox/sandboxid"
)

func TestClassifyContainerLeavesAnotherInstallAlone(t *testing.T) {
	dockerx.SetInstallID("mine")
	t.Cleanup(func() { dockerx.SetInstallID("") })
	for _, c := range []struct {
		labels map[string]string
		want   dockergc.ContainerKind
	}{
		{map[string]string{sandboxid.ContainerLabel: "true", dockerx.InstallLabel: "theirs"}, ""},
		{map[string]string{agent.LabelManaged: "true", dockerx.InstallLabel: "theirs"}, ""},
		{map[string]string{sandboxid.ContainerLabel: "true", dockerx.InstallLabel: "mine"}, dockergc.ContainerSandbox},
		{map[string]string{sandboxid.ContainerLabel: "true"}, dockergc.ContainerSandbox},
	} {
		ctr := &types.Container{Labels: c.labels, State: "running"}
		if got := dockergc.ClassifyContainer(ctr, nil); got != c.want {
			t.Errorf("%v: kind = %q, want %q", c.labels, got, c.want)
		}
	}
}
