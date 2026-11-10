package api

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
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
	if !t.IsSubtask() {
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
