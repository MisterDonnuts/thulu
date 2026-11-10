package daemon

import (
	"context"
	"os"
	"strings"

	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/api"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/kubert"
)

const (
	envInfraProcRoot  = "OCTOPUS_INFRA_PROC_ROOT"
	envInfraDiskPaths = "OCTOPUS_INFRA_DISK_PATHS"
)

func startInfra(ctx context.Context, docker *client.Client, kubeRT *kubert.Runtime,
	st *stores, srv *api.Server, dataDir string) {
	cfg := infra.Config{
		ProcRoot: strings.TrimSpace(os.Getenv(envInfraProcRoot)),
		DataDir:  dataDir,
		DB:       st.db,
	}
	for _, p := range strings.Split(os.Getenv(envInfraDiskPaths), ",") {
		if p = strings.TrimSpace(p); p != "" {
			cfg.ExtraDisks = append(cfg.ExtraDisks, p)
		}
	}
	if st.settings != nil {
		cfg.Settings = st.settings
	}
	if st.sandbox != nil {
		cfg.SandboxesHeld = st.sandbox.CountLive
	}
	switch {
	case kubeRT != nil:
		cfg.Kube = kubeInfraSource(kubeRT)
	case docker != nil:
		cfg.Docker = docker
	}
	if d := srv.Dispatcher(); d != nil {
		cfg.Admission = func() infra.Admission {
			stage, conv := d.AdmissionStats()
			return infra.Admission{
				Agent:   infra.Gate{Cap: stage.Cap, Inflight: stage.Inflight, Waiting: stage.Waiting},
				Mention: infra.Gate{Cap: conv.Cap, Inflight: conv.Inflight, Waiting: conv.Waiting},
				Room:    d.RoomWaiting(),
			}
		}
	}
	if st.entitle != nil {
		cfg.Plan = func(workspaceID string) (string, int) {
			e, err := st.entitle.Get(workspaceID)
			if err != nil {
				return "", 0
			}
			return string(e.Plan), e.ConcurrencyLimit
		}
	}
	c := infra.New(cfg)
	srv.SetInfra(c)
	go c.Run(ctx)
}
