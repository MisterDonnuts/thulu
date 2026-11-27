package api

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

// Shared by DELETE /tasks/{id} and the workspace purge. Containers and files are best-effort;
// database steps abort and the task row goes last, so a failed teardown can be retried.
// A subtask shares its parent's sandbox and worktree, so the parent's teardown removes them.
func (s *Server) teardownTask(ctx context.Context, t *task.Task) error {
	id := t.ID
	s.tornDown.Store(id, struct{}{})
	// First: the agents mount the worktree and the sandbox about to go, and keep spending tokens.
	stopped := s.stopTaskAgents(ctx, t)
	if t.IsSubtask() {
		s.reapSubtaskSandbox(ctx, t, stopped)
	} else {
		logx.Warn(s.destroySandbox(ctx, id), "api.tasks.log.destroy_sandbox", "task", id)
		for _, pid := range t.ProjectIDs {
			logx.Warn(s.projectStore.RemoveTaskWorktree(ctx, pid, id),
				"api.tasks.log.remove_worktree", "task", id, "project", pid)
		}
	}
	s.removeTaskBrowsers(ctx, id)
	if s.chatSvc != nil {
		s.chatSvc.EvictTaskChats(ctx, id)
	}
	if s.usageStore != nil {
		files, err := s.usageStore.DeleteByTask(ctx, id)
		if err != nil {
			return apperr.Wrap(err, "api.tasks.errors.delete_run_records_failed", "task", id)
		}
		s.removeRunFiles(files)
	}
	// Also needed beyond removeRunFiles: a run that died before reporting usage has no session row,
	// but its dir exists.
	s.removeTaskRunOutputs(id)
	logx.Warn(task.RemoveTaskSessions(s.sessionsRoot(), t.SandboxRootID(), id),
		"api.sessions.log.remove_task_sessions", "task", id)
	if s.authStore != nil {
		if _, err := s.authStore.DeleteTaskRunKeys(ctx, id); err != nil {
			return apperr.Wrap(err, "api.tasks.errors.delete_run_keys_failed", "task", id)
		}
	}
	return s.taskStore.Delete(ctx, id)
}

// Every container that carries the task's label except its chats, which EvictTaskChats owns: stage
// agents, mentions, auditors and their browsers. Each run is marked stopped before it dies, so its
// exit reads as a stop.
type taskContainerDocker interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]dtypes.Container, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
}

// stopTaskAgents returns the instances of the runs it stopped.
func (s *Server) stopTaskAgents(ctx context.Context, t *task.Task) []string {
	var instances []string
	mark := func(runID, instance string) {
		if instance != "" {
			instances = append(instances, instance)
		}
		if s.taskDispatcher != nil {
			s.taskDispatcher.MarkStopped(runID, "octopus")
		}
	}
	switch {
	case s.kube != nil:
		removeTaskPods(ctx, s.kube, t, mark)
	case s.docker != nil:
		removeTaskContainers(ctx, s.docker, t.ID, mark)
	}
	return instances
}

type subtaskReaper interface {
	ReapTask(ctx context.Context, ownerID, taskID string) error
	ReapRun(ctx context.Context, ownerID, instanceID string) error
}

// reapSubtaskSandbox kills what the subtask's runs left in its parent's sandbox, which stays. The
// exit callback of a run stopped here finds the task row gone and reaps nothing, and the janitor
// never knew the parent. The task marker finds every run's leftovers; the instance marker also
// finds those of runs started before execs carried the task.
func (s *Server) reapSubtaskSandbox(ctx context.Context, t *task.Task, instances []string) {
	if s.sandbox.mgr == nil {
		return
	}
	reapSubtaskIn(ctx, s.sandbox.mgr, t, instances)
}

func reapSubtaskIn(ctx context.Context, r subtaskReaper, t *task.Task, instances []string) {
	owner := t.SandboxRootID()
	logx.Warn(r.ReapTask(ctx, owner, t.ID), "api.tasks.log.reap_subtask_sandbox", "task", t.ID, "owner", owner)
	for _, inst := range instances {
		logx.Warn(r.ReapRun(ctx, owner, inst), "api.tasks.log.reap_subtask_run", "task", t.ID, "owner", owner, "instance", inst)
	}
}

type taskPodDeleter interface {
	DeleteTaskPods(ctx context.Context, workspaceID, taskID string, stop func(kubeapi.RunningPod) bool) (int, error)
}

// removeTaskPods deletes the task's pods by their label in its tenant namespace, so it finds the
// runs a restarted daemon no longer tracks. A pod whose instance names another task (the label is a
// sanitized id) is left alone.
func removeTaskPods(ctx context.Context, k taskPodDeleter, t *task.Task, markStopped func(runID, instance string)) int {
	n, err := k.DeleteTaskPods(ctx, t.WorkspaceID, t.ID, func(p kubeapi.RunningPod) bool {
		if owner, ok := task.InstanceTaskID(p.InstanceID); ok && owner != t.ID {
			return false
		}
		markStopped(p.Name, p.InstanceID)
		return true
	})
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.list_task_containers_failed", "task", t.ID, "err", err))
	}
	return n
}

func removeTaskContainers(ctx context.Context, d taskContainerDocker, taskID string, markStopped func(runID, instance string)) int {
	if taskID == "" {
		return 0
	}
	list, err := d.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(
		filters.Arg("label", agent.LabelTask+"="+taskID),
	)})
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.list_task_containers_failed", "task", taskID, "err", err))
		return 0
	}
	removed := 0
	for i := range list {
		c := &list[i]
		if dockerx.ForeignInstall(c.Labels) || c.Labels[agent.LabelChat] == "true" {
			continue
		}
		short := shortContainerID(c.ID)
		if c.Labels[agent.LabelManaged] == "true" {
			markStopped(short, agentInstanceOf(c.Labels, c.Names))
		}
		if rmErr := d.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); rmErr != nil && !errdefs.IsNotFound(rmErr) {
			log.Printf("[api] %s", i18n.Log("api.tasks.log.remove_task_container_failed", "task", taskID, "id", short, "err", rmErr))
			continue
		}
		log.Printf("[api] %s", i18n.Log("api.tasks.log.removed_task_container",
			"task", taskID, "id", short, "instance", c.Labels[agent.LabelInstance], "image", c.Image, "state", c.State))
		removed++
	}
	return removed
}

func (s *Server) destroySandbox(ctx context.Context, ownerID string) error {
	switch {
	case s.sandbox.mgr != nil:
		return s.sandbox.mgr.Destroy(ctx, ownerID)
	case s.sandbox.store != nil:
		return s.sandbox.store.Delete(ctx, ownerID)
	}
	return nil
}

// Paths come from the database, so each is confined before removal: logs under <data>/logs, output
// dirs a direct child of runsDir or of a runs/ dir under agentsDir.
func (s *Server) removeRunFiles(files []usage.RunFiles) {
	logsDir := ""
	if s.usageStore != nil && s.usageStore.DataDir() != "" {
		logsDir = filepath.Join(s.usageStore.DataDir(), "logs")
	}
	for _, f := range files {
		if f.LogsFile != "" && logsDir != "" {
			removeUnder(logsDir, f.LogsFile, false)
		}
		if f.OutputDir == "" {
			continue
		}
		if s.runsDir != "" {
			if rel, err := safepath.Rel(s.runsDir, f.OutputDir); err == nil && isRunInstanceName(rel) {
				logx.Warn(safepath.RemoveAll(s.runsDir, rel), "api.tasks.log.remove_run_output", "path", logx.Safe(f.OutputDir))
				continue
			}
		}
		if s.agentsDir != "" {
			removeUnder(s.agentsDir, f.OutputDir, true)
		}
	}
}

func (s *Server) removeTaskRunOutputs(taskID string) {
	if s.runsDir == "" || taskID == "" {
		return
	}
	root, err := os.OpenRoot(s.runsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.run_outputs_failed", "task", taskID, "err", err))
		return
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.run_outputs_failed", "task", taskID, "err", err))
		return
	}
	entries, err := dir.ReadDir(-1)
	logx.Warn(dir.Close(), "api.tasks.log.close_runs_dir")
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.run_outputs_failed", "task", taskID, "err", err))
		return
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 || !isRunInstanceName(e.Name()) {
			continue
		}
		if owner, ok := task.InstanceTaskID(e.Name()); !ok || owner != taskID {
			continue
		}
		logx.Warn(root.RemoveAll(e.Name()), "api.tasks.log.remove_task_run_output", "name", e.Name(), "task", taskID)
	}
}

func removeUnder(base, path string, runsDir bool) {
	rel, err := safepath.Rel(base, path)
	if err != nil || rel == "." {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.not_removing_outside", "path", logx.Safe(path), "base", logx.Safe(base)))
		return
	}
	if runsDir && filepath.Base(filepath.Dir(rel)) != "runs" {
		log.Printf("[api] %s", i18n.Log("api.tasks.log.not_removing_not_run_output", "path", logx.Safe(path)))
		return
	}
	logx.Warn(safepath.RemoveAll(base, rel), "api.tasks.log.remove_run_file", "path", logx.Safe(path))
}
