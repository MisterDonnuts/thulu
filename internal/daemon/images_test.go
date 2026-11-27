package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type fakeImages struct {
	mu      sync.Mutex
	present map[string]bool
	broken  bool
}

func (f *fakeImages) ImageInspectWithRaw(_ context.Context, ref string) (types.ImageInspect, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken {
		return types.ImageInspect{}, nil, errors.New("docker unreachable")
	}
	if f.present[ref] {
		return types.ImageInspect{ID: ref}, nil, nil
	}
	return types.ImageInspect{}, nil, errdefs.NotFound(errors.New("no such image"))
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// The sandbox image and the agent images a stored agent runs on are checked by the names the
// launchers ask Docker for; a name with a registry is pulled on first use, so it is never "missing".
func TestConfiguredImages(t *testing.T) {
	got := configuredImages(envOf(map[string]string{
		"OCTOPUS_SANDBOX_IMAGE":           "octopus-runtime:flujo-default",
		"OCTOPUS_AGENT_CLAUDE_CODE_IMAGE": "octopus-agent-claude-code:local",
		"OCTOPUS_AGENT_CODEX_IMAGE":       "ghcr.io/misterdonnuts/octopus-agent-codex:1.1.0",
		"OCTOPUS_AGENT_OPENCODE_IMAGE":    "octopus-runtime:flujo-default",
	}))
	if len(got) != 2 {
		t.Fatalf("configuredImages = %+v; want the sandbox and the claude-code image", got)
	}
	if got[0].Ref != "octopus-runtime:flujo-default" || got[0].Env != "OCTOPUS_SANDBOX_IMAGE" ||
		got[0].Fix != "docker build -t octopus-runtime:flujo-default images/runtime/" {
		t.Errorf("sandbox image = %+v", got[0])
	}
	if got[1].Env != "OCTOPUS_AGENT_CLAUDE_CODE_IMAGE" || !strings.Contains(got[1].Fix, "ENGINES=claude-code -t octopus-agent-claude-code:local") {
		t.Errorf("agent image = %+v", got[1])
	}

	if got := configuredImages(envOf(nil)); len(got) != 1 || got[0].Ref != "octopus-runtime:latest" {
		t.Errorf("defaults = %+v; want the default sandbox image alone", got)
	}
	mirrored := configuredImages(envOf(map[string]string{"OCTOPUS_IMAGE_REGISTRY": "registry.local:5000"}))
	if len(mirrored) != 0 {
		t.Errorf("with a registry mirror = %+v; want nothing to check, every image is pulled", mirrored)
	}
	override := configuredImages(envOf(map[string]string{
		"OCTOPUS_AGENT_IMAGE": "mine:1", "OCTOPUS_AGENT_CODEX_IMAGE": "codex:1"}))
	if len(override) != 2 || override[1].Env != "OCTOPUS_AGENT_IMAGE" {
		t.Errorf("with the override = %+v; want it alone among the agent images", override)
	}
}

func TestImageCheckListsMissingImagesWithTheirFix(t *testing.T) {
	docker := &fakeImages{present: map[string]bool{"octopus-agent-claude-code:local": true}}
	c := newImageCheck(docker, envOf(map[string]string{
		"OCTOPUS_SANDBOX_IMAGE":           "octopus-runtime:flujo-default",
		"OCTOPUS_AGENT_CLAUDE_CODE_IMAGE": "octopus-agent-claude-code:local",
	}))
	c.check(t.Context())
	probs := c.Problems()
	if len(probs) != 1 || probs[0].Source != "images" {
		t.Fatalf("Problems = %+v; want the sandbox image", probs)
	}
	var ae *apperr.Error
	if !errors.As(probs[0].Err, &ae) || ae.Key != "daemon.problems.image_missing" {
		t.Fatalf("problem err = %v", probs[0].Err)
	}
	if msg := apperr.Text(probs[0].Err); !strings.Contains(msg, "docker build -t octopus-runtime:flujo-default images/runtime/") {
		t.Errorf("problem message %q does not say how to fix it", msg)
	}

	// Docker going away says nothing new about the images: the last answer stays.
	docker.mu.Lock()
	docker.broken = true
	docker.mu.Unlock()
	c.check(t.Context())
	if len(c.Problems()) != 1 {
		t.Errorf("Problems with Docker unreachable = %+v; want the last answer kept", c.Problems())
	}

	// Building it clears the problem on the next check.
	docker.mu.Lock()
	docker.broken = false
	docker.present["octopus-runtime:flujo-default"] = true
	docker.mu.Unlock()
	c.check(t.Context())
	if p := c.Problems(); len(p) != 0 {
		t.Errorf("Problems after the build = %+v; want none", p)
	}
}
