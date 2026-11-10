package updater

import (
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/release"
)

func TestCaddyImageIsPulledOnTheStacksThatRunIt(t *testing.T) {
	d := func(c string) string { return "@sha256:" + strings.Repeat(c, 64) }
	m := &release.Manifest{
		Version:    "1.1.0",
		Images:     map[string]string{"octopus": "ghcr.io/misterdonnuts/octopus" + d("1")},
		ThirdParty: map[string]string{"caddy": "caddy:2-alpine" + d("2")},
	}
	for file, want := range map[string]bool{
		"deploy/docker-compose.yml":            true,
		"deploy/docker-compose.caddy.yml":      true,
		"deploy/docker-compose.standalone.yml": false,
	} {
		imgs, err := planDockerImages(m, "", &initwizard.EnvFile{}, nil, file, false)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if got := slices.Contains(imgs.Pull, m.ThirdParty["caddy"]); got != want {
			t.Errorf("%s: caddy pulled = %v, want %v (%v)", file, got, want, imgs.Pull)
		}
	}
}
