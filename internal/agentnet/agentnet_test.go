package agentnet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

type fakeAPI struct {
	networks  map[string]network.CreateOptions
	createErr error
	peers     map[string][]types.Container
	connected []string
}

func (f *fakeAPI) NetworkInspect(_ context.Context, id string, _ network.InspectOptions) (network.Inspect, error) {
	if _, ok := f.networks[id]; ok {
		return network.Inspect{Name: id}, nil
	}
	return network.Inspect{}, errdefs.NotFound(errors.New("network not found"))
}

func (f *fakeAPI) NetworkCreate(_ context.Context, name string, o network.CreateOptions) (network.CreateResponse, error) {
	if f.createErr != nil {
		return network.CreateResponse{}, f.createErr
	}
	f.networks[name] = o
	return network.CreateResponse{ID: name}, nil
}

func (f *fakeAPI) NetworkConnect(_ context.Context, netID, cid string, _ *network.EndpointSettings) error {
	f.connected = append(f.connected, netID+"/"+cid)
	return nil
}

func (f *fakeAPI) ContainerList(_ context.Context, o container.ListOptions) ([]types.Container, error) {
	var out []types.Container
	for _, v := range o.Filters.Get("label") {
		for svc, cs := range f.peers {
			if v == composeServiceLabel+"="+svc {
				out = append(out, cs...)
			}
		}
	}
	return out, nil
}

func newFake() *fakeAPI {
	return &fakeAPI{networks: map[string]network.CreateOptions{}, peers: map[string][]types.Container{}}
}

func TestNameDefaultsAndLegacy(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	if nameFrom(get) != DefaultNetwork {
		t.Errorf("default network = %q", nameFrom(get))
	}
	env[EnvNetwork] = "bridge"
	if nameFrom(get) != "" {
		t.Error("bridge keeps the legacy default bridge")
	}
	env[EnvNetwork] = "corp-agents"
	if nameFrom(get) != "corp-agents" {
		t.Error("custom name honored")
	}
	delete(env, EnvNetwork)
	env[EnvNetworkAlias] = "old-agents"
	if nameFrom(get) != "old-agents" {
		t.Error("the older variable name is still read")
	}
	env[EnvNetwork] = "corp-agents"
	if nameFrom(get) != "corp-agents" {
		t.Error("the current name wins over the older one")
	}
	if bridgeFrom(get) != DefaultBridge {
		t.Error("default bridge interface")
	}
	env[EnvBridge] = "auto"
	if bridgeFrom(get) != "" {
		t.Error("auto lets Docker name the interface")
	}
}

func TestEnsureCreatesOnceAndAdopts(t *testing.T) {
	t.Setenv(EnvNetwork, "octopus-test-ensure")
	t.Setenv(EnvBridge, "")
	f := newFake()
	name, err := Ensure(context.Background(), f)
	if err != nil || name != "octopus-test-ensure" {
		t.Fatalf("Ensure = %q, %v", name, err)
	}
	opts := f.networks[name]
	if opts.Driver != "bridge" || opts.Options["com.docker.network.bridge.name"] != DefaultBridge || opts.Labels[ManagedLabel] == "" {
		t.Errorf("network created with %+v", opts)
	}
	f.createErr = errors.New("boom")
	delete(f.networks, name)
	if _, err := Ensure(context.Background(), f); err != nil {
		t.Errorf("cached Ensure must not fail: %v", err)
	}

	f2 := newFake()
	f2.networks["handmade"] = network.CreateOptions{}
	f2.createErr = errors.New("must not create")
	if err := ensure(context.Background(), f2, "handmade", DefaultBridge); err != nil {
		t.Errorf("existing network must be adopted: %v", err)
	}
}

func TestEnsureFailureNamesTheKnobs(t *testing.T) {
	f := newFake()
	f.createErr = errors.New("bridge name already in use")
	err := ensure(context.Background(), f, "octopus-agents", DefaultBridge)
	if err == nil {
		t.Fatal("create failure must surface")
	}
	for _, s := range []string{EnvNetwork, EnvBridge} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error must mention %s: %v", s, err)
		}
	}
}

func TestLegacyBridgeSkipsEnsure(t *testing.T) {
	t.Setenv(EnvNetwork, "default")
	name, err := Ensure(context.Background(), newFake())
	if err != nil || name != "" {
		t.Errorf("legacy mode: name=%q err=%v", name, err)
	}
}

func TestConnectPeers(t *testing.T) {
	t.Setenv(EnvPeers, "")
	f := newFake()
	on := types.Container{ID: "already", NetworkSettings: &types.SummaryNetworkSettings{
		Networks: map[string]*network.EndpointSettings{"octopus-agents": {}},
	}}
	f.peers["thulu-browser"] = []types.Container{{ID: "browser1"}, on}
	f.peers["other"] = []types.Container{{ID: "unrelated"}}
	if err := ConnectPeers(context.Background(), f, "octopus-agents"); err != nil {
		t.Fatal(err)
	}
	if len(f.connected) != 1 || f.connected[0] != "octopus-agents/browser1" {
		t.Errorf("connected = %v, want only the unattached browser", f.connected)
	}

	t.Setenv(EnvPeers, "none")
	f.connected = nil
	if err := ConnectPeers(context.Background(), f, "octopus-agents"); err != nil || len(f.connected) != 0 {
		t.Errorf("peers disabled: connected=%v err=%v", f.connected, err)
	}
}

func TestExtraHostsOptIn(t *testing.T) {
	t.Setenv(EnvHostGateway, "")
	if ExtraHosts() != nil {
		t.Error("host-gateway mapping is opt-in")
	}
	t.Setenv(EnvHostGateway, "true")
	if h := ExtraHosts(); len(h) != 1 || h[0] != "host.docker.internal:host-gateway" {
		t.Errorf("ExtraHosts = %v", h)
	}
}
