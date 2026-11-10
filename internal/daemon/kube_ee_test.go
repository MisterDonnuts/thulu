//go:build !foss

package daemon

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/edition/editiontest"
)

func TestBuildKubeRuntime_NeedsTheLicense(t *testing.T) {
	editiontest.Community(t)
	t.Setenv("OCTOPUS_AGENT_RUNTIME", "kubernetes")
	rt, err := buildKubeRuntime()
	if rt != nil || apperr.Code(err) != "daemon.errors.kubernetes_needs_license" {
		t.Fatalf("buildKubeRuntime on Community = %v, %v; want the license refusal", rt, err)
	}

	t.Setenv("OCTOPUS_AGENT_RUNTIME", "")
	if rt, err := buildKubeRuntime(); rt != nil || err != nil {
		t.Fatalf("Docker runtime = %v, %v; want nil, nil", rt, err)
	}
}
