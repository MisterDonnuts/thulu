//go:build foss

package daemon

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/api"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/kubert"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

func buildKubeRuntime() (*kubert.Runtime, error) {
	if !kubeRuntimeRequested() {
		return nil, nil
	}
	return nil, apperr.Unavailable("daemon.errors.kubernetes_not_in_build")
}

func kubeSandboxRuntime(*kubert.Runtime) sandbox.Runtime { return nil }

func kubeInfraSource(*kubert.Runtime) *infra.KubeSource { return nil }

func startImageBuild(context.Context, *kubert.Runtime) {}

func startRoomWatch(context.Context, *kubert.Runtime, *api.Server) {}

func startKubeCleanup(context.Context, *kubert.Runtime, *sandbox.Manager, *stores, *api.Server) {}
