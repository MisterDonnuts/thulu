package watcher

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/registry"
	"github.com/MisterDonnuts/thulu/internal/safepath"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type Watcher struct {
	docker       *client.Client
	registry     *registry.Registry
	collector    *usage.Collector
	projectStore *project.Store

	mu        sync.Mutex
	callbacks map[string]func(exitCode int, stopReason string)
}

func New(docker *client.Client, reg *registry.Registry, collector *usage.Collector, projectStore *project.Store) *Watcher {
	return &Watcher{
		docker:       docker,
		registry:     reg,
		collector:    collector,
		projectStore: projectStore,
		callbacks:    make(map[string]func(int, string)),
	}
}

func (w *Watcher) OnDie(containerID string, cb func(exitCode int, stopReason string)) {
	if len(containerID) > 12 {
		containerID = containerID[:12]
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks[containerID] = cb
}

func (w *Watcher) SyncExisting(ctx context.Context) error {
	f := filters.NewArgs(filters.Arg("label", agent.LabelManaged+"=true"))
	containers, err := w.docker.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil {
		return err
	}

	for i := range containers {
		if dockerx.ForeignInstall(containers[i].Labels) {
			continue
		}
		a := containerToAgent(containers[i])
		a.OutputDir = w.detectOutputDir(ctx, containers[i].ID)
		w.registry.Upsert(a)
		log.Printf("[watcher] %s", i18n.Log("watcher.log.synced_existing_agent", "name", a.Name, "id", a.ID, "output", a.OutputDir))
	}
	return nil
}

func (w *Watcher) Watch(ctx context.Context) {
	f := filters.NewArgs(
		filters.Arg("type", "container"),
		filters.Arg("label", agent.LabelManaged+"=true"),
	)

	msgCh, errCh := w.docker.Events(ctx, events.ListOptions{Filters: f})

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if err != nil && ctx.Err() == nil {
				log.Printf("[watcher] %s", i18n.Log("watcher.log.event_stream_error", "err", err))
				time.Sleep(5 * time.Second)
				msgCh, errCh = w.docker.Events(ctx, events.ListOptions{Filters: f})
			}
		case msg := <-msgCh:
			w.handleEvent(ctx, msg)
		}
	}
}

func (w *Watcher) handleEvent(ctx context.Context, msg events.Message) {
	switch msg.Action {
	case "start":
		info, err := w.docker.ContainerInspect(ctx, msg.Actor.ID)
		if err != nil {
			log.Printf("[watcher] %s", i18n.Log("watcher.log.inspect_failed", "container", msg.Actor.ID[:12], "err", err))
			return
		}
		if info.Config != nil && dockerx.ForeignInstall(info.Config.Labels) {
			return
		}
		a := inspectToAgent(info)
		a.OutputDir = w.detectOutputDir(ctx, msg.Actor.ID)
		w.ensureObsidianVault(a.OutputDir)
		w.registry.Upsert(a)
		log.Printf("[watcher] %s", i18n.Log("watcher.log.agent_started", "name", a.Name, "id", a.ID))

	case "die":
		exitCode := 0
		if v := msg.Actor.Attributes["exitCode"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				exitCode = n
			}
		}
		a, err := w.registry.Get(msg.Actor.ID[:12])
		if err != nil {
			return
		}
		now := time.Now()
		a.Status = agent.StatusExited
		a.FinishedAt = &now
		a.ExitCode = &exitCode

		a.StopReason = w.detectStopReason(ctx, msg.Actor.ID, a.Engine)

		// Read the output mount now: the container may be removed before anyone asks again.
		a.OutputDir = w.detectOutputDir(ctx, msg.Actor.ID)

		w.registry.Upsert(a)
		log.Printf("[watcher] %s", i18n.Log("watcher.log.agent_exited", "name", a.Name, "id", a.ID, "reason", a.StopReason))

		w.mu.Lock()
		cb, hasCB := w.callbacks[a.ID]
		if hasCB {
			delete(w.callbacks, a.ID)
		}
		w.mu.Unlock()
		if hasCB {
			go cb(exitCode, a.StopReason)
		}

		if w.collector != nil {
			pctx := context.WithoutCancel(ctx)
			go func() {
				collectCtx, cancel := context.WithTimeout(pctx, 60*time.Second)
				defer cancel()
				w.collector.Collect(collectCtx, a)
			}()
		}

	case "destroy":
		w.registry.Remove(msg.Actor.ID[:12])
	default:
	}
}

// The engine's result line is not the last one: the entrypoint logs the exit and posts outputs
// after it.
const stopReasonTailLines = "200"

func (w *Watcher) detectStopReason(ctx context.Context, containerID, engineName string) string {
	reader, err := w.docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       stopReasonTailLines,
	})
	if err != nil {
		return "unknown"
	}
	defer reader.Close()

	return stopReasonFromLog(dockerx.DemuxAll(reader), engineName)
}

func stopReasonFromLog(tail, engineName string) string {
	if engine.ResultOutcome([]byte(tail), engineName) == engine.OutcomeMaxTurns {
		return "max_turns"
	}
	return "completed"
}

func (w *Watcher) detectOutputDir(ctx context.Context, containerID string) string {
	dir, err := DetectOutputDir(ctx, w.docker, containerID)
	if err != nil {
		return ""
	}
	return dir
}

func DetectOutputDir(ctx context.Context, docker client.ContainerAPIClient, containerID string) (string, error) {
	info, err := docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	instanceID := InstanceIDFromEnv(info.Config.Env)
	for _, m := range info.Mounts {
		switch m.Destination {
		case "/workspace/output":
			return m.Source, nil
		case "/workspace/runs":
			return ResolveRunsOutputDir(m.Source, instanceID), nil
		}
	}
	return "", apperr.NotFound("watcher.errors.no_output_mount")
}

func InstanceIDFromEnv(env []string) string {
	for _, e := range env {
		if strings.HasPrefix(e, "INSTANCE_ID=") {
			return strings.TrimPrefix(e, "INSTANCE_ID=")
		}
	}
	return ""
}

// .instance_id is written by the entrypoint. The mount is container-writable, so reads go through
// safepath to refuse planted symlinks.
func ResolveRunsOutputDir(runsSource, instanceID string) string {
	if instanceID == "" {
		if data, err := safepath.ReadFile(runsSource, ".instance_id"); err == nil {
			instanceID = strings.TrimSpace(string(data))
		}
	}
	if instanceID != "" {
		if fi, err := safepath.Stat(runsSource, instanceID); err == nil && fi.IsDir() {
			return filepath.Join(runsSource, instanceID)
		}
	}
	return runsSource
}

func containerToAgent(c types.Container) *agent.Agent {
	status := agent.StatusUnknown
	switch c.State {
	case "running":
		status = agent.StatusRunning
	case "exited":
		status = agent.StatusExited
	}

	eng, projectID := resolveLabels(c.Labels)
	return &agent.Agent{
		ID:          c.ID[:12],
		Name:        trimSlash(c.Names),
		Type:        c.Labels[agent.LabelType],
		Project:     c.Labels[agent.LabelProject],
		Instance:    c.Labels[agent.LabelInstance],
		Engine:      eng,
		TaskID:      c.Labels[agent.LabelTask],
		WorkspaceID: c.Labels[agent.LabelWorkspaceID],
		ProjectID:   projectID,
		Stage:       c.Labels[agent.LabelStage],
		Image:       c.Image,
		Status:      status,
		StartedAt:   time.Unix(c.Created, 0),
	}
}

func inspectToAgent(info types.ContainerJSON) *agent.Agent {
	status := agent.StatusRunning
	started, err := time.Parse(time.RFC3339Nano, info.State.StartedAt)
	logx.Warn(err, "watcher.log.parse_start_time", "started_at", info.State.StartedAt)

	eng, projectID := resolveLabels(info.Config.Labels)
	return &agent.Agent{
		ID:          info.ID[:12],
		Name:        trimName(info.Name),
		Type:        info.Config.Labels[agent.LabelType],
		Project:     info.Config.Labels[agent.LabelProject],
		Instance:    info.Config.Labels[agent.LabelInstance],
		Engine:      eng,
		TaskID:      info.Config.Labels[agent.LabelTask],
		WorkspaceID: info.Config.Labels[agent.LabelWorkspaceID],
		ProjectID:   projectID,
		Stage:       info.Config.Labels[agent.LabelStage],
		Image:       info.Config.Image,
		Status:      status,
		StartedAt:   started,
	}
}

func resolveLabels(labels map[string]string) (engineName, projectID string) {
	engineName = strings.TrimSpace(labels[agent.LabelEngine])
	if engineName == "" {
		engineName = engine.DefaultName
	}
	projectID = strings.TrimSpace(labels[agent.LabelProjectID])
	if projectID == "" {
		projectID = strings.TrimSpace(labels[agent.LabelProject])
	}
	return engineName, projectID
}

func (w *Watcher) lookupContainerLabels(ctx context.Context, containerID string) (engineName, projectID string, ok bool) {
	info, err := w.docker.ContainerInspect(ctx, containerID)
	if err != nil || info.Config == nil {
		return "", "", false
	}
	if info.Config.Labels[agent.LabelManaged] != "true" {
		return "", "", false
	}
	eng, pid := resolveLabels(info.Config.Labels)
	return eng, pid, true
}

func (w *Watcher) BackfillSessionLabels(ctx context.Context) (int, error) {
	store := w.collector.Store()
	gaps, err := store.SessionsMissingLabels(ctx)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, g := range gaps {
		newEng, newPID := "", ""
		cEng, cPID, found := w.lookupContainerLabels(ctx, g.ID)
		switch {
		case found && g.Engine == "" && cEng != "":
			newEng = cEng
		case g.Engine == "":
			// Container is gone: record the default engine, else the row stays out of the
			// dashboard's engine filter forever.
			newEng = engine.DefaultName
		}
		if found && g.ProjectID == "" && cPID != "" {
			newPID = cPID
		}
		if newEng == "" && newPID == "" {
			continue
		}
		if err := store.SetSessionLabels(ctx, g.ID, newEng, newPID); err != nil {
			logx.Warn(err, "watcher.log.backfill_labels", "id", g.ID)
			continue
		}
		updated++
	}
	if updated > 0 {
		if err := store.RebuildDaily(ctx); err != nil {
			return updated, err
		}
	}
	return updated, nil
}

func (w *Watcher) ensureObsidianVault(outputDir string) {
	if outputDir == "" {
		return
	}
	runsDir := filepath.Dir(outputDir)
	agentRoot := filepath.Dir(runsDir)

	if filepath.Base(runsDir) != "runs" {
		return
	}

	obsDir := filepath.Join(agentRoot, ".obsidian")
	if _, err := os.Stat(obsDir); err == nil {
		return
	}

	if err := os.MkdirAll(obsDir, 0o750); err != nil {
		log.Printf("[watcher] %s", i18n.Log("watcher.log.obsidian_create_failed", "dir", obsDir, "err", err))
		return
	}

	appJSON := `{"attachmentFolderPath":"./","newFileLocation":"current","showFrontmatter":true,"useMarkdownLinks":false,"defaultViewMode":"preview"}`
	logx.Warn(
		os.WriteFile(filepath.Join(obsDir, "app.json"), []byte(appJSON), 0o644),
		"watcher.log.obsidian_write_failed", "file", "app.json", "dir", obsDir,
	)

	coreJSON := `["file-explorer","global-search","graph","backlink","tag-pane","page-preview","outline","properties"]`
	logx.Warn(
		os.WriteFile(filepath.Join(obsDir, "core-plugins.json"), []byte(coreJSON), 0o644),
		"watcher.log.obsidian_write_failed", "file", "core-plugins.json", "dir", obsDir,
	)

	log.Printf("[watcher] %s", i18n.Log("watcher.log.obsidian_initialized", "dir", agentRoot))
}

func trimSlash(names []string) string {
	if len(names) == 0 {
		return ""
	}
	n := names[0]
	if n != "" && n[0] == '/' {
		return n[1:]
	}
	return n
}

func trimName(name string) string {
	if name != "" && name[0] == '/' {
		return name[1:]
	}
	return name
}
