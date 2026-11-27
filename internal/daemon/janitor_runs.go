package daemon

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/personallogin"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// The dispatcher writes the task row before it launches, so a younger container may belong to a
// task the janitor reads before its commit.
const deletedTaskAgentGrace = 2 * time.Minute

// reapDeletedTaskAgents removes the agents still running for a task that no longer exists: deleting
// a task stops its agents, but a delete that failed half way, or one from an older daemon, left them
// spending tokens with a revoked key until their deadline.
func (j *Janitor) reapDeletedTaskAgents(ctx context.Context, now time.Time) int {
	if j.tasks == nil {
		return 0
	}
	list, err := j.docker.ContainerList(ctx, container.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", agent.LabelManaged+"=true"),
		filters.Arg("status", "running"),
	)})
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.list_running_failed", "kind", "agent", "err", err))
		return 0
	}
	removed := 0
	for i := range list {
		c := &list[i]
		if dockerx.ForeignInstall(c.Labels) || now.Sub(time.Unix(c.Created, 0)) < deletedTaskAgentGrace {
			continue
		}
		inst := c.Labels[agent.LabelInstance]
		if inst == "" && len(c.Names) > 0 {
			inst = strings.TrimPrefix(c.Names[0], "/")
		}
		taskID := c.Labels[agent.LabelTask]
		if taskID == "" {
			taskID, _ = task.InstanceTaskID(inst)
		}
		if taskID == "" {
			continue
		}
		if _, err := j.tasks.GetRow(ctx, taskID); !errors.Is(err, task.ErrNotFound) {
			continue
		}
		if rmErr := j.docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.remove_container_failed", "kind", "agent", "id", shortID(c.ID), "err", rmErr))
			continue
		}
		log.Printf("[janitor] %s", i18n.Log("daemon.log.reaped_deleted_task_agent",
			"id", shortID(c.ID), "instance", inst, "task", taskID, "image", c.Image))
		removed++
	}
	return removed
}

// reapStaleLogins removes the personal-login containers created before cutoff. A flow lives at most
// personallogin.StaleAfter, and only in the memory of the daemon that started it.
func (j *Janitor) reapStaleLogins(ctx context.Context, cutoff time.Time) {
	list, err := j.docker.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", personallogin.ContainerLabel),
	)})
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.list_running_failed", "kind", "login", "err", err))
		return
	}
	for i := range list {
		c := &list[i]
		created := time.Unix(c.Created, 0)
		if dockerx.ForeignInstall(c.Labels) || !created.Before(cutoff) {
			continue
		}
		if rmErr := j.docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.remove_container_failed", "kind", "login", "id", shortID(c.ID), "err", rmErr))
			continue
		}
		log.Printf("[janitor] %s", i18n.Log("daemon.log.reaped_stale_login",
			"id", shortID(c.ID), "engine", c.Labels[personallogin.ContainerLabel], "age", time.Since(created).Round(time.Second)))
	}
}
