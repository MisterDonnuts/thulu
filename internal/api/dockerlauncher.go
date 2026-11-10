package api

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/enginecache"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type taskLauncher struct {
	srv *Server
}

func (l *taskLauncher) FindAgentType(workspaceID, name string) *types.AgentType {
	return types.FindAgent(l.srv.agents(workspaceID), name)
}

func (l *taskLauncher) LaunchForStage(
	ctx context.Context,
	_ *task.Task,
	stage task.Stage,
	agentType *types.AgentType,
	env map[string]string,
	worktreePaths map[string]string,
	stagePaths map[string]string,
	workspaceReadOnly bool,
) (string, string, string, error) {
	if env == nil {
		env = map[string]string{}
	}
	env["STAGE"] = string(stage)

	// Only when the workspace resolved no credential: overlaying after a profile put the host's
	// OAuth token into a run routed to the operator's key.
	// Use the resolved engine (env ENGINE), not the agent's label: a profile may switch engines.
	overlayHostAuth(ctx, env, agentType)

	params := types.LaunchParams{
		Env:               env,
		NoBuild:           true,
		WorkspacePaths:    worktreePaths,
		StagePaths:        stagePaths,
		WorkspaceReadOnly: workspaceReadOnly,
	}
	if ws := env["WORKSPACE_ID"]; ws != "" {
		params.EngineCacheScope = enginecache.WorkspaceScope(ws)
	}
	vols, mountErr := personalLoginMount(env)
	if mountErr != nil {
		return "", "", "", mountErr
	}
	params.NamedVolumes = append(params.NamedVolumes, vols...)
	// Reconcile reads this label back to finish a run whose exit callback a restart lost.
	if kind := env[task.EnvLaunchKind]; kind != "" {
		params.LabelOverrides = map[string]string{task.LabelLaunchKind: kind}
	}

	if l.srv.settingsStore != nil {
		params.Resources = l.srv.settingsStore.AgentResourceCaps(ctx)
	}

	// OCTOPUS_DATA_VOLUME is set only when the daemon runs containerized; paths must then be
	// subpaths of the data volume both daemon and agents mount.
	dataRoot := ""
	if dv := strings.TrimSpace(os.Getenv("OCTOPUS_DATA_VOLUME")); dv != "" {
		params.UseCopyMode = true
		params.DataVolume = dv
		params.OutputContainerPath = "/workspace/runs/" + env["INSTANCE_ID"]
		dataRoot = l.srv.projectStore.DataRoot()
		params.WorkspacePaths = types.VolumeSubpaths("launch", dataRoot, worktreePaths)
		params.StagePaths = types.VolumeSubpaths("launch", dataRoot, stagePaths)
	}
	// The worktrees' .git pointers name the clone by its daemon path, so it must be mounted there
	// (types/worktreegit.go).
	params.AddWorktreeGit("launch", worktreePaths, dataRoot)

	if err := l.pullRunBrowserImage(ctx, env); err != nil {
		return "", "", "", err
	}
	containerID, err := types.Launch(ctx, l.srv.docker, *agentType, params)
	if err != nil {
		return "", "", "", err
	}
	if len(containerID) > 12 {
		containerID = containerID[:12]
	}
	if err := l.attachRunBrowser(ctx, containerID, env); err != nil {
		return "", "", "", err
	}

	outputDir := l.srv.runOutputDirOf(env["INSTANCE_ID"])
	return containerID, env["INSTANCE_ID"], outputDir, nil
}

func overlayHostAuth(ctx context.Context, env map[string]string, agentType *types.AgentType) {
	if !engine.HostAuthApplies(env) {
		return
	}
	engName := env["ENGINE"]
	if engName == "" {
		engName = agentType.Engine
	}
	eng, err := engine.Get(engName)
	if err != nil {
		return
	}
	for _, kv := range eng.AuthEnv(ctx) {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
}

// The volume variable is removed from env so the run never sees it.
func personalLoginMount(env map[string]string) ([]types.ExtraMount, error) {
	vol := env[task.EnvPersonalLoginVolume]
	if vol == "" {
		return nil, nil
	}
	delete(env, task.EnvPersonalLoginVolume)
	pl := engine.PersonalLoginOf(env["ENGINE"])
	if pl == nil {
		return nil, apperr.Internal("task.errors.engine_without_personal_login", "engine", env["ENGINE"])
	}
	return []types.ExtraMount{{Source: vol, Target: pl.ConfigDir}}, nil
}

func (l *taskLauncher) OnExit(ctx context.Context, containerID string, cb func(int, string, string)) {
	l.srv.watcher.OnDie(containerID, func(exitCode int, stopReason string) {
		// Stop the run's browser first and always: it holds the run key.
		if l.srv.docker != nil {
			stopRunBrowsers(ctx, dockerRunBrowser{l.srv.docker}, containerID)
		}
		// Outputs live only inside the container: copy them out before cb (postStageRun reads them)
		// and before the reap below destroys them.
		outDir, wtPath := l.collectRunOutputs(ctx, containerID)
		log.Printf("[task] %s", i18n.Log("api.launcher.log.container_exited",
			"id", containerID, "code", exitCode, "reason", stopReason, "dir", outDir))
		cb(exitCode, outDir, wtPath)
		// Reap only after cb has read the outputs.
		l.reapAgentContainer(ctx, containerID)
	})
}

func (l *taskLauncher) reapAgentContainer(ctx context.Context, containerID string) {
	if l.srv.settingsStore != nil {
		if on, err := l.srv.settingsStore.AgentReapOnExit(ctx); err == nil && !on {
			return
		}
	}
	if err := l.srv.docker.ContainerRemove(ctx, containerID,
		container.RemoveOptions{Force: true}); err != nil {
		log.Printf("[task] %s", i18n.Log("api.launcher.log.reap_failed", "id", containerID, "err", err))
		return
	}
	log.Printf("[task] %s", i18n.Log("api.launcher.log.reaped", "id", containerID))
}

func (l *taskLauncher) collectRunOutputs(ctx context.Context, containerID string) (outDir, wtPath string) {
	info, err := l.srv.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		log.Printf("[task] %s", i18n.Log("api.launcher.log.inspect_failed", "id", containerID, "err", err))
		return "", ""
	}
	if info.Config != nil {
		for k, v := range info.Config.Labels {
			if strings.HasPrefix(k, "thulu.workspace-worktree") {
				wtPath = v
				break
			}
		}
	}
	inst := containerInstanceID(info)
	outDir = l.srv.runOutputDirOf(inst)
	if outDir == "" {
		log.Printf("[task] %s", i18n.Log("api.launcher.log.no_output_dir", "id", containerID, "instance", inst, "root", l.srv.runsDir))
		return "", wtPath
	}
	if err := types.RetrieveOutputs(ctx, l.srv.docker, containerID, "/workspace/runs/"+inst, outDir); err != nil {
		log.Printf("[task] %s", i18n.Log("api.launcher.log.copy_out_failed", "id", containerID, "err", err))
	}
	return outDir, wtPath
}

func containerNameOf(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func (l *taskLauncher) ContainerAlive(containerID string) bool {
	return l.srv.containerRunning(context.Background(), containerID)
}

func (l *taskLauncher) ScanExitedTaskAgents(ctx context.Context) ([]task.ExitedTaskAgent, error) {
	f := filters.NewArgs(
		filters.Arg("label", agent.LabelManaged+"=true"),
		filters.Arg("status", "exited"),
	)
	containers, err := l.srv.docker.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	out := make([]task.ExitedTaskAgent, 0, len(containers))
	for i := range containers {
		if dockerx.ForeignInstall(containers[i].Labels) {
			continue
		}
		instanceID := containers[i].Labels[agent.LabelInstance]
		if instanceID == "" {
			// Containers launched while agent definitions moved to Postgres lack the instance
			// label; the container name is the instance id.
			instanceID = containerNameOf(containers[i].Names)
		}
		agentType := containers[i].Labels[agent.LabelType]
		shortID := containers[i].ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		// A run whose exit a restart lost never had outputs copied out: copy now, while the
		// container exists. An existing dir was filled by OnExit.
		outDir := l.srv.runOutputDirOf(instanceID)
		if _, replayable := task.InstanceTaskID(instanceID); replayable && outDir != "" {
			if _, statErr := os.Stat(outDir); errors.Is(statErr, fs.ErrNotExist) {
				logx.Warn(types.RetrieveOutputs(ctx, l.srv.docker, containers[i].ID, "/workspace/runs/"+instanceID, outDir),
					"api.launcher.log.copy_out_exited", "id", shortID)
			}
		}
		// ContainerList doesn't include the exit code.
		exitCode := 0
		if info, err := l.srv.docker.ContainerInspect(ctx, containers[i].ID); err == nil && info.State != nil {
			exitCode = info.State.ExitCode
		}
		out = append(out, task.ExitedTaskAgent{
			ContainerID: shortID,
			InstanceID:  instanceID,
			AgentType:   agentType,
			OutputDir:   outDir,
			ExitCode:    exitCode,
			LaunchKind:  task.ParseLaunchKind(containers[i].Labels[task.LabelLaunchKind]),
		})
	}
	return out, nil
}

// Contract with snippets/steer-driver.js and entrypoint-base.sh.
const steerQueuePath = "/workspace/.steer/queue.jsonl"

// The driver writes `closed` under the same mkdir lock when it stops listening, so an append either
// reaches its final drain or fails here.
// That failure tells the dispatcher to resume the stage instead of marking it steered. Exit 3 =
// sealed, 4 = stuck lock.
//
//i18n:ignore shell script run in the agent container
const steerAppendScript = `d=$(dirname "$1"); mkdir -p "$d" || exit 1
i=0; until mkdir "$d/lock" 2>/dev/null; do i=$((i+1)); [ "$i" -gt 100 ] && exit 4; sleep 0.05; done
if [ -e "$d/closed" ]; then rmdir "$d/lock"; exit 3; fi
cat >> "$1"; s=$?; rmdir "$d/lock"; exit $s`

// The exec runs as root so it can write into the directory the agent's unprivileged process
// created.
func (l *taskLauncher) SteerRun(ctx context.Context, runID string, payload []byte) error {
	create, err := l.srv.docker.ContainerExecCreate(ctx, runID, container.ExecOptions{
		AttachStdin:  true,
		AttachStderr: true,
		Cmd:          []string{"sh", "-c", steerAppendScript, "steer-append", steerQueuePath},
	})
	if err != nil {
		return apperr.Wrap(err, "api.launcher.errors.steer_exec_create")
	}
	attach, err := l.srv.docker.ContainerExecAttach(ctx, create.ID, container.ExecStartOptions{})
	if err != nil {
		return apperr.Wrap(err, "api.launcher.errors.steer_exec_attach")
	}
	defer attach.Close()
	if _, err := attach.Conn.Write(payload); err != nil {
		return apperr.Wrap(err, "api.launcher.errors.steer_write")
	}
	if cw, ok := attach.Conn.(interface{ CloseWrite() error }); ok {
		logx.Warn(cw.CloseWrite(), "api.launcher.log.steer_half_close")
	}
	if _, err := io.Copy(io.Discard, attach.Reader); err != nil && !errors.Is(err, io.EOF) {
		return apperr.Wrap(err, "api.launcher.errors.steer_drain")
	}
	if inspect, err := l.srv.docker.ContainerExecInspect(ctx, create.ID); err == nil && inspect.ExitCode != 0 {
		return apperr.New("api.launcher.errors.steer_exec_exited", "code", inspect.ExitCode)
	}
	return nil
}

func (l *taskLauncher) RunLogs(runID string) string {
	reader, err := l.srv.docker.ContainerLogs(context.Background(), runID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return ""
	}
	defer reader.Close()
	return dockerx.DemuxAll(reader)
}

func (l *taskLauncher) KillRun(ctx context.Context, runID string) error {
	timeout := 10
	return l.srv.docker.ContainerStop(ctx, runID, container.StopOptions{Timeout: &timeout})
}
