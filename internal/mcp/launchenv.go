package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// Read only by agent images built before OCTOPUS_MCP_SERVERS. Remove once no supported image
// predates it.
const ForwardKeyEnv = "MCP_FORWARD_KEY"

// Not proxied: the daemon serves it and checks the run key itself.
const sandboxServer = "thulu-sandbox"

// Callers must set MCP_TOOLS, OCTOPUS_API, AGENT_NAME, STAGE, TASK_ID or CHAT_ID and OCTOPUS_LOCALE
// first.
// Except OCTOPUS_MCP_SERVERS, existing keys in env are never overwritten.
func ApplyLaunchEnv(ctx context.Context, env map[string]string, f *Forwarder, browser browserrun.Config) {
	// Must run first so LaunchServers lists the loopback URL.
	browser.PrepareAgentEnv(env)
	servers := LaunchServers(env, f)
	// deploy/.env uses the same name for the init wizard's comma list, which an agent's compose env
	// could interpolate.
	delete(env, ServersEnv)
	if len(servers) > 0 {
		if b, err := json.Marshal(servers); err == nil {
			env[ServersEnv] = string(b)
		}
	}
	applyLegacyEnv(env, servers)
	if f != nil {
		recordGrant(ctx, f.grantRecorder(), env)
	}
}

func LaunchServers(env map[string]string, f *Forwarder) []ServerEntry {
	apiBase := strings.TrimSpace(env[browserrun.DaemonURLEnv])
	taskID, locale := env["TASK_ID"], env["OCTOPUS_LOCALE"]
	var out []ServerEntry
	for _, e := range ParseToolList(env["MCP_TOOLS"]) {
		switch {
		case e.Server == sandboxServer:
			if u := env[NameToEnvKey(sandboxServer)]; u != "" {
				out = append(out, ServerEntry{Name: e.Server, URL: WithQuery(u, RunQuery(taskID, locale, e)),
					Transport: TransportSSE, RunKey: true})
			}
		case e.Server == browserrun.ServerName && browserrun.Wanted(env):
			out = append(out, ServerEntry{Name: e.Server, URL: WithQuery(env[browserrun.URLEnv], RunQuery(taskID, locale, e)),
				Transport: TransportSSE, RunKey: true})
		default:
			if f == nil || apiBase == "" {
				continue
			}
			up, ok := f.Lookup(e.Server)
			if !ok {
				continue
			}
			out = append(out, ServerEntry{Name: e.Server, URL: ProxyURL(apiBase, e.Server),
				Transport: up.Transport, RunKey: true})
		}
	}
	return out
}

// Images built before OCTOPUS_MCP_SERVERS read MCP_<NAME>_URL and write every entry as SSE, so only
// SSE servers go here.
func applyLegacyEnv(env map[string]string, servers []ServerEntry) {
	var forward []string
	for _, s := range servers {
		if s.Transport != TransportSSE {
			continue
		}
		key := NameToEnvKey(s.Name)
		if _, ok := env[key]; !ok {
			env[key] = s.URL
		}
		if s.Name == browserrun.ServerName && browserrun.Wanted(env) {
			forward = append(forward, s.Name)
		}
	}
	if len(forward) > 0 {
		env[ForwardKeyEnv] = strings.Join(forward, ",")
	}
}

func recordGrant(ctx context.Context, r GrantRecorder, env map[string]string) {
	if r == nil {
		return
	}
	ref := env["CHAT_ID"]
	if ref == "" {
		ref = env["TASK_ID"]
	}
	agent, stage := env["AGENT_NAME"], env["STAGE"]
	if ref == "" || agent == "" || stage == "" {
		return
	}
	err := r.RecordRunGrant(ctx, ref, agent, stage, RunGrant{Tools: env["MCP_TOOLS"], Locale: env["OCTOPUS_LOCALE"],
		OnRunner: env[runner.HostRunEnv] == "1"})
	logx.Warn(err, "mcp.log.record_run_grant_failed", "run", ref)
}
