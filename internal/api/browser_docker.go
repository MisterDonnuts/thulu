package api

import (
	"context"
	"log"
	"sort"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/mcp"
)

type runBrowserDocker interface {
	imageref.DockerImages
	dockerx.InfoAPI
	CreateContainer(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, name string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
}

type dockerRunBrowser struct{ *client.Client }

func (d dockerRunBrowser) CreateContainer(
	ctx context.Context, cfg *container.Config, hcfg *container.HostConfig, name string,
) (container.CreateResponse, error) {
	return d.ContainerCreate(ctx, cfg, hcfg, nil, nil, name)
}

// Pull before the agent starts: the browser starts after it (joins its network namespace) and a
// first Chromium pull takes minutes, long enough for the agent to give up or exit.
func (l *taskLauncher) pullRunBrowserImage(ctx context.Context, env map[string]string) error {
	if !l.srv.browser.PerRun() || !browserrun.Startable(env) {
		return nil
	}
	return ensureRunBrowserImage(ctx, l.srv.docker, l.srv.browser)
}

func ensureRunBrowserImage(ctx context.Context, api imageref.DockerImages, cfg browserrun.Config) error {
	image := imageref.Resolve(cfg.Image)
	if err := imageref.EnsureLocal(ctx, api, image); err != nil {
		return apperr.Wrap(err, "api.launcher.errors.browser_image", "image", image)
	}
	return nil
}

// Starts after the agent, whose network namespace it joins; on failure the agent is removed, since
// it would wait forever for its browser.
func (l *taskLauncher) attachRunBrowser(ctx context.Context, agentID string, env map[string]string) error {
	if !l.srv.browser.PerRun() || !browserrun.Startable(env) {
		return nil
	}
	_, err := startRunBrowser(ctx, dockerRunBrowser{l.srv.docker}, l.srv.browser, agentID, env)
	if err == nil {
		return nil
	}
	if rmErr := l.srv.docker.ContainerRemove(ctx, agentID, container.RemoveOptions{Force: true}); rmErr != nil {
		log.Printf("[task] %s", i18n.Log("api.launcher.log.reap_failed", "id", agentID, "err", rmErr))
	}
	return err
}

// In per-run mode nothing may point at a shared thulu-browser, which would run scripts next to
// every other caller's key.
func (s *Server) wireBrowser(mcpFwd *mcp.Forwarder) {
	s.taskDispatcher.WithBrowser(s.browser)
	if s.browser.PerRun() && mcpFwd != nil {
		mcpFwd.Withhold(browserrun.URLEnv)
	}
}

const (
	// Chromium runs a few hundred threads across its browser, GPU and renderer processes.
	runBrowserPids = 1024
	// Docker's 64MB /dev/shm default crashes Chromium tabs on any real page.
	runBrowserShm      = 1 << 30
	runBrowserTmpfs    = "rw,nosuid,nodev,size=1g" //i18n:ignore tmpfs mount options
	runBrowserRestarts = 3
)

// Must not carry the thulu.agent label, or the watcher and reconcile scan take it for a run.
// From the agent's env it takes only the daemon URL, the run key and the granted tools.
func startRunBrowser(ctx context.Context, api runBrowserDocker, cfg browserrun.Config, agentID string, env map[string]string) (string, error) {
	if err := ensureRunBrowserImage(ctx, api, cfg); err != nil {
		return "", err
	}
	image := imageref.Resolve(cfg.Image)
	ccfg, hcfg := runBrowserContainer(cfg, image, agentID, env)
	hcfg.LogConfig = dockerx.ContainerLogConfig(ctx, api)
	dockerx.ApplyInit(ctx, api, image, ccfg.Entrypoint, hcfg)
	created, err := api.CreateContainer(ctx, ccfg, hcfg, runBrowserName(agentID))
	if err != nil {
		return "", apperr.Wrap(err, "api.launcher.errors.browser_create", "agent", agentID)
	}
	if err := api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		if rmErr := api.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			log.Printf("[task] %s", i18n.Log("api.launcher.log.browser_remove_failed", "id", shortContainerID(created.ID), "err", rmErr))
		}
		return "", apperr.Wrap(err, "api.launcher.errors.browser_start", "agent", agentID)
	}
	return created.ID, nil
}

func runBrowserContainer(cfg browserrun.Config, image, agentID string, env map[string]string) (*container.Config, *container.HostConfig) {
	server := cfg.ServerEnv(env["MCP_TOOLS"])
	server[browserrun.DaemonURLEnv] = env[browserrun.DaemonURLEnv]
	server[browserrun.RequiredKeyEnv] = env[browserrun.RunKeyEnv]
	list := make([]string, 0, len(server))
	for k, v := range server {
		list = append(list, k+"="+v)
	}
	sort.Strings(list)

	labels := map[string]string{
		browserrun.LabelBrowser: "true",
		browserrun.LabelOwner:   agentID,
	}
	for envKey, label := range map[string]string{
		"TASK_ID":      agent.LabelTask,
		"WORKSPACE_ID": agent.LabelWorkspaceID,
		"INSTANCE_ID":  agent.LabelInstance,
	} {
		if v := env[envKey]; v != "" {
			labels[label] = v
		}
	}
	dockerx.StampInstall(labels)

	pids := int64(runBrowserPids)
	ccfg := &container.Config{
		Image:  image,
		Env:    list,
		Labels: labels,
		// Spelled out so a retagged image cannot run the agent's scripts as root.
		User: "1000:1000",
	}
	hcfg := &container.HostConfig{
		NetworkMode:    container.NetworkMode("container:" + agentID),
		ReadonlyRootfs: true,
		Tmpfs: map[string]string{
			"/tmp":           runBrowserTmpfs,
			"/agents-shared": runBrowserTmpfs,
		},
		ShmSize:     runBrowserShm,
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"}, //i18n:ignore Docker security option
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyOnFailure, MaximumRetryCount: runBrowserRestarts,
		},
		Resources: container.Resources{
			Memory:     cfg.MemoryLimitBytes(),
			MemorySwap: cfg.MemoryLimitBytes(),
			NanoCPUs:   cfg.NanoCPUs(),
			PidsLimit:  &pids,
		},
	}
	return ccfg, hcfg
}

func runBrowserName(agentID string) string {
	return "octopus-browser-" + shortContainerID(agentID)
}

// Runs on agent exit regardless of the reap setting: the browser holds the run key.
func stopRunBrowsers(ctx context.Context, api runBrowserDocker, agentID string) {
	removeRunBrowsers(ctx, api, filters.NewArgs(
		filters.Arg("label", browserrun.LabelBrowser+"=true"),
		filters.Arg("label", browserrun.LabelOwner+"="+agentID),
	))
}

// Skips other installs' containers: an imported workspace keeps its task ids, so another install on
// the same engine can hold browsers with the same task label.
func removeRunBrowsers(ctx context.Context, api runBrowserDocker, f filters.Args) int {
	list, err := api.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		log.Printf("[task] %s", i18n.Log("api.launcher.log.browser_list_failed", "err", err))
		return 0
	}
	removed := 0
	for i := range list {
		if dockerx.ForeignInstall(list[i].Labels) {
			continue
		}
		if err := api.ContainerRemove(ctx, list[i].ID, container.RemoveOptions{Force: true}); err != nil {
			log.Printf("[task] %s", i18n.Log("api.launcher.log.browser_remove_failed", "id", shortContainerID(list[i].ID), "err", err))
			continue
		}
		removed++
	}
	return removed
}

func (s *Server) removeTaskBrowsers(ctx context.Context, taskID string) {
	if s.docker == nil || taskID == "" {
		return
	}
	removeRunBrowsers(ctx, dockerRunBrowser{s.docker}, taskBrowsers(taskID))
}

func taskBrowsers(taskID string) filters.Args {
	return filters.NewArgs(
		filters.Arg("label", browserrun.LabelBrowser+"=true"),
		filters.Arg("label", agent.LabelTask+"="+taskID),
	)
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
