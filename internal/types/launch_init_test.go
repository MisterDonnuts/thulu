package types

import (
	"strings"
	"testing"
)

func TestStoredAgentImageFollowsTheEngine(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_IMAGE", "")
	t.Setenv("OCTOPUS_AGENT_CLAUDE_CODE_IMAGE", "ghcr.io/misterdonnuts/octopus-agent-claude-code@sha256:1")
	t.Setenv("OCTOPUS_AGENT_CODEX_IMAGE", "ghcr.io/misterdonnuts/octopus-agent-codex@sha256:2")
	t.Setenv("OCTOPUS_AGENT_OPENCODE_IMAGE", "")
	image := func(at AgentType, eng string) string {
		t.Helper()
		spec, err := launchSpec(at, eng)
		if err != nil {
			t.Fatal(err)
		}
		return spec.Image
	}
	coder := AgentType{Name: "coder", Type: "coder"}
	if got := image(coder, "codex"); !strings.Contains(got, "octopus-agent-codex@") {
		t.Errorf("codex run image = %s", got)
	}
	if got := image(coder, ""); !strings.Contains(got, "octopus-agent-claude-code@") {
		t.Errorf("default engine image = %s", got)
	}
	if got := image(AgentType{Name: "own", Image: "acme/agent:1"}, "codex"); got != "acme/agent:1" {
		t.Errorf("own image = %s", got)
	}
	_, err := launchSpec(coder, "opencode")
	if err == nil || !strings.Contains(err.Error(), "OCTOPUS_AGENT_OPENCODE_IMAGE") {
		t.Errorf("no opencode image: err = %v, want it to name the variable", err)
	}
	t.Setenv("OCTOPUS_AGENT_IMAGE", "registry.corp/agent:7")
	if got := image(coder, "opencode"); got != "registry.corp/agent:7" {
		t.Errorf("override image = %s", got)
	}
}

// Overriding Entrypoint or Cmd would put bash at PID 1 instead of the image's tini, unlike the
// process tree a Kubernetes pod gets.
func TestStoredAgentKeepsTheImageEntrypoint(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_IMAGE", "ghcr.io/misterdonnuts/octopus-agent:1")
	spec, err := launchSpec(AgentType{Name: "coder", Type: "coder"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := buildContainerConfig(spec.Image, nil, nil, spec)
	if len(cfg.Entrypoint) != 0 || len(cfg.Cmd) != 0 {
		t.Errorf("Entrypoint %q Cmd %q, want both unset so the image's tini stays PID 1", cfg.Entrypoint, cfg.Cmd)
	}
	hc, err := buildHostConfig(spec, nil, ResourceCaps{})
	if err != nil {
		t.Fatal(err)
	}
	if hc.Init != nil {
		t.Error("buildHostConfig must not set Init; dockerx.ApplyInit decides it from the image")
	}
}

func TestComposeEntrypointIsKept(t *testing.T) {
	spec := &ComposeServiceSpec{Image: "custom:1", Entrypoint: composeCommand{"/workspace/entrypoint.sh"}}
	cfg := buildContainerConfig(spec.Image, nil, nil, spec)
	if len(cfg.Entrypoint) != 1 || cfg.Entrypoint[0] != "/workspace/entrypoint.sh" {
		t.Errorf("Entrypoint = %q", cfg.Entrypoint)
	}
}

func TestDockerAgentStartsAsRootUnlessRootless(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_IMAGE", "ghcr.io/misterdonnuts/octopus-agent:1")
	spec, err := launchSpec(AgentType{Name: "coder", Type: "coder"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OCTOPUS_ROOTLESS_AGENTS", "")
	if cfg := buildContainerConfig(spec.Image, nil, nil, spec); cfg.User != "0:0" {
		t.Errorf("User = %q, want 0:0 so the root prologue runs", cfg.User)
	}
	t.Setenv("OCTOPUS_ROOTLESS_AGENTS", "1")
	if cfg := buildContainerConfig(spec.Image, nil, nil, spec); cfg.User != "1000:1000" {
		t.Errorf("rootless User = %q, want 1000:1000", cfg.User)
	}
}
