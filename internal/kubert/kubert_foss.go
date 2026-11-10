//go:build foss

package kubert

import (
	"context"
	"io"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
)

// Runtime has the methods the core calls on the Kubernetes runtime. The daemon never builds one in
// this build, so every caller holds nil and checks for it first; the methods only keep the core
// compiling.
type Runtime struct{}

var errNotInBuild = apperr.Unavailable("daemon.errors.kubernetes_not_in_build")

func (*Runtime) Clientset() kubernetes.Interface { return nil }

func (*Runtime) ListPodsAll(context.Context) ([]kubeapi.RunningPod, error) { return nil, errNotInBuild }

func (*Runtime) FindPod(context.Context, string) (kubeapi.RunningPod, bool) {
	return kubeapi.RunningPod{}, false
}

func (*Runtime) FindPodNamespace(context.Context, string) (string, error) { return "", errNotInBuild }

func (*Runtime) DeletePod(context.Context, string, string) error { return errNotInBuild }

func (*Runtime) PodLogs(context.Context, string, string, bool) (io.ReadCloser, error) {
	return nil, errNotInBuild
}

func (*Runtime) PodLogsTail(context.Context, string, string, bool, int64) (io.ReadCloser, error) {
	return nil, errNotInBuild
}

func (*Runtime) PodLogsSince(context.Context, string, string, time.Time) (io.ReadCloser, error) {
	return nil, errNotInBuild
}

func (*Runtime) PodLogReadiness(context.Context, string, string) (kubeapi.LogReadiness, error) {
	return kubeapi.LogsGone, errNotInBuild
}
