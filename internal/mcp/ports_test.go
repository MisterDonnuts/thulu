package mcp

import (
	"testing"

	"github.com/docker/go-connections/nat"
)

func TestPortConfigKeepsTheLoopbackAddressOfAThreePartMapping(t *testing.T) {
	exposed, bindings, err := portConfig([]string{`"127.0.0.1:3012:3000"`})
	if err != nil {
		t.Fatal(err)
	}
	p := nat.Port("3000/tcp")
	if _, ok := exposed[p]; !ok {
		t.Fatalf("container port not exposed: %v", exposed)
	}
	got := bindings[p]
	if len(got) != 1 || got[0].HostIP != "127.0.0.1" || got[0].HostPort != "3012" {
		t.Fatalf("binding = %+v, want 127.0.0.1:3012", got)
	}
}

func TestPortConfigTwoPartMappingStillBindsEveryInterface(t *testing.T) {
	_, bindings, err := portConfig([]string{"3010:3000"})
	if err != nil {
		t.Fatal(err)
	}
	got := bindings[nat.Port("3000/tcp")]
	if len(got) != 1 || got[0].HostIP != "0.0.0.0" || got[0].HostPort != "3010" {
		t.Fatalf("binding = %+v, want 0.0.0.0:3010", got)
	}
}

func TestPortConfigRejectsAMappingWithoutAContainerPort(t *testing.T) {
	if _, _, err := portConfig([]string{"3000"}); err == nil {
		t.Fatal("want an error for a bare port")
	}
}

func TestFirstPortReadsHostAndContainerPastTheAddress(t *testing.T) {
	h, c := firstPort([]string{"127.0.0.1:3015:3000"})
	if h != 3015 || c != 3000 {
		t.Fatalf("firstPort = %d, %d; want 3015, 3000", h, c)
	}
	h, c = firstPort([]string{"3010:3000/tcp"})
	if h != 3010 || c != 3000 {
		t.Fatalf("firstPort = %d, %d; want 3010, 3000", h, c)
	}
}

func TestAgentNetworkConfigJoinsTheAgentsNetworkByServiceName(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_NETWORK", "octopus-agents")
	cfg := agentNetworkConfig("thulu-github")
	ep, ok := cfg.EndpointsConfig["octopus-agents"]
	if !ok {
		t.Fatalf("not attached to octopus-agents: %+v", cfg.EndpointsConfig)
	}
	if len(ep.Aliases) != 1 || ep.Aliases[0] != "thulu-github" {
		t.Fatalf("aliases = %v, want [thulu-github]", ep.Aliases)
	}
}

func TestAgentNetworkConfigLegacyBridgeLeavesTheDefaultBridge(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_NETWORK", "bridge")
	if cfg := agentNetworkConfig("thulu-github"); len(cfg.EndpointsConfig) != 0 {
		t.Fatalf("want no endpoints, got %+v", cfg.EndpointsConfig)
	}
}
