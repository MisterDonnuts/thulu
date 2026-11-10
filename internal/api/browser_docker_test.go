package api

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
)

type fakeBrowserDocker struct {
	created    []*container.Config
	hosts      []*container.HostConfig
	names      []string
	started    []string
	removed    []string
	listed     []container.ListOptions
	list       []types.Container
	startErr   error
	missing    bool
	pulled     []string
	entrypoint []string
}

func (f *fakeBrowserDocker) ImageInspectWithRaw(context.Context, string) (types.ImageInspect, []byte, error) {
	if f.missing {
		return types.ImageInspect{}, nil, errdefs.NotFound(errors.New("no such image"))
	}
	return types.ImageInspect{Config: &container.Config{Entrypoint: f.entrypoint}}, nil, nil
}

func (f *fakeBrowserDocker) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	if !f.missing {
		return nil, errors.New("pulled an image that is present")
	}
	f.pulled = append(f.pulled, ref)
	f.missing = false
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeBrowserDocker) Info(context.Context) (system.Info, error) {
	return system.Info{LoggingDriver: "json-file"}, nil
}

func (f *fakeBrowserDocker) CreateContainer(_ context.Context, cfg *container.Config, hcfg *container.HostConfig, name string) (container.CreateResponse, error) {
	f.created = append(f.created, cfg)
	f.hosts = append(f.hosts, hcfg)
	f.names = append(f.names, name)
	return container.CreateResponse{ID: "b0b0b0b0b0b0b0b0"}, nil
}

func (f *fakeBrowserDocker) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	f.started = append(f.started, id)
	return f.startErr
}

func (f *fakeBrowserDocker) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeBrowserDocker) ContainerList(_ context.Context, opts container.ListOptions) ([]types.Container, error) {
	f.listed = append(f.listed, opts)
	return f.list, nil
}

func perRunConfig(t *testing.T) browserrun.Config {
	t.Helper()
	cfg, err := browserrun.ConfigFromEnv(func(k string) string {
		switch k {
		case browserrun.EnvMode:
			return string(browserrun.ModePerRun)
		case browserrun.EnvImage:
			return "registry.example/browser:3"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runEnv() map[string]string {
	return map[string]string{
		"OCTOPUS_API":       "http://octopus:8585",
		"OCTOPUS_API_KEY":   "oct_run_key",
		"ANTHROPIC_API_KEY": "sk-ant-tenant",
		"GITHUB_TOKEN":      "ghs_should_not_travel",
		"TASK_ID":           "task42",
		"WORKSPACE_ID":      "ws1",
		"INSTANCE_ID":       "coder-task-task42-dev-1",
	}
}

func TestStartRunBrowserJoinsTheAgentNetworkNamespace(t *testing.T) {
	f := &fakeBrowserDocker{}
	id, err := startRunBrowser(context.Background(), f, perRunConfig(t), "a9a9a9a9a9a9", runEnv())
	if err != nil {
		t.Fatal(err)
	}
	if id != "b0b0b0b0b0b0b0b0" || len(f.started) != 1 {
		t.Fatalf("id %q started %v", id, f.started)
	}
	cfg, host := f.created[0], f.hosts[0]

	if host.NetworkMode != "container:a9a9a9a9a9a9" {
		t.Errorf("NetworkMode = %q, want container:<agent id>", host.NetworkMode)
	}
	if len(host.PortBindings) != 0 || len(cfg.ExposedPorts) != 0 {
		t.Error("the browser publishes a port; it is reachable on the agent's loopback only")
	}
	if cfg.Image != "registry.example/browser:3" {
		t.Errorf("image %q", cfg.Image)
	}
	if f.names[0] != "octopus-browser-a9a9a9a9a9a9" {
		t.Errorf("name %q", f.names[0])
	}

	env := map[string]string{}
	for _, kv := range cfg.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	want := map[string]string{
		"MCP_REQUIRED_KEY": "oct_run_key", "OCTOPUS_API": "http://octopus:8585",
		"LISTEN_HOST": "127.0.0.1", "PORT": "3010", "HOME": "/tmp",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env %s = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("env carries %d vars, want %d: %v", len(env), len(want), cfg.Env)
	}

	if cfg.Labels[browserrun.LabelBrowser] != "true" || cfg.Labels[browserrun.LabelOwner] != "a9a9a9a9a9a9" {
		t.Errorf("labels %v", cfg.Labels)
	}
	if cfg.Labels[agent.LabelTask] != "task42" || cfg.Labels[agent.LabelWorkspaceID] != "ws1" ||
		cfg.Labels[agent.LabelInstance] != "coder-task-task42-dev-1" {
		t.Errorf("attribution labels %v", cfg.Labels)
	}
	if _, ok := cfg.Labels[agent.LabelManaged]; ok {
		t.Error("thulu.agent set: the watcher would take the browser for a run")
	}
}

func TestStartRunBrowserHasExactlyOneInit(t *testing.T) {
	withTini := &fakeBrowserDocker{entrypoint: []string{"/usr/bin/tini", "--"}}
	if _, err := startRunBrowser(context.Background(), withTini, perRunConfig(t), "a9a9a9a9a9a9", runEnv()); err != nil {
		t.Fatal(err)
	}
	if init := withTini.hosts[0].Init; init != nil {
		t.Errorf("Init = %v for an image whose entrypoint is tini, want unset", *init)
	}
	if len(withTini.created[0].Entrypoint) != 0 || len(withTini.created[0].Cmd) != 0 {
		t.Errorf("Entrypoint %q Cmd %q, want the image's", withTini.created[0].Entrypoint, withTini.created[0].Cmd)
	}

	bare := &fakeBrowserDocker{}
	if _, err := startRunBrowser(context.Background(), bare, perRunConfig(t), "a9a9a9a9a9a9", runEnv()); err != nil {
		t.Fatal(err)
	}
	if init := bare.hosts[0].Init; init == nil || !*init {
		t.Error("an image without an init must get Docker's")
	}
}

func TestRunBrowserPinsTheGrantedTools(t *testing.T) {
	env := runEnv()
	env["MCP_TOOLS"] = "thulu-browser@screenshot+get_text"
	cfg, _ := runBrowserContainer(perRunConfig(t), "img", "a9a9a9a9a9a9", env)
	if !slices.Contains(cfg.Env, browserrun.GrantedToolsEnv+"=screenshot,get_text") {
		t.Errorf("env %v does not pin the stage's grant", cfg.Env)
	}
}

func TestRunBrowserIsolation(t *testing.T) {
	cfg, host := runBrowserContainer(perRunConfig(t), "img", "a9a9a9a9a9a9", runEnv())
	if cfg.User != "1000:1000" {
		t.Errorf("user %q", cfg.User)
	}
	if !host.ReadonlyRootfs {
		t.Error("root filesystem writable")
	}
	if len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" || len(host.CapAdd) != 0 {
		t.Errorf("caps drop %v add %v", host.CapDrop, host.CapAdd)
	}
	if host.Privileged {
		t.Error("privileged")
	}
	found := false
	for _, o := range host.SecurityOpt {
		if o == "no-new-privileges:true" {
			found = true
		}
	}
	if !found {
		t.Errorf("security opts %v", host.SecurityOpt)
	}
	if host.Memory != 1536<<20 || host.MemorySwap != host.Memory {
		t.Errorf("memory %d swap %d", host.Memory, host.MemorySwap)
	}
	if host.NanoCPUs != 1_000_000_000 {
		t.Errorf("cpus %d", host.NanoCPUs)
	}
	if host.PidsLimit == nil || *host.PidsLimit <= 0 {
		t.Error("no pids limit")
	}
	if host.ShmSize < 1<<30 {
		t.Errorf("shm %d", host.ShmSize)
	}
	for _, dir := range []string{"/tmp", "/agents-shared"} {
		if !strings.Contains(host.Tmpfs[dir], "size=") {
			t.Errorf("%s is not a bounded tmpfs: %q", dir, host.Tmpfs[dir])
		}
	}
	if len(host.Mounts) != 0 || len(host.Binds) != 0 {
		t.Errorf("the browser mounts host paths: %v %v", host.Mounts, host.Binds)
	}
}

func TestStartRunBrowserCleansUpAFailedStart(t *testing.T) {
	f := &fakeBrowserDocker{startErr: errors.New("boom")}
	if _, err := startRunBrowser(context.Background(), f, perRunConfig(t), "a9a9a9a9a9a9", runEnv()); err == nil {
		t.Fatal("a failed start reported success")
	}
	if len(f.removed) != 1 || f.removed[0] != "b0b0b0b0b0b0b0b0" {
		t.Errorf("removed %v, want the created browser", f.removed)
	}
}

func TestStopRunBrowsersRemovesOnlyThatAgentsBrowser(t *testing.T) {
	f := &fakeBrowserDocker{list: []types.Container{{ID: "b1"}}}
	stopRunBrowsers(context.Background(), f, "a9a9a9a9a9a9")
	if len(f.listed) != 1 {
		t.Fatalf("listed %d times", len(f.listed))
	}
	opts := f.listed[0]
	if !opts.All {
		t.Error("stopped browsers would be missed")
	}
	labels := opts.Filters.Get("label")
	want := map[string]bool{
		browserrun.LabelBrowser + "=true":       true,
		browserrun.LabelOwner + "=a9a9a9a9a9a9": true,
	}
	if len(labels) != len(want) {
		t.Errorf("label filters %v", labels)
	}
	for _, l := range labels {
		if !want[l] {
			t.Errorf("unexpected label filter %q", l)
		}
	}
	if len(f.removed) != 1 || f.removed[0] != "b1" {
		t.Errorf("removed %v", f.removed)
	}
}

func TestTaskTeardownFindsTheBrowsersTheLaunchLabels(t *testing.T) {
	cfg, _ := runBrowserContainer(perRunConfig(t), "img", "a9a9a9a9a9a9", runEnv())
	for _, l := range taskBrowsers("task42").Get("label") {
		k, v, _ := strings.Cut(l, "=")
		if cfg.Labels[k] != v {
			t.Errorf("teardown filters on %s=%s, the browser carries %s=%q", k, v, k, cfg.Labels[k])
		}
	}
	f := &fakeBrowserDocker{list: []types.Container{{ID: "b1"}, {ID: "b2"}}}
	if n := removeRunBrowsers(context.Background(), f, taskBrowsers("task42")); n != 2 {
		t.Errorf("removed %d, want 2", n)
	}
}

func TestTaskTeardownLeavesAnotherInstallsBrowsers(t *testing.T) {
	dockerx.SetInstallID("mine")
	t.Cleanup(func() { dockerx.SetInstallID("") })

	f := &fakeBrowserDocker{list: []types.Container{
		{ID: "b1", Labels: map[string]string{dockerx.InstallLabel: "mine"}},
		{ID: "b2", Labels: map[string]string{dockerx.InstallLabel: "theirs"}},
	}}
	if n := removeRunBrowsers(context.Background(), f, taskBrowsers("task42")); n != 1 || !slices.Equal(f.removed, []string{"b1"}) {
		t.Errorf("removed %d %v, want only b1", n, f.removed)
	}
}

func TestEnsureRunBrowserImagePullsAMissingImage(t *testing.T) {
	f := &fakeBrowserDocker{missing: true}
	if err := ensureRunBrowserImage(context.Background(), f, perRunConfig(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.pulled) != 1 || f.pulled[0] != "registry.example/browser:3" {
		t.Errorf("pulled %v", f.pulled)
	}
	if _, err := startRunBrowser(context.Background(), f, perRunConfig(t), "a9a9a9a9a9a9", runEnv()); err != nil {
		t.Fatal(err)
	}
	if len(f.pulled) != 1 {
		t.Errorf("pulled %d times", len(f.pulled))
	}
}
