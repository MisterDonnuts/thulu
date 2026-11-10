package api

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/agentnet"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/types"
)

var evidenceRunnerCommand = []string{"node", "evidence/job.js"}

const LabelEvidenceBatch = "thulu.dev/evidence-batch"

func (s *Server) NewEvidenceService(store *evidence.Store, maxRunners int) *evidence.Service {
	svc := &evidence.Service{
		Store:      store,
		Keys:       evidenceKeys{s},
		MaxRunners: maxRunners,
		DaemonURL: func() string {
			if s.taskDispatcher != nil {
				return s.taskDispatcher.AgentBaseURL()
			}
			return ""
		},
		Schedule: func(expr, tz string) (func(time.Time) time.Time, error) {
			sch, err := automation.ParseCron(expr, tz)
			if err != nil {
				return nil, err
			}
			return func(after time.Time) time.Time {
				next, err := sch.Next(after)
				if err != nil {
					return time.Time{} // zero time: never fires
				}
				return next
			}, nil
		},
	}
	switch {
	case s.kube != nil:
		svc.Launcher = kubeEvidenceLauncher{s}
	case s.docker != nil:
		svc.Launcher = dockerEvidenceLauncher{s}
	}
	if s.sandbox.mgr != nil && s.projectStore != nil {
		svc.Sandbox = evidenceSandbox{s}
	}
	return svc
}

type evidenceKeys struct{ s *Server }

func (k evidenceKeys) MintRunnerKey(ctx context.Context, ws, batchID string, ttl time.Duration) (string, string, error) {
	if k.s.authStore == nil {
		return "", "", apperr.Unavailable("api.evidence.errors.no_auth_store")
	}
	// No TaskID: a runner key must not be able to write any task.
	return k.s.authStore.MintRunKey(ctx, auth.RunScope{
		Agent: evidence.RunnerAgentPrefix + batchID,
		Stage: evidence.RunnerStage,
	}, ws, ttl)
}

func (k evidenceKeys) RevokeAPIKey(ctx context.Context, keyID string) error {
	if k.s.authStore == nil {
		return nil
	}
	return k.s.authStore.RevokeAPIKey(ctx, keyID)
}

func exitReason(code int) string {
	if code == 0 {
		return ""
	}
	return i18n.T(i18n.SourceLocale, "api.evidence.reasons.runner_exit", "code", strconv.Itoa(code))
}

type dockerEvidenceLauncher struct{ s *Server }

func (l dockerEvidenceLauncher) Launch(ctx context.Context, spec evidence.RunnerSpec, onExit func(string)) (string, error) {
	api := dockerRunBrowser{l.s.docker}
	cfg := l.s.browser
	if err := ensureRunBrowserImage(ctx, api, cfg); err != nil {
		return "", err
	}
	image := imageref.Resolve(cfg.Image)
	ccfg, hcfg := evidenceRunnerContainer(cfg, image, spec)
	network, err := agentnet.Ensure(ctx, l.s.docker)
	if err != nil {
		return "", err
	}
	if network != "" {
		hcfg.NetworkMode = container.NetworkMode(network)
	}
	hcfg.LogConfig = dockerx.ContainerLogConfig(ctx, api)
	dockerx.ApplyInit(ctx, api, image, ccfg.Entrypoint, hcfg)
	name := "octopus-evidence-" + spec.Batch.ID
	created, err := api.CreateContainer(ctx, ccfg, hcfg, name)
	if err != nil {
		return "", apperr.Wrap(err, "api.evidence.errors.runner_create", "batch", spec.Batch.ID)
	}
	if err := api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		logx.Warn(api.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true}), "api.evidence.log.stop_failed", "runner", name)
		return "", apperr.Wrap(err, "api.evidence.errors.runner_start", "batch", spec.Batch.ID)
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		waitCtx, cancel := context.WithTimeout(bg, spec.Deadline)
		defer cancel()
		code := -1
		statusCh, errCh := l.s.docker.ContainerWait(waitCtx, created.ID, container.WaitConditionNotRunning)
		select {
		case st := <-statusCh:
			code = int(st.StatusCode)
		case <-errCh:
		}
		reason := exitReason(code)
		if waitCtx.Err() != nil {
			reason = i18n.T(i18n.SourceLocale, "api.evidence.reasons.deadline")
		}
		logx.Warn(api.ContainerRemove(bg, created.ID, container.RemoveOptions{Force: true}), "api.evidence.log.stop_failed", "runner", name)
		onExit(reason)
	}()
	return name, nil
}

func (l dockerEvidenceLauncher) Stop(ctx context.Context, _, name string) {
	logx.Warn(l.s.docker.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}), "api.evidence.log.stop_failed", "runner", name)
}

func evidenceRunnerContainer(cfg browserrun.Config, image string, spec evidence.RunnerSpec) (*container.Config, *container.HostConfig) {
	list := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		list = append(list, k+"="+v)
	}
	sort.Strings(list)
	pids := int64(runBrowserPids)
	ccfg := &container.Config{
		Image: image,
		Cmd:   evidenceRunnerCommand,
		Env:   list,
		Labels: map[string]string{
			LabelEvidenceBatch:     spec.Batch.ID,
			agent.LabelWorkspaceID: spec.Batch.WorkspaceID,
		},
		User: "1000:1000",
	}
	hcfg := &container.HostConfig{
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": runBrowserTmpfs},
		ShmSize:        runBrowserShm,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges:true"}, //i18n:ignore Docker security option
		ExtraHosts:     agentnet.ExtraHosts(),
		Resources: container.Resources{
			Memory:     cfg.MemoryLimitBytes(),
			MemorySwap: cfg.MemoryLimitBytes(),
			NanoCPUs:   cfg.NanoCPUs(),
			PidsLimit:  &pids,
		},
	}
	return ccfg, hcfg
}

func (s *Server) evidenceAugment(ctx context.Context, chatID string, at *types.AgentType) *types.AgentType {
	if s.evidenceStore == nil || chatID == "" || at == nil || !s.evidenceStore.IsRecordingChat(ctx, chatID) {
		return at
	}
	stage := string(types.StageChat)
	if types.MCPWired(at.MCPByStage[stage], browserrun.ServerName) {
		return at
	}
	out := *at
	out.MCPByStage = make(map[string][]string, len(at.MCPByStage)+1)
	for k, v := range at.MCPByStage {
		out.MCPByStage[k] = append([]string(nil), v...)
	}
	out.MCPByStage[stage] = append(out.MCPByStage[stage], browserrun.ServerName)
	return &out
}

type githubDeployEvent struct {
	githubEventPayload
	DeploymentStatus struct {
		State       string `json:"state"`
		Environment string `json:"environment"`
	} `json:"deployment_status"`
	Deployment struct {
		SHA         string `json:"sha"`
		Environment string `json:"environment"`
	} `json:"deployment"`
}

func (s *Server) evidenceDeployHook(ctx context.Context, event string, payload []byte) {
	if s.evidenceSvc == nil || event != "deployment_status" {
		return
	}
	var ev githubDeployEvent
	if json.Unmarshal(payload, &ev) != nil || ev.DeploymentStatus.State != "success" {
		return
	}
	d := evidence.Deploy{Repo: ev.Repository.FullName, Environment: ev.DeploymentStatus.Environment,
		SHA: ev.Deployment.SHA}
	if d.Environment == "" {
		d.Environment = ev.Deployment.Environment
	}
	ws := s.githubEventWorkspace(ctx, ev.githubEventPayload)
	if ws == "" {
		return
	}
	d.Workspaces = []string{ws}
	s.evidenceSvc.OnDeploy(ctx, d)
}
