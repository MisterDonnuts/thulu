package agentnet

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	EnvNetwork      = "OCTOPUS_AGENT_NETWORK"
	EnvNetworkAlias = "OCTOPUS_DOCKER_NETWORK"
	// Only applied at creation: an existing network keeps its interface.
	EnvBridge = "OCTOPUS_DOCKER_NETWORK_BRIDGE"
	EnvPeers  = "OCTOPUS_DOCKER_NETWORK_PEERS"
	// Native Linux engines need this for agents to reach the daemon; Docker Desktop and Colima
	// already resolve the name to the real host, which this would override.
	EnvHostGateway = "OCTOPUS_AGENT_HOST_GATEWAY"
)

const (
	DefaultNetwork = "octopus-agents"
	DefaultBridge  = "br-octopus"
	DefaultPeers   = "thulu-browser"
)

const ManagedLabel = "thulu.dev/managed-by"

// Both `docker compose` and the daemon's MCP manager stamp this label on MCP containers.
const composeServiceLabel = "com.docker.compose.service"

func Name() string { return nameFrom(os.Getenv) }

func nameFrom(getenv func(string) string) string {
	v := strings.TrimSpace(getenv(EnvNetwork))
	if v == "" {
		v = strings.TrimSpace(getenv(EnvNetworkAlias))
	}
	switch strings.ToLower(v) {
	case "":
		return DefaultNetwork
	case "bridge", "default":
		return ""
	}
	return v
}

func ExtraHosts() []string {
	if !truthy(os.Getenv(EnvHostGateway)) {
		return nil
	}
	//i18n:ignore Docker extra-hosts entry, not a message
	return []string{"host.docker.internal:host-gateway"}
}

func Peers() []string {
	v := os.Getenv(EnvPeers)
	if strings.TrimSpace(v) == "" {
		v = DefaultPeers
	}
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" && !strings.EqualFold(p, "none") {
			out = append(out, p)
		}
	}
	return out
}

type API interface {
	NetworkInspect(ctx context.Context, networkID string, options network.InspectOptions) (network.Inspect, error)
	NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
	NetworkConnect(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error)
}

var (
	ensuredMu sync.Mutex
	ensured   = map[string]bool{}
)

func Ensure(ctx context.Context, api API) (string, error) {
	name := Name()
	if name == "" {
		return "", nil
	}
	ensuredMu.Lock()
	defer ensuredMu.Unlock()
	if ensured[name] {
		return name, nil
	}
	if err := ensure(ctx, api, name, bridgeFrom(os.Getenv)); err != nil {
		return "", err
	}
	ensured[name] = true
	return name, nil
}

func bridgeFrom(getenv func(string) string) string {
	v := strings.TrimSpace(getenv(EnvBridge))
	switch strings.ToLower(v) {
	case "":
		return DefaultBridge
	case "auto":
		return ""
	}
	return v
}

func ensure(ctx context.Context, api API, name, bridge string) error {
	if _, err := api.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return apperr.Wrap(err, "agentnet.errors.inspect_network", "name", name)
	}
	opts := network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{ManagedLabel: "octopus"},
	}
	if bridge != "" {
		opts.Options = map[string]string{"com.docker.network.bridge.name": bridge}
	}
	if _, err := api.NetworkCreate(ctx, name, opts); err != nil {
		// A concurrent launch may have won the race.
		if _, ierr := api.NetworkInspect(ctx, name, network.InspectOptions{}); ierr == nil {
			return nil
		}
		return apperr.Wrap(err, "agentnet.errors.create_network",
			"name", name, "bridge", bridge, "network_env", EnvNetwork, "bridge_env", EnvBridge)
	}
	return nil
}

func ConnectPeers(ctx context.Context, api API, networkName string) error {
	peers := Peers()
	if networkName == "" || len(peers) == 0 {
		return nil
	}
	var firstErr error
	for _, p := range peers {
		list, err := api.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: filters.NewArgs(filters.Arg("label", composeServiceLabel+"="+p)),
		})
		if err != nil {
			return apperr.Wrap(err, "agentnet.errors.list_peer", "peer", p)
		}
		for i := range list {
			c := &list[i]
			if c.NetworkSettings != nil {
				if _, on := c.NetworkSettings.Networks[networkName]; on {
					continue
				}
			}
			err := api.NetworkConnect(ctx, networkName, c.ID, &network.EndpointSettings{})
			if err != nil && !isAlreadyConnected(err) && firstErr == nil {
				firstErr = apperr.Wrap(err, "agentnet.errors.connect_peer", "peer", p, "network", networkName)
			}
		}
	}
	return firstErr
}

// Docker reports an existing endpoint as a plain 403/500 with this text, not a typed error.
func isAlreadyConnected(err error) bool {
	msg := strings.ToLower(err.Error())
	//i18n:ignore matches the Docker engine's error text, not a message
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "already attached")
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
