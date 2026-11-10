package browserrun_test

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
)

func TestDockergcKnowsTheBrowserLabel(t *testing.T) {
	if dockergc.LabelBrowser != browserrun.LabelBrowser {
		t.Errorf("dockergc.LabelBrowser = %q, browserrun.LabelBrowser = %q", dockergc.LabelBrowser, browserrun.LabelBrowser)
	}
}

func TestResolvedImageIsKeptByTheJanitor(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"published":   {},
		"release":     {"OCTOPUS_VERSION": "v1.2.3"},
		"mirror":      {"OCTOPUS_IMAGE_REGISTRY": "registry.example/octo", "OCTOPUS_VERSION": "1.2.3"},
		"pinned mcp":  {"OCTOPUS_MCP_BROWSER_IMAGE": "ghcr.io/misterdonnuts/octopus-mcp-browser@sha256:abc"},
		"own browser": {browserrun.EnvImage: "registry.example/browser:7"},
	} {
		t.Run(name, func(t *testing.T) {
			getenv := func(k string) string { return env[k] }
			cfg, err := browserrun.ConfigFromEnv(getenv)
			if err != nil {
				t.Fatal(err)
			}
			if keep := dockergc.KeepFromEnv(getenv); !slices.Contains(keep, cfg.Image) {
				t.Errorf("%s is not in the keep set %v", cfg.Image, keep)
			}
		})
	}
}
