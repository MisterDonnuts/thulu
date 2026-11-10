package daemon

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/usage"
	"github.com/MisterDonnuts/thulu/internal/webhook"
)

// The API (api.Config.RunsDir) and the retention sweep both take the path from here so they cannot
// drift.
// Absolute because api.runOutputDir refuses a relative runs root and would silently drop run
// outputs.
func runOutputsDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	dir, err := filepath.Abs(filepath.Join(dataDir, "runs"))
	if err != nil {
		log.Printf("[daemon] %s", i18n.Log("daemon.log.run_outputs_dir_failed", "dir", dataDir, "err", err))
		return ""
	}
	return dir
}

type retention struct {
	settings  *settings.Store
	usage     *usage.Store
	webhooks  *webhook.Store
	auth      *auth.UserStore
	sandboxes *sandbox.Store
	chatRT    *chatruntime.Store
	tasks     *task.Store
	projects  *project.Store

	logsDir   string
	runsDir   string
	agentsDir string
	// Same sensitivity as a run's log, so pruned under the run-logs window.
	sessionsDir string

	now func() time.Time
}

func newRetention(st *stores, dataDir, agentsDir string) *retention {
	return &retention{
		settings:  st.settings,
		usage:     st.usage,
		webhooks:  st.webhook,
		auth:      st.auth,
		sandboxes: st.sandbox,
		chatRT:    st.chatRT,
		tasks:     st.task,
		projects:  st.project,
		logsDir:   filepath.Join(dataDir, "logs"),
		runsDir:   runOutputsDir(dataDir),
		agentsDir: agentsDir,
		// Derived from the runs dir exactly as the API derives it, so both name the same directory.
		sessionsDir: task.SessionsRootBeside(runOutputsDir(dataDir)),
		now:         time.Now,
	}
}

func (r *retention) run(ctx context.Context) {
	if r.settings == nil {
		return
	}
	r.apply(ctx, settings.RetentionRunLogsDaysKey, r.pruneRunFiles)
	r.apply(ctx, settings.RetentionSessionsDaysKey, r.pruneSessions)
	r.apply(ctx, settings.RetentionWebhookDeliveriesDaysKey, r.pruneWebhooks)
	r.apply(ctx, settings.RetentionRunKeysDaysKey, r.pruneRunKeys)
	r.apply(ctx, settings.RetentionSandboxRowsDaysKey, r.pruneSandboxRows)
	r.apply(ctx, settings.RetentionCacheMetricsDaysKey, r.pruneCacheMetrics)
	r.apply(ctx, settings.RetentionStuckWorktreeDaysKey, r.parkStuckWorktrees)
	r.closeStartedDelegations(ctx)
}

func (r *retention) apply(ctx context.Context, key string, step func(ctx context.Context, cutoff time.Time)) {
	if ctx.Err() != nil {
		return
	}
	days, err := r.settings.RetentionDays(ctx, key)
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_read_failed", "key", key, "err", err))
		return
	}
	if days <= 0 {
		return
	}
	step(ctx, r.now().Add(-time.Duration(days)*24*time.Hour))
}

func logPruned(what string, n int64, err error) {
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_step_failed", "what", what, "err", err, "count", n))
		return
	}
	if n > 0 {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_removed", "count", n, "what", what))
	}
}

// Deletion goes through os.Root so a symlink or ".." cannot reach outside the pruned dir; symlinks
// are skipped, never followed.
func (r *retention) pruneRunFiles(_ context.Context, cutoff time.Time) {
	if r.logsDir != "" {
		n, err := pruneOpenedDir(r.logsDir, cutoff)
		logPruned(i18n.Log("daemon.log.pruned_run_logs", "dir", r.logsDir), n, err)
	}
	if r.runsDir != "" {
		n, err := pruneOpenedDir(r.runsDir, cutoff)
		logPruned(i18n.Log("daemon.log.pruned_run_outputs", "dir", r.runsDir), n, err)
	}
	if r.sessionsDir != "" {
		// Per snapshot, not per task dir: a directory's mtime moves only when its own entries
		// change.
		n, err := task.PruneSessions(r.sessionsDir, cutoff)
		logPruned(i18n.Log("daemon.log.pruned_session_snapshots", "dir", r.sessionsDir), n, err)
	}
	if r.agentsDir == "" {
		return
	}
	agents, err := os.OpenRoot(r.agentsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		logPruned(i18n.Log("daemon.log.pruned_agents_run_outputs", "dir", r.agentsDir), 0, err)
		return
	}
	defer agents.Close()
	entries, err := readRootDir(agents)
	if err != nil {
		logPruned(i18n.Log("daemon.log.pruned_agents_run_outputs", "dir", r.agentsDir), 0, err)
		return
	}
	var total int64
	for _, e := range entries {
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		runs, openErr := agents.OpenRoot(filepath.Join(e.Name(), "runs"))
		if errors.Is(openErr, fs.ErrNotExist) {
			continue
		}
		if openErr != nil {
			logPruned(i18n.Log("daemon.log.pruned_agent_run_outputs", "agent", e.Name()), 0, openErr)
			continue
		}
		n, pruneErr := pruneRoot(runs, cutoff)
		logx.Warn(runs.Close(), "daemon.log.close_agent_runs_dir", "agent", e.Name())
		if pruneErr != nil {
			logPruned(i18n.Log("daemon.log.pruned_agent_run_outputs", "agent", e.Name()), n, pruneErr)
		}
		total += n
	}
	logPruned(i18n.Log("daemon.log.pruned_agents_total", "dir", r.agentsDir), total, nil)
}

func pruneOpenedDir(dir string, cutoff time.Time) (int64, error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer root.Close()
	return pruneRoot(root, cutoff)
}

func pruneRoot(root *os.Root, cutoff time.Time) (int64, error) {
	entries, err := readRootDir(root)
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !e.IsDir() && !e.Type().IsRegular() {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if rmErr := root.RemoveAll(e.Name()); rmErr != nil {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_remove_failed", "path", filepath.Join(root.Name(), e.Name()), "err", rmErr))
			continue
		}
		removed++
	}
	return removed, nil
}

func readRootDir(root *os.Root) ([]fs.DirEntry, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func (r *retention) pruneSessions(ctx context.Context, cutoff time.Time) {
	if r.usage == nil {
		return
	}
	sessions, events, err := r.usage.PruneSessions(ctx, cutoff)
	logPruned(i18n.Log("daemon.log.pruned_tool_events"), events, nil)
	logPruned(i18n.Log("daemon.log.pruned_usage_sessions"), sessions, err)
}

func (r *retention) pruneWebhooks(ctx context.Context, cutoff time.Time) {
	if r.webhooks == nil {
		return
	}
	deliveries, outbox, err := r.webhooks.PruneBefore(ctx, cutoff)
	logPruned(i18n.Log("daemon.log.pruned_webhook_deliveries"), deliveries, nil)
	logPruned(i18n.Log("daemon.log.pruned_webhook_outbox"), outbox, err)
}

// The CLI kills its worker at 150 s, so a delegation still started after this lost its pod.
const startedDelegationTTL = 15 * time.Minute

func (r *retention) closeStartedDelegations(ctx context.Context) {
	if r.usage == nil || ctx.Err() != nil {
		return
	}
	n, err := r.usage.CloseStartedDelegations(ctx, r.now().Add(-startedDelegationTTL))
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.close_started_delegations_failed", "err", err))
		return
	}
	if n > 0 {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.closed_started_delegations", "count", n))
	}
}

func (r *retention) pruneRunKeys(ctx context.Context, cutoff time.Time) {
	if r.auth == nil {
		return
	}
	n, err := r.auth.PruneRunKeys(ctx, cutoff)
	logPruned(i18n.Log("daemon.log.pruned_run_keys"), n, err)
}

func (r *retention) pruneSandboxRows(ctx context.Context, cutoff time.Time) {
	if r.sandboxes != nil {
		n, err := r.sandboxes.PruneReleasedBefore(ctx, cutoff)
		logPruned(i18n.Log("daemon.log.pruned_sandbox_rows"), n, err)
	}
	if r.chatRT != nil {
		n, err := r.chatRT.PruneReleasedBefore(ctx, cutoff)
		logPruned(i18n.Log("daemon.log.pruned_chat_rows"), n, err)
	}
}

func (r *retention) pruneCacheMetrics(ctx context.Context, cutoff time.Time) {
	if r.sandboxes == nil {
		return
	}
	n, err := r.sandboxes.PruneCacheMetricsBefore(ctx, cutoff)
	logPruned(i18n.Log("daemon.log.pruned_cache_metrics"), n, err)
}

// Parks rather than removes: pending changes are committed to the kept task branch first, and a
// failed commit keeps the worktree.
// A subtask shares its root's worktree, so the whole group must be idle.
func (r *retention) parkStuckWorktrees(ctx context.Context, cutoff time.Time) {
	if r.tasks == nil || r.projects == nil {
		return
	}
	all, err := r.tasks.List(ctx)
	if err != nil {
		logPruned(i18n.Log("daemon.log.pruned_stuck_worktrees"), 0, err)
		return
	}
	for _, root := range stuckWorktreeRoots(all, cutoff) {
		if ctx.Err() != nil {
			return
		}
		r.parkTask(ctx, root, cutoff)
	}
}

func stuckWorktreeRoots(all []task.Task, cutoff time.Time) []string {
	blocked := map[string]bool{}
	for i := range all {
		t := &all[i]
		if !isParkableWorktreeStatus(t.Status) || t.UpdatedAt.After(cutoff) {
			blocked[t.SandboxRootID()] = true
		}
	}
	var roots []string
	for i := range all {
		t := &all[i]
		if t.IsSubtask() || blocked[t.ID] || len(t.WorktreePaths) == 0 {
			continue
		}
		roots = append(roots, t.ID)
	}
	return roots
}

// Re-read: the list is a snapshot and the task may have moved since.
func (r *retention) parkTask(ctx context.Context, id string, cutoff time.Time) {
	t, err := r.tasks.Get(ctx, id)
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_reload_task_failed", "task", id, "err", err))
		return
	}
	if !isParkableWorktreeStatus(t.Status) || t.UpdatedAt.After(cutoff) || len(t.WorktreePaths) == 0 {
		return
	}
	kept := map[string]string{}
	var parked []string
	for pid, path := range t.WorktreePaths {
		if err := r.projects.ParkTaskWorktree(ctx, pid, t.ID); err != nil {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_park_worktree_failed", "task", t.ID, "project", pid, "err", err))
			kept[pid] = path
			continue
		}
		parked = append(parked, pid)
	}
	if len(parked) == 0 {
		return
	}
	if _, err := r.tasks.Update(ctx, t.ID, task.Patch{WorktreePaths: kept}); err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_forget_worktrees_failed", "task", t.ID, "err", err))
		return
	}
	log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_parked_worktrees",
		"count", len(parked), "task", t.ID, "status", t.Status, "ago", r.now().Sub(t.UpdatedAt).Round(time.Hour)))
	if _, err := r.tasks.AddNote(ctx, t.ID, "", task.SourceSystem, "octopus",
		task.NoteOf("daemon.comments.worktree_parked")); err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.retention_comment_failed", "task", t.ID, "err", err))
	}
}
