package updater

import (
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

func TestMirrorPullsEveryEngineImage(t *testing.T) {
	d := func(c string) string { return "@sha256:" + strings.Repeat(c, 64) }
	m := &release.Manifest{Version: "1.1.0", Images: map[string]string{"octopus": "ghcr.io/misterdonnuts/octopus" + d("1")}}
	for i, e := range agentimage.Engines {
		m.Images[agentimage.Name(e)] = "ghcr.io/misterdonnuts/" + agentimage.Name(e) + d(string(rune('a'+i)))
	}
	for registry, wantAll := range map[string]bool{"": false, "registry.corp.example/octopus": true} {
		imgs, err := planDockerImages(m, registry, &initwizard.EnvFile{}, []string{"codex"},
			"deploy/docker-compose.standalone.yml", false)
		if err != nil {
			t.Fatalf("registry %q: %v", registry, err)
		}
		for _, e := range agentimage.Engines {
			ref := imgs.Env[agentimage.Env(e)]
			if got, want := slices.Contains(imgs.Pull, ref), wantAll || e == "codex"; got != want {
				t.Errorf("registry %q: %s pulled = %v, want %v (%v)", registry, e, got, want, imgs.Pull)
			}
		}
	}
}
