package task

import (
	"context"
	"log"
	"maps"
	"net/url"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type llmRouting struct {
	gatewayOn  bool
	gatewayURL string
	// Off by default: on a shared daemon it bills one tenant's work to the operator's key, a
	// credential the workspace admin never chose.
	// OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL=1 turns it on.
	hostAllowed bool
}

func (d *Dispatcher) WithLLMGateway(agentURL string) *Dispatcher {
	d.llm.gatewayOn = true
	d.llm.gatewayURL = strings.TrimRight(strings.TrimSpace(agentURL), "/")
	return d
}

func (d *Dispatcher) WithHostLLMCredential(allowed bool) *Dispatcher {
	d.llm.hostAllowed = allowed
	return d
}

func (d *Dispatcher) HostLLMCredentialAllowed() bool {
	return d.llm.hostAllowed || (d.llmResolver == nil && d.llmKeys == nil)
}

func (d *Dispatcher) LLMGatewayURL() string {
	if !d.llm.gatewayOn {
		return ""
	}
	if d.llm.gatewayURL != "" {
		return d.llm.gatewayURL
	}
	return strings.TrimRight(d.agentBaseURL, "/") + llmgateway.MountPath
}

// With no credential store wired (tests, bare dev setups) the host credential is the only one and
// stays allowed.
// An engine with no provider authenticates however its image does.
func (d *Dispatcher) hostCredential(engineName, workspaceID, agentName string) error {
	if d.HostLLMCredentialAllowed() {
		return nil
	}
	eng, err := engine.Get(engineName)
	if err != nil || eng.Capabilities().Provider == "" {
		return nil
	}
	// The returned error must not name OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL: task readers cannot set
	// it, so the operator hint goes to the log.
	log.Printf("[task] %s", i18n.Log("task.log.host_llm_credential_off",
		"agent", agentName, "engine", engineName, "workspace", workspaceID))
	if workspaceID == "" {
		return apperr.Precondition("task.errors.no_llm_credential_no_workspace", "agent", agentName, "engine", engineName)
	}
	return apperr.Precondition("task.errors.no_llm_credential",
		"agent", agentName, "workspace", workspaceID, "engine", engineName)
}

// Every other credential var is cleared so the container holds nothing a prompt injection could use
// outside this run.
// A task run without a run key would get a 401 on every model call; a chat turn keeps the key its
// container was created with.
func (d *Dispatcher) applyGatewayEnv(env map[string]string, caps engine.Capabilities, provider string, taskRun bool) error {
	token := env["OCTOPUS_API_KEY"]
	if taskRun && (d.keys == nil || token == "") {
		return apperr.Precondition("task.errors.gateway_no_run_key")
	}
	clearProviderAuthEnv(env, caps)
	provider = caps.ProfileProvider(provider)
	env[caps.BaseURLEnv] = d.LLMGatewayURL() + "/" + provider
	if caps.ProviderEnv != "" {
		env[caps.ProviderEnv] = provider
	}
	if api, ok := caps.APIKeyKind(); ok && token != "" {
		for _, v := range api.EnvVars {
			env[v] = token
		}
	}
	env[engine.LLMGatewayEnv] = "1"
	return nil
}

// Must match where the agent runtime mounts OCTOPUS_EXTRA_CA_BUNDLE.
const agentCAPath = "/etc/octopus/ca.crt"

func (d *Dispatcher) applyAgentEgressEnv(env map[string]string) {
	for k, v := range agentEgressEnv(os.Getenv, env["ENGINE"], d.agentBaseURL, d.LLMGatewayURL()) {
		env[k] = v
	}
}

// Proxy vars are set in both cases because tools disagree on which they read.
// NO_PROXY always names the daemon, gateway, loopback and cluster hosts: proxying them breaks the
// agent's API calls or hands the proxy the run token.
func agentEgressEnv(getenv func(string) string, engineName string, daemonURLs ...string) map[string]string {
	out := map[string]string{}

	if getenv("OCTOPUS_CLAUDE_CODE_NONESSENTIAL_TRAFFIC") != "1" {
		out["OCTOPUS_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
		if eng, err := engine.Get(engineName); err == nil {
			for k, v := range eng.Capabilities().NonessentialTrafficEnv {
				out[k] = v
			}
		}
	}

	inherit := getenv("OCTOPUS_AGENT_PROXY_INHERIT") != "0"
	pick := func(explicit, upper, lower string) string {
		if v := strings.TrimSpace(getenv(explicit)); v != "" {
			return v
		}
		if !inherit {
			return ""
		}
		if v := strings.TrimSpace(getenv(upper)); v != "" {
			return v
		}
		return strings.TrimSpace(getenv(lower))
	}
	httpProxy := pick("OCTOPUS_AGENT_HTTP_PROXY", "HTTP_PROXY", "http_proxy")
	httpsProxy := pick("OCTOPUS_AGENT_HTTPS_PROXY", "HTTPS_PROXY", "https_proxy")
	if httpProxy != "" {
		out["HTTP_PROXY"], out["http_proxy"] = httpProxy, httpProxy
	}
	if httpsProxy != "" {
		out["HTTPS_PROXY"], out["https_proxy"] = httpsProxy, httpsProxy
	}
	if httpProxy != "" || httpsProxy != "" {
		noProxy := noProxyList(pick("OCTOPUS_AGENT_NO_PROXY", "NO_PROXY", "no_proxy"), daemonURLs)
		out["NO_PROXY"], out["no_proxy"] = noProxy, noProxy
	}

	if strings.TrimSpace(getenv("OCTOPUS_EXTRA_CA_BUNDLE")) != "" {
		out["NODE_EXTRA_CA_CERTS"] = agentCAPath
	}
	maps.Copy(out, githubAgentEnv(getenv))
	return out
}

func githubAgentEnv(getenv func(string) string) map[string]string {
	if strings.TrimSpace(getenv(githubcfg.EnvWebURL)) == "" && strings.TrimSpace(getenv(githubcfg.EnvAPIURL)) == "" {
		return nil
	}
	return githubcfg.EnvFrom(getenv)
}

func noProxyList(configured string, daemonURLs []string) string {
	var list []string
	seen := map[string]bool{}
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || seen[strings.ToLower(h)] {
			return
		}
		seen[strings.ToLower(h)] = true
		list = append(list, h)
	}
	for _, h := range strings.Split(configured, ",") {
		add(h)
	}
	for _, raw := range daemonURLs {
		if u, err := url.Parse(raw); err == nil {
			add(u.Hostname())
		}
	}
	for _, h := range []string{"localhost", "127.0.0.1", "::1", "host.docker.internal", ".svc", ".cluster.local"} {
		add(h)
	}
	return strings.Join(list, ",")
}

func (d *Dispatcher) LLMReady(ctx context.Context, workspaceID string, agentType *types.AgentType, agentName string) error {
	fallback := resolveEngineName(agentType)
	if d.llmResolver == nil || workspaceID == "" {
		if d.llmKeys != nil && workspaceID != "" {
			if key, ok := d.llmKeys(ctx, workspaceID, fallback); ok && key != "" {
				return nil
			}
		}
		return d.hostCredential(fallback, workspaceID, agentName)
	}
	resolved, err := d.llmResolver(ctx, workspaceID, agentName, fallback)
	if err != nil {
		return apperr.Wrap(err, "task.errors.resolve_llm_profile", "agent", agentName)
	}
	if resolved.Source != "host" {
		return nil
	}
	engineName := strings.TrimSpace(resolved.Engine)
	if engineName == "" {
		engineName = fallback
	}
	return d.hostCredential(engineName, workspaceID, agentName)
}
