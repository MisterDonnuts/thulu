package agentimage_test

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/engine"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
)

func TestEnginesAreTheRegisteredOnes(t *testing.T) {
	known := engine.Known()
	slices.Sort(known)
	got := slices.Clone(agentimage.Engines)
	slices.Sort(got)
	if !slices.Equal(got, known) {
		t.Fatalf("agentimage.Engines = %v, the registered engines are %v", got, known)
	}
	if n, e := agentimage.Name("claude-code"), agentimage.Env("claude-code"); n != "octopus-agent-claude-code" || e != "OCTOPUS_AGENT_CLAUDE_CODE_IMAGE" {
		t.Fatalf("claude-code: %s, %s", n, e)
	}
}

func TestFor(t *testing.T) {
	env := map[string]string{
		"OCTOPUS_AGENT_CLAUDE_CODE_IMAGE": "ghcr.io/x/octopus-agent-claude-code@sha256:1",
		"OCTOPUS_AGENT_CODEX_IMAGE":       " ghcr.io/x/octopus-agent-codex@sha256:2 ",
	}
	imgs := agentimage.FromEnv(func(k string) string { return env[k] })

	cases := []struct{ engine, image, env string }{
		{"codex", "ghcr.io/x/octopus-agent-codex@sha256:2", "OCTOPUS_AGENT_CODEX_IMAGE"},
		{"", "ghcr.io/x/octopus-agent-claude-code@sha256:1", "OCTOPUS_AGENT_CLAUDE_CODE_IMAGE"},
		{"opencode", "", "OCTOPUS_AGENT_OPENCODE_IMAGE"},
	}
	for _, tc := range cases {
		if img, name := imgs.For(tc.engine); img != tc.image || name != tc.env {
			t.Errorf("For(%q) = %q, %q; want %q, %q", tc.engine, img, name, tc.image, tc.env)
		}
	}
	if got := imgs.All(); len(got) != 2 {
		t.Errorf("All = %v, want the two set", got)
	}

	env["OCTOPUS_AGENT_IMAGE"] = "registry.corp/agent:7"
	imgs = agentimage.FromEnv(func(k string) string { return env[k] })
	for _, e := range []string{"codex", "opencode", ""} {
		if img, name := imgs.For(e); img != "registry.corp/agent:7" || name != "OCTOPUS_AGENT_IMAGE" {
			t.Errorf("override, For(%q) = %q, %q", e, img, name)
		}
	}
	if got := imgs.All(); len(got) != 3 || got[0] != "registry.corp/agent:7" {
		t.Errorf("All = %v, want the override first", got)
	}
}
