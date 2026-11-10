package task

import (
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

func TestDispatcher_DelegatesToSharedForwarder(t *testing.T) {
	fwd := mcp.NewForwarder()
	d := (&Dispatcher{mcpFwd: mcp.NewForwarder()}).WithMCPForwarder(fwd)

	d.WithMCPURLs(map[string]string{"MCP_THULU_GITHUB_URL": "http://gh/sse"})
	d.SetMCPURL("MCP_TICKET_READER_URL", "http://ticket-reader/sse")
	if got := fwd.Snapshot()["MCP_THULU_GITHUB_URL"]; got != "http://gh/sse" {
		t.Errorf("WithMCPURLs not reflected in shared table: %q", got)
	}
	if got := fwd.Snapshot()["MCP_TICKET_READER_URL"]; got != "http://ticket-reader/sse" {
		t.Errorf("SetMCPURL not reflected in shared table: %q", got)
	}

	fwd.Set("MCP_THULU_BROWSER_URL", "http://browser/sse")
	if got := d.MCPURLs()["MCP_THULU_BROWSER_URL"]; got != "http://browser/sse" {
		t.Errorf("shared registration not visible via dispatcher: %q", got)
	}

	d.DeleteMCPURL("MCP_TICKET_READER_URL")
	if _, ok := fwd.Snapshot()["MCP_TICKET_READER_URL"]; ok {
		t.Error("DeleteMCPURL did not reach the shared table")
	}
}

func TestDispatcher_ApplyMCPURLsNeverOverwrites(t *testing.T) {
	d := &Dispatcher{mcpFwd: mcp.NewForwarder()}
	d.WithMCPURLs(map[string]string{
		"MCP_THULU_GITHUB_URL":  "http://gh/sse",
		"MCP_THULU_SANDBOX_URL": "http://static-should-lose",
	})
	sandbox := "http://octopus.svc:8585/sandbox/sse?task_id=abc&stage=in_progress"
	env := map[string]string{
		"MCP_TOOLS":             "thulu-github,thulu-sandbox",
		"OCTOPUS_API":           "http://octopus.svc:8585",
		"MCP_THULU_SANDBOX_URL": sandbox,
	}
	d.applyMCPURLs(context.Background(), env, false)
	if got := env["MCP_THULU_SANDBOX_URL"]; got != sandbox {
		t.Errorf("sandbox URL was overwritten: %q", got)
	}
	if !strings.Contains(env[mcp.ServersEnv], `"url":"http://octopus.svc:8585/mcp-proxy/thulu-github"`) {
		t.Errorf("%s = %s, want thulu-github through the proxy", mcp.ServersEnv, env[mcp.ServersEnv])
	}
}

func TestDispatcher_ProxyEntriesCarryNoTrustDecision(t *testing.T) {
	d := &Dispatcher{mcpFwd: mcp.NewForwarder()}
	d.WithMCPURLs(map[string]string{
		"MCP_OCTOPUS_API_URL":   "http://octopus-api/sse",
		"MCP_TICKET_READER_URL": "http://ticket-reader/sse",
	})
	d.SetMCPForwardsCredential("octopus-api", true)
	env := map[string]string{"MCP_TOOLS": "octopus-api,ticket-reader", "OCTOPUS_API": "http://d:8585"}
	d.applyMCPURLs(context.Background(), env, false)
	if _, present := env[mcp.ForwardKeyEnv]; present {
		t.Errorf("%s = %q, want it absent without a per-run browser", mcp.ForwardKeyEnv, env[mcp.ForwardKeyEnv])
	}
	want := `[{"name":"octopus-api","url":"http://d:8585/mcp-proxy/octopus-api","transport":"sse","run_key":true},` +
		`{"name":"ticket-reader","url":"http://d:8585/mcp-proxy/ticket-reader","transport":"sse","run_key":true}]`
	if got := env[mcp.ServersEnv]; got != want {
		t.Errorf("%s =\n%s\nwant\n%s", mcp.ServersEnv, got, want)
	}
}

func TestDispatcher_PerRunBrowserReplacesTheSharedURL(t *testing.T) {
	fwd := mcp.NewForwarder()
	fwd.Set(browserrun.URLEnv, "http://thulu-browser:3000/sse")
	fwd.Withhold(browserrun.URLEnv)
	d := (&Dispatcher{}).WithMCPForwarder(fwd).WithBrowser(perRunBrowser(t))

	env := map[string]string{"MCP_TOOLS": "thulu-browser@browser_navigate+browser_screenshot,octopus-api"}
	d.applyMCPURLs(context.Background(), env, false)
	if got := env[browserrun.URLEnv]; got != "http://127.0.0.1:3010/sse" {
		t.Errorf("%s = %q, want the loopback URL", browserrun.URLEnv, got)
	}
	if !browserrun.Wanted(env) {
		t.Error("the launch env does not ask the launcher for a browser")
	}
	if !strings.Contains(env[mcp.ServersEnv], `"url":"http://127.0.0.1:3010/sse?tools=`) {
		t.Errorf("%s = %s, want the loopback browser", mcp.ServersEnv, env[mcp.ServersEnv])
	}

	env = map[string]string{"MCP_TOOLS": "octopus-api"}
	d.applyMCPURLs(context.Background(), env, false)
	if v, ok := env[browserrun.URLEnv]; ok {
		t.Errorf("an ungranted run got %s=%q", browserrun.URLEnv, v)
	}
	if browserrun.Wanted(env) {
		t.Error("an ungranted run asks for a browser")
	}
}

func TestDispatcher_SharedBrowserGoesThroughTheProxy(t *testing.T) {
	d := &Dispatcher{mcpFwd: mcp.NewForwarder()}
	d.WithMCPURLs(map[string]string{browserrun.URLEnv: "http://thulu-browser:3000/sse"})
	env := map[string]string{"MCP_TOOLS": "thulu-browser", "OCTOPUS_API": "http://d:8585"}
	d.applyMCPURLs(context.Background(), env, false)
	if got := env[browserrun.URLEnv]; got != "http://d:8585/mcp-proxy/thulu-browser" {
		t.Errorf("%s = %q, want the proxy URL", browserrun.URLEnv, got)
	}
	if browserrun.Wanted(env) {
		t.Error("shared mode asks the launcher for a per-run browser")
	}
}

func perRunBrowser(t *testing.T) browserrun.Config {
	t.Helper()
	cfg, err := browserrun.ConfigFromEnv(func(k string) string {
		if k == browserrun.EnvMode {
			return string(browserrun.ModePerRun)
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestDispatcher_RunnerRunUsesTheSharedBrowser(t *testing.T) {
	d := &Dispatcher{mcpFwd: mcp.NewForwarder()}
	d.WithMCPURLs(map[string]string{browserrun.URLEnv: "http://thulu-browser:3000/sse"})
	env := map[string]string{"MCP_TOOLS": "thulu-browser", "OCTOPUS_API": "http://d:8585"}
	d.applyMCPURLs(context.Background(), env, true)
	if env[runner.HostRunEnv] != "1" {
		t.Errorf("%s = %q, want the run marked as a runner's", runner.HostRunEnv, env[runner.HostRunEnv])
	}
	if browserrun.Wanted(env) {
		t.Error("a runner run asks for a per-run browser, which nothing would start")
	}
	if !strings.Contains(env[mcp.ServersEnv], "http://d:8585/mcp-proxy/thulu-browser") {
		t.Errorf("%s = %s, want the shared browser through the proxy", mcp.ServersEnv, env[mcp.ServersEnv])
	}
}

func TestDispatcher_RunnerRunGetsNoBrowserInPerRunMode(t *testing.T) {
	d := &Dispatcher{mcpFwd: mcp.NewForwarder()}
	d.WithBrowser(perRunBrowser(t))
	d.WithMCPURLs(map[string]string{browserrun.URLEnv: "http://thulu-browser:3000/sse"})
	env := map[string]string{"MCP_TOOLS": "octopus-api@write,thulu-browser@browser_navigate", "OCTOPUS_API": "http://d:8585"}
	d.applyMCPURLs(context.Background(), env, true)
	if env["MCP_TOOLS"] != "octopus-api@write" {
		t.Errorf("MCP_TOOLS = %q, want thulu-browser dropped", env["MCP_TOOLS"])
	}
	if strings.Contains(env[mcp.ServersEnv], "thulu-browser") || browserrun.Wanted(env) {
		t.Errorf("%s = %s, want no browser at all", mcp.ServersEnv, env[mcp.ServersEnv])
	}
}
