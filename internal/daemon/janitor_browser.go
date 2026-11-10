package daemon

import (
	"context"
	"log"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func (j *Janitor) reapOrphanBrowsers(ctx context.Context) int {
	list, err := j.docker.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", browserrun.LabelBrowser+"=true")),
	})
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.list_exited_failed", "kind", "browser", "err", err))
		return 0
	}
	alive := func(id string) bool {
		info, err := j.docker.ContainerInspect(ctx, id)
		// Any inspect failure counts as gone: a transient error is retried next sweep, but a
		// permanent one would keep the browser forever.
		return err == nil && info.State != nil && info.State.Running
	}
	reaped := 0
	orphans := orphanBrowsers(list, alive)
	for i := range orphans {
		c := &orphans[i]
		if err := j.docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.remove_container_failed", "kind", "browser", "id", shortID(c.ID), "err", err))
			continue
		}
		log.Printf("[janitor] %s", i18n.Log("daemon.log.reaped_orphan_browser",
			"id", shortID(c.ID), "agent", c.Labels[browserrun.LabelOwner]))
		reaped++
	}
	return reaped
}

func orphanBrowsers(list []dtypes.Container, alive func(id string) bool) []dtypes.Container {
	var out []dtypes.Container
	for i := range list {
		if dockerx.ForeignInstall(list[i].Labels) {
			continue
		}
		owner := list[i].Labels[browserrun.LabelOwner]
		if owner == "" || !alive(owner) {
			out = append(out, list[i])
		}
	}
	return out
}
