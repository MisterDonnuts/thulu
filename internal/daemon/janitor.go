package daemon

import (
	"context"
	"database/sql"
	"log"
	"os"
	"sync/atomic"
	"time"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/api"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/dockergc"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type Janitor struct {
	docker      *client.Client
	sandboxMgr  *sandbox.Manager
	tasks       *task.Store
	retention   *retention
	agentImages func(ctx context.Context) ([]string, error)

	interval             time.Duration
	agentExitedTTL       time.Duration
	chatExitedTTL        time.Duration
	sandboxExitedTTL     time.Duration
	failedTaskSandboxTTL time.Duration
	buildCacheTTL        time.Duration

	dataDir           string
	diskFree          func(path string) (float64, error)
	diskCheckInterval time.Duration
	pressureCooldown  time.Duration
	pressureExitedTTL time.Duration
	lastPressure      time.Time

	// Atomic: the operator's Status page reads these from request goroutines.
	lastSweep         atomic.Int64
	lastSweepPressure atomic.Bool
}

func newJanitor(d *client.Client, s *sandbox.Manager, t *task.Store, ret *retention, dataDir string) *Janitor {
	return &Janitor{
		docker:               d,
		sandboxMgr:           s,
		tasks:                t,
		retention:            ret,
		interval:             1 * time.Hour,
		agentExitedTTL:       2 * time.Hour,
		chatExitedTTL:        2 * time.Hour,
		sandboxExitedTTL:     6 * time.Hour,
		failedTaskSandboxTTL: 6 * time.Hour,
		buildCacheTTL:        7 * 24 * time.Hour,
		dataDir:              dataDir,
		diskFree:             diskFreePercent,
		diskCheckInterval:    5 * time.Minute,
		// Long enough that a disk held full by something the janitor cannot free doesn't sweep
		// every five minutes; short enough to catch a second burst the same hour.
		pressureCooldown: 30 * time.Minute,
		// A container that exited minutes ago is the one somebody is most likely about to inspect.
		pressureExitedTTL: 10 * time.Minute,
	}
}

// The 2-minute delay keeps the first sweep off the startup reconcile, which scans every exited
// container.
func (j *Janitor) Start(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		j.Sweep(ctx)
		sweepTick := time.NewTicker(j.interval)
		defer sweepTick.Stop()
		diskTick := time.NewTicker(j.diskCheckInterval)
		defer diskTick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sweepTick.C:
				j.Sweep(ctx)
			case <-diskTick.C:
				j.checkDiskPressure(ctx)
			}
		}
	}()
}

func (j *Janitor) Sweep(ctx context.Context) { j.sweep(ctx, false) }

func (j *Janitor) sweep(ctx context.Context, pressure bool) {
	start := time.Now()
	if j.docker != nil {
		j.sweepDocker(ctx, pressure)
	}
	failed := j.releaseFailedTaskSandboxes(ctx)
	if j.retention != nil {
		j.retention.run(ctx)
	}
	log.Printf("[janitor] %s", i18n.Log("daemon.log.janitor_sweep_done",
		"elapsed", time.Since(start).Round(time.Second), "released", failed, "pressure", pressure))
	j.lastSweepPressure.Store(pressure)
	j.lastSweep.Store(time.Now().UnixNano())
}

func (j *Janitor) Status() api.CleanupStatus {
	st := api.CleanupStatus{Every: j.interval, LastSweepPressure: j.lastSweepPressure.Load()}
	if ns := j.lastSweep.Load(); ns != 0 {
		st.LastSweep = time.Unix(0, ns)
	}
	if j.diskFree != nil && j.dataDir != "" {
		if free, err := j.diskFree(j.dataDir); err == nil {
			st.DiskFreePercent, st.DiskFreeKnown = free, true
		}
	}
	return st
}

func (j *Janitor) sweepDocker(ctx context.Context, pressure bool) {
	agentTTL, chatTTL, sandboxTTL := j.agentExitedTTL, j.chatExitedTTL, j.sandboxExitedTTL
	if pressure {
		agentTTL, chatTTL, sandboxTTL = j.pressureExitedTTL, j.pressureExitedTTL, j.pressureExitedTTL
	}
	agents := j.reapExitedByLabel(ctx, agent.LabelManaged+"=true", agentTTL, "agent")
	chats := j.reapExitedByLabel(ctx, agent.LabelChat+"=true", chatTTL, "chat")
	sandboxes := j.reapExitedByLabel(ctx, sandbox.SandboxLabel+"=true", sandboxTTL, "sandbox")
	j.reapOrphanBrowsers(ctx)
	imageBytes := j.pruneDanglingImages(ctx) + j.pruneOctopusImages(ctx)
	cacheBytes := j.pruneBuildCache(ctx, pressure)
	log.Printf("[janitor] %s", i18n.Log("daemon.log.janitor_docker_sweep",
		"agents", agents, "chats", chats, "sandboxes", sandboxes, "images", imageBytes/1024/1024, "cache", cacheBytes/1024/1024))
}

func (j *Janitor) checkDiskPressure(ctx context.Context) {
	minPercent := j.diskFreeMinPercent(ctx)
	if minPercent <= 0 || j.diskFree == nil || j.dataDir == "" {
		return
	}
	free, err := j.diskFree(j.dataDir)
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.disk_free_failed", "dir", j.dataDir, "err", err))
		return
	}
	now := time.Now()
	if !pressureSweepDue(free, minPercent, j.lastPressure, now, j.pressureCooldown) {
		return
	}
	j.lastPressure = now
	log.Printf("[janitor] %s", i18n.Log("daemon.log.disk_pressure", "free", free, "dir", j.dataDir, "floor", minPercent))
	j.sweep(ctx, true)
	if after, err := j.diskFree(j.dataDir); err == nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.disk_pressure_swept", "after", after, "before", free))
		if after < float64(minPercent) {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.disk_pressure_persists", "floor", minPercent))
		}
	}
}

func (j *Janitor) diskFreeMinPercent(ctx context.Context) int {
	const fallback = 10
	if j.retention == nil || j.retention.settings == nil {
		return fallback
	}
	n, err := j.retention.settings.JanitorDiskFreeMinPercent(ctx)
	if err != nil {
		return fallback
	}
	return n
}

func pressureSweepDue(freePercent float64, minPercent int, last, now time.Time, cooldown time.Duration) bool {
	if minPercent <= 0 || freePercent >= float64(minPercent) {
		return false
	}
	return last.IsZero() || now.Sub(last) >= cooldown
}

func (j *Janitor) reapExitedByLabel(ctx context.Context, label string, ttl time.Duration, kind string) int {
	f := filters.NewArgs(
		filters.Arg("label", label),
		filters.Arg("status", "exited"),
	)
	list, err := j.docker.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.list_exited_failed", "kind", kind, "err", err))
		return 0
	}
	cutoff := time.Now().Add(-ttl)
	reaped := 0
	for i := range list {
		c := &list[i]
		if dockerx.ForeignInstall(c.Labels) {
			continue
		}
		info, inspectErr := j.docker.ContainerInspect(ctx, c.ID)
		if inspectErr != nil || info.State == nil || info.State.FinishedAt == "" {
			continue
		}
		finished, parseErr := time.Parse(time.RFC3339Nano, info.State.FinishedAt)
		if parseErr != nil || finished.After(cutoff) {
			continue
		}
		if rmErr := j.docker.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); rmErr != nil {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.remove_container_failed", "kind", kind, "id", shortID(c.ID), "err", rmErr))
			continue
		}
		log.Printf("[janitor] %s", i18n.Log("daemon.log.reaped_container",
			"kind", kind, "id", shortID(c.ID), "ago", time.Since(finished).Round(time.Second)))
		reaped++
	}
	return reaped
}

func (j *Janitor) releaseFailedTaskSandboxes(ctx context.Context) int {
	if j.sandboxMgr == nil || j.tasks == nil {
		return 0
	}
	all, err := j.tasks.List(ctx)
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.list_tasks_failed", "err", err))
		return 0
	}
	now, grace := time.Now(), questionsSandboxGrace()
	released := 0
	for i := range all {
		t := &all[i]
		if !sandboxReleasable(t, now, j.failedTaskSandboxTTL, grace, j.docker != nil) {
			continue
		}
		if err := j.sandboxMgr.Release(ctx, t.ID); err != nil {
			log.Printf("[janitor] %s", i18n.Log("daemon.log.release_sandbox_failed", "task", t.ID, "err", err))
			continue
		}
		log.Printf("[janitor] %s", i18n.Log("daemon.log.released_sandbox",
			"task", t.ID, "status", t.Status, "ago", time.Since(t.UpdatedAt).Round(time.Second)))
		released++
	}
	return released
}

// Running and Idle are excluded: the dispatcher owns running tasks, the queue's idle eviction owns
// idle ones.
func isJanitorReleasableStatus(s task.RunStatus) bool {
	switch s {
	case task.StatusFailed, task.StatusOutOfTurns, task.StatusBlocked, task.StatusNeedsMoreTurns,
		task.StatusNotStarted, task.StatusCredentialBlocked, task.StatusNoAgent, task.StatusStopped,
		task.StatusResumeFailed:
		return true
	default:
		return false
	}
}

func (j *Janitor) pruneDanglingImages(ctx context.Context) uint64 {
	rep, err := j.docker.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.image_prune_failed", "err", err))
		return 0
	}
	if rep.SpaceReclaimed > 0 {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.dangling_images_pruned", "count", len(rep.ImagesDeleted), "mb", rep.SpaceReclaimed/1024/1024))
	}
	return rep.SpaceReclaimed
}

func (j *Janitor) pruneOctopusImages(ctx context.Context) uint64 {
	keep := dockergc.KeepFromEnv(os.Getenv)
	if j.agentImages != nil {
		imgs, err := j.agentImages(ctx)
		if err != nil {
			// Without the stored agents' images the keep set is incomplete, and a local-only agent
			// image removed now cannot be pulled back.
			log.Printf("[janitor] %s", i18n.Log("daemon.log.image_retention_skipped", "err", err))
			return 0
		}
		keep = append(keep, imgs...)
	}
	rollback, until := dockergc.RollbackFromEnv(os.Getenv)
	plan, res, err := dockergc.Sweep(ctx, j.docker, dockergc.Options{
		Keep:            keep,
		Registries:      dockergc.RegistriesFromEnv(os.Getenv),
		Rollback:        rollback,
		RollbackUntil:   until,
		Running:         buildinfo.Version,
		ComposeProjects: []string{dockergc.SelfComposeProject(ctx, j.docker)},
		AgentNetwork:    agentnet.Name(),
		ImagesOnly:      true,
	})
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.image_retention_failed", "err", err))
		return 0
	}
	if rerr := res.Err(); rerr != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.image_retention_failed", "err", rerr))
	}
	if res.Images > 0 {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.image_retention_removed",
			"removed", res.Images, "total", len(plan.Images), "mb", res.ImageBytes/1024/1024))
	}
	return uint64(max(res.ImageBytes, 0))
}

// No All=true on a regular sweep: it would strip cache the next build wants. Under disk pressure a
// slow build beats a daemon that cannot write.
func (j *Janitor) pruneBuildCache(ctx context.Context, pressure bool) uint64 {
	opts := dtypes.BuildCachePruneOptions{}
	what := i18n.Log("daemon.log.build_cache_unused")
	if pressure {
		opts.All = true
	} else {
		what = i18n.Log("daemon.log.build_cache_older_than", "ttl", j.buildCacheTTL.String())
		opts.Filters = filters.NewArgs(filters.Arg("until", j.buildCacheTTL.String()))
	}
	rep, err := j.docker.BuildCachePrune(ctx, opts)
	if err != nil {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.build_cache_prune_failed", "err", err))
		return 0
	}
	if rep.SpaceReclaimed > 0 {
		log.Printf("[janitor] %s", i18n.Log("daemon.log.build_cache_pruned", "what", what, "mb", rep.SpaceReclaimed/1024/1024))
	}
	return rep.SpaceReclaimed
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Handlers hide workspace_id = ” from restricted callers. Stamping is only unambiguous with
// exactly one workspace; elsewhere such rows stay hidden until assigned manually.
func startWorkspaceIDBackfill(ctx context.Context, db *sql.DB) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		if err := workspaceIDBackfill(ctx, db); err != nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.workspace_backfill_failed", "err", err))
		}
	}()
}

func workspaceIDBackfill(ctx context.Context, db *sql.DB) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&count); err != nil {
		return apperr.Wrap(err, "daemon.errors.count_workspaces")
	}
	if count != 1 {
		return nil
	}
	var wsID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM workspaces LIMIT 1`).Scan(&wsID); err != nil {
		return apperr.Wrap(err, "daemon.errors.resolve_workspace")
	}
	for _, tbl := range backfilledTables {
		// A store that is off on this install never created its table.
		if ok, err := migrate.ColumnExists(ctx, db, tbl, "workspace_id"); err != nil {
			return apperr.Wrap(err, "daemon.errors.stamp_table", "table", tbl)
		} else if !ok {
			continue
		}
		q := dbpkg.Rebind(`UPDATE ` + tbl + ` SET workspace_id = ? WHERE workspace_id = ''`)
		res, err := db.ExecContext(ctx, q, wsID)
		if err != nil {
			return apperr.Wrap(err, "daemon.errors.stamp_table", "table", tbl)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.workspace_backfill_stamped", "count", n, "table", tbl, "workspace", wsID))
		}
	}
	return nil
}

// Constant names only: they are spliced into the UPDATE SQL.
var backfilledTables = []string{
	"projects", "tasks", "chats", "chat_runtimes", "task_sandboxes", "automations",
}

func startProjectExcludesBackfill(ctx context.Context, ps *project.Store) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		projects, err := ps.List(ctx)
		if err != nil {
			log.Printf("[daemon] %s", i18n.Log("daemon.log.excludes_backfill_list_failed", "err", err))
			return
		}
		installed := 0
		for i := range projects {
			p := &projects[i]
			if err := ps.EnsureProjectExcludes(ctx, p.ID); err != nil {
				log.Printf("[daemon] %s", i18n.Log("daemon.log.excludes_backfill_failed", "project", p.ID, "err", err))
				continue
			}
			installed++
		}
		log.Printf("[daemon] %s", i18n.Log("daemon.log.excludes_backfill_done", "count", installed))
	}()
}
