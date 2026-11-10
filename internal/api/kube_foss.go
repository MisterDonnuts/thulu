//go:build foss

package api

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/kubert"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Stubs for the Kubernetes code in ee/. s.kube is always nil in this build, so none of them runs.

type kubeEvidenceLauncher struct{ s *Server }

func (kubeEvidenceLauncher) Launch(context.Context, evidence.RunnerSpec, func(string)) (string, error) {
	return "", apperr.Unavailable("daemon.errors.kubernetes_not_in_build")
}

func (kubeEvidenceLauncher) Stop(context.Context, string, string) {}

func (s *Server) kubeAgentLauncher(context.Context, *kubert.Runtime) task.AgentLauncher { return nil }
