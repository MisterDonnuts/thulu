//go:build !foss

package api

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type kubeLauncher struct {
	rt         *kuberuntime.Runtime
	runsDir    string
	agents     types.WorkspaceAgents
	gitBaseURL string
	baseCtx    context.Context

	logSink func(ctx context.Context, a *agent.Agent, fullLogs string)

	runDeadline func(workspaceID string) time.Duration

	mu    sync.Mutex
	byPod map[string]podRec
}

type podRec struct {
	namespace string
	outputDir string
	agentName string
	agentType string
	engine    string
	taskID    string
	projectID string
	stage     string
	startedAt time.Time
	watched   bool
}

var _ task.AgentLauncher = (*kubeLauncher)(nil)

func (s *Server) kubeAgentLauncher(ctx context.Context, kube *kuberuntime.Runtime) task.AgentLauncher {
	kl := newKubeLauncher(ctx, kube, s.runsDir,
		os.Getenv("OCTOPUS_GIT_BASE_URL"), serverAgents{s})
	kl.runDeadline = s.runDeadline
	if s.usageStore != nil {
		collector := usage.NewCollector(nil, s.usageStore, s.usageStore.DataDir())
		kl.WithLogSink(collector.RecordRun)
	}
	return kl
}

func newKubeLauncher(baseCtx context.Context, rt *kuberuntime.Runtime,
	runsDir, gitBaseURL string, agents types.WorkspaceAgents,
) *kubeLauncher {
	return &kubeLauncher{
		rt:         rt,
		runsDir:    runsDir,
		agents:     agents,
		gitBaseURL: strings.TrimRight(gitBaseURL, "/"),
		baseCtx:    baseCtx,
		byPod:      map[string]podRec{},
	}
}

// The clone URL carries no key: an embedded key would land in the pod spec and in .git/config on
// the worktree PVC.
// The git credential helper reads OCTOPUS_API_KEY instead, so a relaunch pushes with its own live
// key.
func (l *kubeLauncher) worktreeMounts(t *task.Task, worktreePaths map[string]string, readOnly bool, taskKey string) []kuberuntime.WorktreeMount {
	if l.gitBaseURL == "" || len(worktreePaths) == 0 {
		return nil
	}
	credEnv := ""
	if taskKey != "" {
		credEnv = gitAuthEnvVar
	}
	out := make([]kuberuntime.WorktreeMount, 0, len(worktreePaths))
	for name, path := range worktreePaths {
		out = append(out, kuberuntime.WorktreeMount{
			Name:          name,
			CloneURL:      fmt.Sprintf("%s/%s/%s", l.gitBaseURL, t.ID, name),
			Branch:        t.BranchAt(path),
			ReadOnly:      readOnly,
			CredentialEnv: credEnv,
		})
	}
	return out
}

// Must match the env var the pod's git credential helper reads; the dispatcher injects the run's
// task-scoped key there.
const gitAuthEnvVar = "OCTOPUS_API_KEY"

// Resolve through the agent source, never the agents dir: in Kubernetes that dir is an emptyDir and
// every agent would be "not found".
func (l *kubeLauncher) FindAgentType(workspaceID, name string) *types.AgentType {
	if l.agents == nil {
		return nil
	}
	return types.FindAgent(l.agents.For(workspaceID), name)
}

func (l *kubeLauncher) LaunchForStage(
	ctx context.Context,
	t *task.Task,
	stage task.Stage,
	agentType *types.AgentType,
	env map[string]string,
	worktreePaths map[string]string,
	_ map[string]string,
	workspaceReadOnly bool,
) (containerID, instanceID, outputDir string, err error) {
	if env == nil {
		env = map[string]string{}
	}
	env["STAGE"] = string(stage)
	// A personal engine login lives in a Docker volume that pods can't mount; launching anyway
	// would silently run with no credential.
	if env[task.EnvPersonalLoginVolume] != "" {
		return "", "", "", apperr.Precondition("api.engine_logins.errors.unavailable")
	}
	instanceID = env["INSTANCE_ID"]
	if instanceID == "" {
		return "", "", "", apperr.Invalid("api.launcher.errors.missing_instance_id")
	}

	// Pods cannot build images, so this must be a registry-pullable ref; empty falls back to the
	// runtime's image for the run's engine (internal/agentimage).
	image := strings.TrimSpace(agentType.Image)
	if image == "" && agentType.Dir != "" {
		if spec, _, perr := types.ParseComposeSpec(agentType.Dir); perr == nil {
			image = spec.Image
		}
	}

	if err = l.rt.EnsureTenantNamespace(ctx, t.WorkspaceID); err != nil {
		return "", "", "", apperr.Wrap(err, "api.launcher.errors.ensure_namespace")
	}

	// The worktree PVC (the RWO claim shared with the sandbox pod) exists only for tasks with
	// repos.
	// Referencing it otherwise leaves the pod unschedulable and shadows the image's baked
	// /workspace entrypoint.
	var worktreePVC string
	if len(worktreePaths) > 0 {
		worktreePVC = sandbox.WorktreePVCName(t.SandboxRootID())
	}

	podName, err := l.rt.LaunchPod(ctx, kuberuntime.LaunchSpec{
		WorkspaceID: t.WorkspaceID,
		TaskID:      t.ID,
		Stage:       string(stage),
		InstanceID:  instanceID,
		Image:       image,
		Engine:      env["ENGINE"],
		Env:         env,
		Annotations: map[string]string{
			kuberuntime.AnnotationInstanceID: instanceID,
			kuberuntime.AnnotationAgentType:  agentType.Name,
			kuberuntime.AnnotationLaunchKind: env[task.EnvLaunchKind],
		},
		WorktreePVC: worktreePVC,
		// The claim belongs to the sandbox root; the release and the sweep key on this label.
		WorktreeOwnerID:       t.SandboxRootID(),
		Worktrees:             l.worktreeMounts(t, worktreePaths, workspaceReadOnly, env["OCTOPUS_API_KEY"]),
		ActiveDeadlineSeconds: l.podDeadline(t.WorkspaceID),
		Browser:               browserrun.Startable(env),
	})
	if kuberuntime.IsQuotaFull(err) {
		return "", "", "", task.NoCapacity(err)
	}
	if err != nil {
		return "", "", "", err
	}

	// Stays empty: a pod's /workspace/runs is not a volume. Outputs arrive through the API
	// (entrypoint-base.sh posts them) and the session id through RunLogs.
	outputDir = runOutputDir(l.runsDir, instanceID)
	// Same rule as the Docker path's PROJECT_ID (t.ProjectIDs[0]), so /usage/by-project never mixes
	// in workspace ids.
	projectID := ""
	if len(t.ProjectIDs) > 0 {
		projectID = t.ProjectIDs[0]
	}
	l.mu.Lock()
	l.byPod[podName] = podRec{
		namespace: l.rt.NamespaceFor(t.WorkspaceID),
		outputDir: outputDir,
		agentName: agentType.Name,
		agentType: agentType.Type,
		engine:    agentType.Engine,
		taskID:    t.ID,
		projectID: projectID,
		stage:     string(stage),
		startedAt: time.Now(),
	}
	l.mu.Unlock()
	return podName, instanceID, outputDir, nil
}

func (l *kubeLauncher) WithLogSink(sink func(ctx context.Context, a *agent.Agent, fullLogs string)) *kubeLauncher {
	l.logSink = sink
	return l
}

// The pod is deleted only after cb returns, so the run stays discoverable by ListExited until its
// side effects are recorded.
func (l *kubeLauncher) OnExit(_ context.Context, containerID string, cb func(exitCode int, outputDir, worktreePath string)) {
	rec, ok := l.lookup(containerID)
	if !ok {
		return
	}
	l.markWatched(containerID)
	//nolint:contextcheck // deliberate: the watch must outlive the launch that
	// registered it, so it is scoped to the daemon's context, not the caller's.
	l.rt.WatchExit(l.baseCtx, containerID, rec.namespace, func(code int) {
		cb(code, rec.outputDir, "")
		// Capture logs before DeletePod: Kubernetes discards them with the pod.
		l.captureLogs(l.baseCtx, containerID, rec)
		// Background, not baseCtx: the pod must be reaped even while the daemon shuts down. A
		// failed delete is swept later by kuberuntime.SweepExitedPods.
		if err := l.rt.DeletePod(context.Background(), containerID, rec.namespace); err != nil {
			log.Printf("[kubelauncher] %s", i18n.Log("api.launcher.log.delete_pod_failed",
				"namespace", rec.namespace, "pod", containerID, "err", err))
		}
		l.forget(containerID)
	})
}

func (l *kubeLauncher) captureLogs(ctx context.Context, containerID string, rec podRec) {
	if l.logSink == nil {
		return
	}
	reader, err := l.rt.PodLogs(ctx, containerID, rec.namespace, false)
	if err != nil {
		log.Printf("[kubelauncher] %s", i18n.Log("api.launcher.log.read_logs_failed", "namespace", rec.namespace, "pod", containerID, "err", err))
		return
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("[kubelauncher] %s", i18n.Log("api.launcher.log.drain_logs_failed", "namespace", rec.namespace, "pod", containerID, "err", err))
		return
	}
	l.logSink(ctx, &agent.Agent{
		ID:        containerID, // The pod name is the id the log viewer looks up.
		Name:      rec.agentName,
		Type:      rec.agentType,
		Engine:    rec.engine,
		TaskID:    rec.taskID,
		ProjectID: rec.projectID,
		Stage:     rec.stage,
		StartedAt: rec.startedAt,
		OutputDir: rec.outputDir,
	}, string(data))
}

func (l *kubeLauncher) ContainerAlive(containerID string) bool {
	rec, ok := l.lookup(containerID)
	if !ok {
		return false
	}
	return l.rt.PodAlive(context.Background(), containerID, rec.namespace)
}

// Pods with an armed exit watch are skipped: their OnExit records the exit, and the janitor re-runs
// Reconcile every sweep, which would replay post-run side effects twice.
func (l *kubeLauncher) ScanExitedTaskAgents(ctx context.Context) ([]task.ExitedTaskAgent, error) {
	exited, err := l.rt.ListExitedAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]task.ExitedTaskAgent, 0, len(exited))
	for _, e := range exited {
		if rec, ok := l.lookup(e.Name); ok && rec.watched {
			continue
		}
		outputDir := runOutputDir(l.runsDir, e.InstanceID)
		out = append(out, task.ExitedTaskAgent{
			ContainerID: e.Name,
			InstanceID:  e.InstanceID,
			AgentType:   e.AgentType,
			OutputDir:   outputDir,
			ExitCode:    e.ExitCode,
			LaunchKind:  task.ParseLaunchKind(e.LaunchKind),
		})
	}
	return out, nil
}

func (l *kubeLauncher) lookup(podName string) (podRec, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.byPod[podName]
	return rec, ok
}

func (l *kubeLauncher) markWatched(podName string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec, ok := l.byPod[podName]; ok {
		rec.watched = true
		l.byPod[podName] = rec
	}
}

func (l *kubeLauncher) forget(podName string) {
	l.mu.Lock()
	delete(l.byPod, podName)
	l.mu.Unlock()
}

// No SteerRun on purpose: it would need pods/exec, which the daemon's ClusterRole withholds (see
// internal/sandbox/execagent).
func (l *kubeLauncher) RunLogs(runID string) string {
	rec, ok := l.lookup(runID)
	if !ok {
		return ""
	}
	reader, err := l.rt.PodLogs(context.Background(), runID, rec.namespace, false)
	if err != nil {
		return ""
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return ""
	}
	return string(data)
}

func (l *kubeLauncher) podDeadline(workspaceID string) int64 {
	if l.runDeadline == nil {
		return 0
	}
	return podDeadlineSeconds(l.runDeadline(workspaceID))
}

func (l *kubeLauncher) KillRun(ctx context.Context, runID string) error {
	rec, ok := l.lookup(runID)
	if !ok {
		return apperr.NotFound("api.launcher.errors.run_not_tracked", "run", runID)
	}
	return l.rt.DeletePod(ctx, runID, rec.namespace)
}
