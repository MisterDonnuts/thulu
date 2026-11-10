package types

import (
	"context"
	"log"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/proxyenv"
)

const (
	// The socket is root on the Docker host: an agent holding it escapes every other control here.
	EnvAllowAgentDockerSocket = "OCTOPUS_ALLOW_AGENT_DOCKER_SOCKET"
	EnvAgentDockerSocketGID   = "OCTOPUS_AGENT_DOCKER_SOCKET_GID"
	EnvAllowAgentHostNetwork  = "OCTOPUS_ALLOW_AGENT_HOST_NETWORK"
	EnvFallbackDNS            = "OCTOPUS_FALLBACK_DNS"
)

// Exactly what the entrypoint's root prologue needs: chown run dirs (CHOWN, FOWNER), write
// /etc/group and resolv.conf (DAC_OVERRIDE), gosu to node (SETUID, SETGID).
var agentRootCaps = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID"}

// Mounting any of these hands out root on the Docker host; matched on source and target so a
// renamed mount is still caught.
var containerSockets = map[string]bool{"docker.sock": true, "podman.sock": true, "containerd.sock": true}

func isRuntimeSocket(m mount.Mount) bool {
	return containerSockets[path.Base(m.Target)] || containerSockets[path.Base(m.Source)]
}

func checkSocketMounts(agent string, mounts []mount.Mount) (bool, error) {
	for _, m := range mounts {
		if !isRuntimeSocket(m) {
			continue
		}
		if !truthyEnv(EnvAllowAgentDockerSocket) {
			return false, apperr.Forbidden("types.errors.socket_mount_refused",
				"agent", agent, "source", m.Source, "target", m.Target, "env", EnvAllowAgentDockerSocket)
		}
		return true, nil
	}
	return false, nil
}

func socketGroup() string {
	if v := strings.TrimSpace(os.Getenv(EnvAgentDockerSocketGID)); v != "" {
		if _, err := strconv.ParseUint(v, 10, 32); err == nil {
			return v
		}
		log.Printf("[launch] %s", i18n.Log("types.log.socket_gid_not_numeric", "env", EnvAgentDockerSocketGID))
	}
	if gid, ok := fileGID(dockerSocketPath); ok && gid != 0 {
		return strconv.FormatUint(uint64(gid), 10)
	}
	return ""
}

func resolveNetworkMode(agent, specMode, managed string) (container.NetworkMode, error) {
	mode := strings.TrimSpace(specMode)
	if mode == "" {
		return container.NetworkMode(managed), nil
	}
	if mode == "host" || strings.HasPrefix(mode, "container:") || strings.HasPrefix(mode, "service:") {
		if !truthyEnv(EnvAllowAgentHostNetwork) {
			return "", apperr.Forbidden("types.errors.network_mode_refused",
				"agent", agent, "mode", mode, "env", EnvAllowAgentHostNetwork)
		}
	}
	return container.NetworkMode(mode), nil
}

func hardenHostConfig(hc *container.HostConfig, rootless, socket bool) {
	hc.CapDrop = []string{"ALL"}
	if !rootless {
		hc.CapAdd = append([]string(nil), agentRootCaps...)
	}
	if socket {
		if gid := socketGroup(); gid != "" {
			hc.GroupAdd = append(hc.GroupAdd, gid)
		}
	}
	if !hc.NetworkMode.IsHost() && !hc.NetworkMode.IsContainer() {
		hc.ExtraHosts = append(hc.ExtraHosts, agentnet.ExtraHosts()...)
	}
}

func agentHostConfig(ctx context.Context, api agentnet.API, spec *ComposeServiceSpec, agent string,
	mounts []mount.Mount, caps ResourceCaps,
) (*container.HostConfig, error) {
	socket, err := checkSocketMounts(agent, mounts)
	if err != nil {
		return nil, err
	}
	hc, err := buildHostConfig(spec, mounts, caps)
	if err != nil {
		return nil, err
	}
	if err := applyAgentNetwork(ctx, api, hc, agent, spec.NetworkMode); err != nil {
		return nil, err
	}
	hardenHostConfig(hc, rootlessAgents(), socket)
	return hc, nil
}

func applyAgentNetwork(ctx context.Context, api agentnet.API, hc *container.HostConfig, agent, specMode string) error {
	managed := ""
	if strings.TrimSpace(specMode) == "" {
		name, err := agentnet.Ensure(ctx, api)
		if err != nil {
			return apperr.Wrap(err, "types.errors.agent", "agent", agent)
		}
		managed = name
	}
	mode, err := resolveNetworkMode(agent, specMode, managed)
	if err != nil {
		return err
	}
	hc.NetworkMode = mode
	return nil
}

func copyCABundle(ctx context.Context, docker *client.Client, containerID string, ca []byte) error {
	archive, err := catrust.Archive(ca)
	if err != nil {
		return apperr.Wrap(err, "types.errors.extra_ca_bundle")
	}
	if err := docker.CopyToContainer(ctx, containerID, "/", archive, container.CopyToContainerOptions{}); err != nil {
		return apperr.Wrap(err, "types.errors.copy_ca_bundle", "container", containerID)
	}
	return nil
}

func runtimeEnv(env []string) []string {
	have := make(map[string]bool, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			have[kv[:i]] = true
		}
	}
	extra := proxyenv.List(os.Getenv)
	if v := strings.TrimSpace(os.Getenv(EnvFallbackDNS)); v != "" {
		extra = append(extra, EnvFallbackDNS+"="+v)
	}
	for _, kv := range extra {
		if k := kv[:strings.IndexByte(kv, '=')]; !have[k] {
			env = append(env, kv)
		}
	}
	return env
}
