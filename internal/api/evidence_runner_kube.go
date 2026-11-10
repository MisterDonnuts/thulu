//go:build !foss

package api

import (
	"context"
	"time"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type kubeEvidenceLauncher struct{ s *Server }

func (l kubeEvidenceLauncher) Launch(ctx context.Context, spec evidence.RunnerSpec, onExit func(string)) (string, error) {
	kube := l.s.kube
	ws := spec.Batch.WorkspaceID
	if err := kube.EnsureTenantNamespace(ctx, ws); err != nil {
		return "", err
	}
	name, err := kube.LaunchPod(ctx, kuberuntime.LaunchSpec{
		WorkspaceID: ws,
		// Opaque task id so the task reconcile never matches a runner pod.
		TaskID:                "ev-" + spec.Batch.ID,
		Stage:                 evidence.RunnerStage,
		InstanceID:            "evidence-" + spec.Batch.ID,
		Image:                 l.s.browser.Image,
		Command:               append([]string{"/usr/bin/tini", "--"}, evidenceRunnerCommand...),
		Env:                   spec.Env,
		Labels:                map[string]string{LabelEvidenceBatch: spec.Batch.ID},
		ActiveDeadlineSeconds: int64(spec.Deadline / time.Second),
		BrowserJob:            true,
		// The sandbox's preview policies admit the runner by this label.
		SandboxRoot: spec.SandboxOwner,
	})
	if err != nil {
		return "", err
	}
	// The watch outlives the request that queued the batch.
	kube.WatchExit(context.WithoutCancel(ctx), name, kube.NamespaceFor(ws), func(code int) { onExit(exitReason(code)) })
	return name, nil
}

func (l kubeEvidenceLauncher) Stop(ctx context.Context, ws, name string) {
	logx.Warn(l.s.kube.DeletePod(ctx, name, l.s.kube.NamespaceFor(ws)), "api.evidence.log.stop_failed", "runner", name)
}
