package types

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/imageref"
)

func TestSocketMountNeedsOptIn(t *testing.T) {
	t.Setenv(EnvAllowAgentDockerSocket, "")
	mounts := []mount.Mount{
		{Type: mount.TypeBind, Source: "/srv/work", Target: "/workspace"},
		{Type: mount.TypeBind, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
	}
	if _, err := checkSocketMounts("coder", mounts); err == nil || !strings.Contains(err.Error(), EnvAllowAgentDockerSocket) {
		t.Fatalf("a socket mount without the opt-in must be refused naming the env var, got %v", err)
	}
	renamed := []mount.Mount{{Type: mount.TypeBind, Source: "/run/user/1000/podman/podman.sock", Target: "/sock"}}
	if _, err := checkSocketMounts("coder", renamed); err == nil {
		t.Error("a runtime socket mounted under another path must be refused too")
	}
	if has, err := checkSocketMounts("coder", mounts[:1]); err != nil || has {
		t.Errorf("plain mounts pass: has=%v err=%v", has, err)
	}

	t.Setenv(EnvAllowAgentDockerSocket, "1")
	if has, err := checkSocketMounts("coder", mounts); err != nil || !has {
		t.Errorf("with the opt-in the socket is allowed and reported: has=%v err=%v", has, err)
	}
}

func TestSocketGroupFromEnv(t *testing.T) {
	t.Setenv(EnvAgentDockerSocketGID, "991")
	hc := &container.HostConfig{}
	hardenHostConfig(hc, false, true)
	if !slices.Contains(hc.GroupAdd, "991") {
		t.Errorf("GroupAdd = %v, want the socket gid 991", hc.GroupAdd)
	}
	t.Setenv(EnvAgentDockerSocketGID, "docker; rm -rf /")
	if g := socketGroup(); g == "docker; rm -rf /" {
		t.Error("a non-numeric gid must be ignored")
	}
}

func TestHardenHostConfigCaps(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_HOST_GATEWAY", "")
	root := &container.HostConfig{}
	hardenHostConfig(root, false, false)
	if !slices.Equal(root.CapDrop, []string{"ALL"}) {
		t.Errorf("CapDrop = %v, want [ALL]", root.CapDrop)
	}
	for _, c := range root.CapAdd {
		if c == "SYS_ADMIN" || c == "NET_RAW" || c == "NET_ADMIN" || c == "MKNOD" {
			t.Errorf("root agent must not keep %s", c)
		}
	}
	if !slices.Contains(root.CapAdd, "SETUID") || !slices.Contains(root.CapAdd, "SETGID") {
		t.Errorf("root agent needs SETUID/SETGID to drop to node, got %v", root.CapAdd)
	}
	if len(root.GroupAdd) != 0 {
		t.Error("no socket, no extra group")
	}
	if len(root.ExtraHosts) != 0 {
		t.Error("host-gateway mapping is opt-in")
	}

	rootless := &container.HostConfig{}
	hardenHostConfig(rootless, true, false)
	if len(rootless.CapAdd) != 0 {
		t.Errorf("a rootless agent needs no capabilities, got %v", rootless.CapAdd)
	}

	t.Setenv("OCTOPUS_AGENT_HOST_GATEWAY", "1")
	gw := &container.HostConfig{}
	hardenHostConfig(gw, false, false)
	if !slices.Contains(gw.ExtraHosts, "host.docker.internal:host-gateway") {
		t.Errorf("ExtraHosts = %v", gw.ExtraHosts)
	}
}

func TestResolveNetworkMode(t *testing.T) {
	t.Setenv(EnvAllowAgentHostNetwork, "")
	if m, err := resolveNetworkMode("a", "", "octopus-agents"); err != nil || m != "octopus-agents" {
		t.Errorf("empty spec mode joins the install network, got %q %v", m, err)
	}
	if m, err := resolveNetworkMode("a", "none", "octopus-agents"); err != nil || m != "none" {
		t.Errorf("none is always allowed, got %q %v", m, err)
	}
	for _, mode := range []string{"host", "container:abc", "service:db"} {
		if _, err := resolveNetworkMode("a", mode, "octopus-agents"); err == nil {
			t.Errorf("%s must need %s", mode, EnvAllowAgentHostNetwork)
		}
	}
	t.Setenv(EnvAllowAgentHostNetwork, "1")
	if m, err := resolveNetworkMode("a", "host", ""); err != nil || m != "host" {
		t.Errorf("host with the opt-in = %q %v", m, err)
	}
}

func TestRuntimeEnvForwardsProxyWithoutOverriding(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv(EnvFallbackDNS, "9.9.9.9")
	got := runtimeEnv([]string{"https_proxy=http://agent-own:8080", "A=1"})
	if !slices.Contains(got, "HTTPS_PROXY=http://proxy:3128") {
		t.Errorf("daemon proxy not forwarded: %v", got)
	}
	if slices.Contains(got, "https_proxy=http://proxy:3128") {
		t.Errorf("a key the launch already sets must not be overridden: %v", got)
	}
	if !slices.Contains(got, EnvFallbackDNS+"=9.9.9.9") {
		t.Errorf("fallback DNS not forwarded: %v", got)
	}

	t.Setenv("HTTPS_PROXY", "")
	t.Setenv(EnvFallbackDNS, "")
	if got := runtimeEnv([]string{"A=1"}); !slices.Equal(got, []string{"A=1"}) {
		t.Errorf("nothing configured, nothing added: %v", got)
	}
}

type fakeImages struct {
	present map[string]bool
	pulled  []string
	built   []string
}

func (f *fakeImages) ImageInspectWithRaw(_ context.Context, ref string) (types.ImageInspect, []byte, error) {
	if f.present[ref] {
		return types.ImageInspect{}, nil, nil
	}
	return types.ImageInspect{}, nil, errdefs.NotFound(errors.New("no such image"))
}

func (f *fakeImages) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	f.pulled = append(f.pulled, ref)
	f.present[ref] = true
	return io.NopCloser(strings.NewReader(`{"status":"done"}`)), nil
}

func (f *fakeImages) ImageBuild(_ context.Context, _ io.Reader, o types.ImageBuildOptions) (types.ImageBuildResponse, error) {
	f.built = append(f.built, o.Tags...)
	return types.ImageBuildResponse{Body: io.NopCloser(bytes.NewReader(nil))}, nil
}

func TestEnsureImageNeverBuildsByDefault(t *testing.T) {
	t.Setenv(EnvBuildMissingImages, "")
	t.Setenv(imageref.RegistryEnv, "")
	f := &fakeImages{present: map[string]bool{}}
	spec := &ComposeServiceSpec{Build: composeBuild{Context: "."}}
	_, err := EnsureImage(context.Background(), f, spec, AgentType{Name: "coder", Dir: t.TempDir()}, true)
	var missing *imageref.MissingError
	if !errors.As(err, &missing) || missing.Ref != "octopus-built-coder:latest" {
		t.Fatalf("a missing image must fail naming it, got %v", err)
	}
	if !strings.Contains(err.Error(), EnvBuildMissingImages) {
		t.Errorf("the error must say how to get the image: %v", err)
	}
	if len(f.built) != 0 || len(f.pulled) != 0 {
		t.Errorf("nothing may be built or pulled from Docker Hub: built=%v pulled=%v", f.built, f.pulled)
	}
}

func TestEnsureImageBuildsWithOptIn(t *testing.T) {
	t.Setenv(EnvBuildMissingImages, "1")
	t.Setenv(imageref.RegistryEnv, "")
	f := &fakeImages{present: map[string]bool{}}
	spec := &ComposeServiceSpec{Build: composeBuild{Context: "."}}
	name, err := EnsureImage(context.Background(), f, spec, AgentType{Name: "coder", Dir: t.TempDir()}, true)
	if err != nil || name != "octopus-built-coder:latest" || !slices.Equal(f.built, []string{"octopus-built-coder:latest"}) {
		t.Errorf("opt-in builds the missing image: name=%q built=%v err=%v", name, f.built, err)
	}
}

func TestEnsureImagePullsFromRegistry(t *testing.T) {
	t.Setenv(EnvBuildMissingImages, "")
	t.Setenv(imageref.RegistryEnv, "registry.corp.example/octopus")
	f := &fakeImages{present: map[string]bool{}}
	name, err := EnsureImage(context.Background(), f, &ComposeServiceSpec{Image: "octopus-docker-agent:latest"}, AgentType{Name: "coder"}, true)
	want := "registry.corp.example/octopus/octopus-docker-agent:latest"
	if err != nil || name != want || !slices.Equal(f.pulled, []string{want}) {
		t.Errorf("registry-prefixed image is pulled: name=%q pulled=%v err=%v", name, f.pulled, err)
	}
	f.pulled = nil
	if _, err := EnsureImage(context.Background(), f, &ComposeServiceSpec{Image: "octopus-docker-agent:latest"}, AgentType{Name: "coder"}, true); err != nil || len(f.pulled) != 0 {
		t.Errorf("present image must not be pulled again: pulled=%v err=%v", f.pulled, err)
	}
}
