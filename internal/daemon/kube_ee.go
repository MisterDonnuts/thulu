//go:build !foss

package daemon

import (
	"context"
	"log"
	"os"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/api"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/edition"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/kubert"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

// The Kubernetes runtime is an Enterprise feature. Asked for without kubernetes in the license, the
// daemon refuses to start: running the agents in Docker instead would put them somewhere the
// operator did not choose.
func buildKubeRuntime() (*kubert.Runtime, error) {
	if !kubeRuntimeRequested() {
		return nil, nil
	}
	edition.Load()
	if !edition.Has(edition.Kubernetes) {
		return nil, apperr.Unavailable("daemon.errors.kubernetes_needs_license")
	}
	cfg, err := kuberuntime.ConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, apperr.Wrap(err, "daemon.errors.build_kube_runtime")
	}
	rt, err := kuberuntime.New(cfg)
	if err != nil {
		return nil, apperr.Wrap(err, "daemon.errors.build_kube_runtime")
	}
	// Before the first launch: netguard needs the node addresses to refuse them for webhooks and
	// automations.
	rt.RefreshNodeAddrs(context.Background())
	if host, herr := os.Hostname(); herr == nil {
		rt.CheckOwnPodSelector(context.Background(), host)
	}
	log.Printf("[daemon] %s", i18n.Log("daemon.log.agent_runtime_kubernetes"))
	return rt, nil
}

func kubeSandboxRuntime(kubeRT *kubert.Runtime) sandbox.Runtime {
	kcfg := sandbox.KubeConfigFromEnv(os.Getenv)
	if kcfg.Namespace == "" {
		kcfg.Namespace = "octopus-sandboxes"
	}
	kcfg.Provisioner = kubeRT
	kcfg.CABundle = kubeRT.CABundleConfigured()
	log.Printf("[daemon] %s", i18n.Log("daemon.log.sandbox_runtime_kubernetes", "namespace", kcfg.Namespace))
	return sandbox.NewKube(kubeRT.Clientset(), kcfg)
}

func kubeInfraSource(kubeRT *kubert.Runtime) *infra.KubeSource {
	return &infra.KubeSource{
		Client:                kubeRT.Clientset(),
		Metrics:               kubeRT.MetricsGet,
		ControlPlaneNamespace: kubeRT.ControlPlaneNamespace(),
		RuntimeClass:          kubeRT.RuntimeClassName(),
		AgentImages: func() []infra.AgentImageBuild {
			builds := kubeRT.ImageBuilds()
			out := make([]infra.AgentImageBuild, 0, len(builds))
			for i := range builds {
				b := &builds[i]
				out = append(out, infra.AgentImageBuild{
					Base: b.Base, Image: b.Image, State: b.State, Job: b.Job, Err: b.Err, Since: b.Since,
				})
			}
			return out
		},
	}
}

// The daemon holds the singleton lease by now, so only one replica ever starts a build.
func startImageBuild(ctx context.Context, kubeRT *kubert.Runtime) { kubeRT.StartImageBuild(ctx) }

func startRoomWatch(ctx context.Context, kubeRT *kubert.Runtime, srv *api.Server) {
	d := srv.Dispatcher()
	if d == nil {
		return
	}
	go kubeRT.WatchPodEnds(ctx, func(namespace string) {
		d.RoomFreed(func(workspaceID string) bool { return kubeRT.NamespaceFor(workspaceID) == namespace })
	})
}

func startKubeCleanup(ctx context.Context, kubeRT *kubert.Runtime, sandboxMgr *sandbox.Manager, st *stores, srv *api.Server) {
	srv.SetTenantTeardown(kubeRT.TeardownTenant)
	go reconcileTenantNamespaces(ctx, kubeRT, st)
	if d := srv.Dispatcher(); d != nil {
		newKubeJanitor(kubeRT, sandboxMgr, st.task, d).Start(ctx)
	}
}

// Tenant namespaces an older release created get today's labels before the janitor's first sweep
// selects on them.
func reconcileTenantNamespaces(ctx context.Context, kubeRT *kubert.Runtime, st *stores) {
	ids, err := st.auth.ListWorkspaceIDs(ctx)
	if err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.reconcile_namespaces_failed", "err", err))
		return
	}
	n := kubeRT.ReconcileTenantNamespaces(ctx, ids)
	log.Printf("[daemon] %s", i18n.Log("daemon.log.reconcile_namespaces_done", "count", n))
}
