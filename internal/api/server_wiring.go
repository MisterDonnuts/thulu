package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/kubert"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func (s *Server) wireDispatcher(ctx context.Context, kube *kubert.Runtime, mcpFwd *mcp.Forwarder) {
	s.mcp.fwd = mcpFwd
	s.mcp.prober = mcp.NewProber()
	s.initMCPProxy()
	var launcher task.AgentLauncher = &taskLauncher{srv: s}
	if kube != nil {
		s.kube = kube
		launcher = s.kubeAgentLauncher(ctx, kube)
	}
	s.runnerRoute, launcher = newRoutedLauncher(launcher, s.projectStore)
	// A nil *sandbox.Manager passed directly becomes a non-nil interface and cleanupDoneTask panics
	// calling Release on it.
	var releaser task.SandboxReleaser
	if s.sandbox.mgr != nil {
		releaser = s.sandbox.mgr
	}
	s.taskDispatcher = task.NewDispatcher(s.taskStore, launcher, s.projectStore, nil, releaser).
		WithBoards(s.boards)
	s.taskStore.WithBoards(s.boards)
	s.taskDispatcher.WithMCPForwarder(mcpFwd)
	s.wireBrowser(mcpFwd)
	if kube != nil {
		s.taskDispatcher.WithAgentPushesResults()
	}
	s.taskDispatcher.WithAgentBaseURL(os.Getenv("OCTOPUS_AGENT_BASE_URL"))
	s.taskDispatcher.WithSessionsDir(s.sessionsRoot())
	s.taskDispatcher.WithBranchPusher(func(ctx context.Context, t *task.Task, projID string) (bool, error) {
		pushed, _, err := s.pushTaskBranchForProject(ctx, t, projID)
		return pushed, err
	})
	s.mcp.env = collectMCPURLs(os.Environ())
	s.mcp.envForward = map[string]bool{}
	s.taskDispatcher.WithMCPURLs(s.mcp.env)
	for _, name := range forwardCredentialNames(os.Getenv("OCTOPUS_MCP_FORWARD_CREDENTIAL")) {
		s.mcp.envForward[name] = true
		s.taskDispatcher.SetMCPForwardsCredential(name, true)
	}
	s.taskDispatcher.WithChatMentions(s.answerMentionByChat).WithAgentComments(s.dispatchMentions)
	s.taskDispatcher.WithStatusHook(func(t *task.Task, status task.RunStatus) {
		if !firesTaskFailed(status) {
			return
		}
		s.publishTaskAutomationEvent(ctx, automation.EventTaskFailed, t, nil)
	})
	s.taskDispatcher.WithStageHook(func(t *task.Task, from task.Stage) {
		s.publishTaskAutomationEvent(ctx, automation.EventTaskStageChanged, t,
			map[string]any{"from": string(from), "to": string(t.Stage)})
	})
	s.taskDispatcher.WithCredentialGate(s.credentialGate)
	s.taskDispatcher.WithTokenOptimizer(s.tokenOptimizerWorker)
	if s.authStore != nil {
		s.taskDispatcher.WithTaskKeys(s.authStore)
	}
	if s.secretsStore != nil {
		s.taskDispatcher.WithLLMKeys(s.llmKeyForWorkspace)
		s.taskDispatcher.WithLLMResolver(
			s.resolveLLMForAgent,
			s.touchLLMProfile,
			s.auditLLMResolved,
		)
		s.taskDispatcher.WithLLMRefusals(s.RecordLLMProfileRefusal)
	}
	if s.workloadIdentity != nil {
		s.taskDispatcher.WithWorkloadIdentity(s.workloadIdentity)
	}
	// Must run after the dispatcher's LLM resolvers are wired: chat pods reuse them, and without
	// auth the chat entrypoint aborts.
	if s.chatSvc != nil {
		s.chatSvc.Runtime().SetLLMAuth(s.chatLLMAuth(ctx))
		if s.authStore != nil {
			s.chatSvc.Runtime().SetRunKeys(s.authStore)
		}
	}
	s.wirePersonalLogins()
	s.wireLimits(ctx)
	if s.settingsStore != nil {
		s.taskDispatcher.WithConversationCap(func() int {
			n, err := s.settingsStore.MentionMaxContainers(ctx)
			if err != nil {
				return 0
			}
			return n
		})
	}
}

func (s *Server) chatLLMAuth(ctx context.Context) func(env map[string]string, workspaceID, agentName string, at *types.AgentType) error {
	return func(env map[string]string, workspaceID, agentName string, at *types.AgentType) error {
		if err := s.taskDispatcher.ApplyLLMAuth(ctx, env, workspaceID, at, agentName); err != nil {
			return err
		}
		s.taskDispatcher.ApplyTokenOptimizer(ctx, env, workspaceID, at)
		return nil
	}
}

func (s *Server) wireMCP(kube *kubert.Runtime) {
	switch {
	case s.docker != nil && s.agentsDir != "":
		s.mcp.mgr = mcp.NewManager(s.docker, filepath.Join(s.agentsDir, "tools", "docker-compose.yml"))
	case kube != nil:
		s.mcp.mgr = mcp.NewK8sManager(kube.Clientset(), daemonNamespace())
	}

	if s.sandbox.mgr != nil {
		s.sandbox.mcp = s.newSandboxMCPHandler(s.sandbox.mgr)
	}
}

// MCP_<NAME>_URL is the form the agent entrypoint's mcp-config.sh expects. MCP_THULU_SANDBOX_URL is
// computed per task and must not be pinned.
func collectMCPURLs(environ []string) map[string]string {
	out := make(map[string]string)
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		if !strings.HasPrefix(k, "MCP_") || !strings.HasSuffix(k, "_URL") {
			continue
		}
		if k == "MCP_THULU_SANDBOX_URL" {
			continue
		}
		out[k] = v
	}
	return out
}

// Each name listed receives the run's own API key; naming a third-party server hands it a working
// credential for the workspace.
func forwardCredentialNames(raw string) []string {
	out := []string{}
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func daemonNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if ns := strings.TrimSpace(os.Getenv("OCTOPUS_NAMESPACE")); ns != "" {
		return ns
	}
	const saNamespace = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	if data, err := os.ReadFile(saNamespace); err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns
		}
	}
	return "octopus"
}

func firesTaskFailed(status task.RunStatus) bool {
	return status == task.StatusFailed
}
