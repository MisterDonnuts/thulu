//go:build !foss

package chatruntime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Regression: chat pods lacked the MCP forward credential, so octopus-api/thulu-github answered 401
// and
// the engine fell back to an OAuth registration the daemon does not serve.

const chatMCPTools = "thulu-browser,thulu-github,octopus-api@write"

const chatAPIBase = "http://octopus:8585"

func perRunBrowserConfig(t *testing.T) browserrun.Config {
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

func credentialForwarder() *mcp.Forwarder {
	fwd := mcp.NewForwarder()
	fwd.SetMany(map[string]string{
		"MCP_OCTOPUS_API_URL":  "http://octopus-api:3013/sse",
		"MCP_THULU_GITHUB_URL": "http://thulu-github:3012/sse",
		browserrun.URLEnv:      "http://thulu-browser:3000/sse",
	})
	fwd.SetForwardsCredential("octopus-api", true)
	fwd.SetForwardsCredential("thulu-github", true)
	return fwd
}

func chatLaunchEnv(t *testing.T, fwd *mcp.Forwarder, browser browserrun.Config) (*Manager, map[string]string) {
	t.Helper()
	return chatLaunchEnvIn(t, "", fwd, browser)
}

func chatLaunchEnvIn(t *testing.T, locale string, fwd *mcp.Forwarder, browser browserrun.Config) (*Manager, map[string]string) {
	t.Helper()
	ctx := context.Background()
	if locale != "" {
		ctx = i18n.WithLocale(ctx, locale)
	}
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	docker := newFakeDocker()
	docker.stdout = okStream
	var (
		mu  sync.Mutex
		env map[string]string
	)
	launched := 0
	launch := func(_ context.Context, _ types.AgentType, params types.LaunchParams) (string, error) {
		launched++
		id := fmt.Sprintf("ctr-%d", launched)
		mu.Lock()
		env = params.Env
		mu.Unlock()
		docker.setRunning(id, true)
		return id, nil
	}
	m := newManagerForTest(st, sett, docker, ManagerConfig{
		DataDir: t.TempDir(), Launch: launch, MCPForwarder: fwd, Browser: browser,
		APIBaseURL: chatAPIBase,
	})
	m.SetRunKeys(&fakeMinter{})

	at := testAgent()
	at.MCPByStage = map[string][]string{string(types.StageChat): {"thulu-browser", "thulu-github", "octopus-api@write"}}
	c := &chat.Chat{ID: "chat-mcp1", WorkspaceID: "ws-1", AgentType: at.Name}
	if _, err := m.ExecTurn(ctx, c, at, AcquireOpts{}, "hola"); err != nil {
		t.Fatalf("ExecTurn: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return m, env
}

func TestChatForwardsTheRunKeyToTheSameServersAsARun(t *testing.T) {
	fwd := credentialForwarder()
	_, env := chatLaunchEnv(t, fwd, browserrun.Config{})

	run := map[string]string{"MCP_TOOLS": chatMCPTools, "OCTOPUS_API": chatAPIBase}
	mcp.ApplyLaunchEnv(context.Background(), run, fwd, browserrun.Config{})

	if got := env["MCP_TOOLS"]; got != chatMCPTools {
		t.Fatalf("MCP_TOOLS = %q, want %q", got, chatMCPTools)
	}
	if got, want := env[mcp.ServersEnv], run[mcp.ServersEnv]; got != want || got == "" {
		t.Errorf("chat %s = %s\nrun %s = %s\nwant both the same, non-empty", mcp.ServersEnv, got, mcp.ServersEnv, want)
	}
	if env["OCTOPUS_API_KEY"] == "" {
		t.Error("chat launched with no OCTOPUS_API_KEY to present to the proxy")
	}
	if got := env["MCP_OCTOPUS_API_URL"]; got != chatAPIBase+"/mcp-proxy/octopus-api" {
		t.Errorf("MCP_OCTOPUS_API_URL = %q, want the proxy", got)
	}
}

func TestDockerChatGetsNoBrowserInPerRunMode(t *testing.T) {
	fwd := credentialForwarder()
	fwd.Withhold(browserrun.URLEnv)
	m, env := chatLaunchEnv(t, fwd, perRunBrowserConfig(t))

	if !m.noPerRunBrowser {
		t.Error("a Docker manager in per-run mode does not know its chats go without a browser")
	}
	if v, ok := env[browserrun.URLEnv]; ok {
		t.Errorf("Docker chat got %s=%q", browserrun.URLEnv, v)
	}
	if browserrun.Wanted(env) {
		t.Error("Docker chat asks for a per-run browser nothing will start")
	}
	if strings.Contains(env[mcp.ServersEnv], `"thulu-browser"`) {
		t.Errorf("Docker chat lists a browser: %s", env[mcp.ServersEnv])
	}
}

func TestKubeChatTakesThePerRunBrowserPolicy(t *testing.T) {
	rt, err := kuberuntime.NewWithClient(fake.NewClientset(), kuberuntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, nil, nil, ManagerConfig{Kube: rt, Browser: perRunBrowserConfig(t)})
	if !m.browser.PerRun() || m.noPerRunBrowser {
		t.Errorf("kube manager browser = %+v, noPerRunBrowser = %v; want per-run and false", m.browser, m.noPerRunBrowser)
	}

	env := map[string]string{"MCP_TOOLS": chatMCPTools, "OCTOPUS_API_KEY": "oct_chat", "OCTOPUS_API": chatAPIBase}
	mcp.ApplyLaunchEnv(context.Background(), env, credentialForwarder(), m.browser)
	if !browserrun.Startable(env) {
		t.Error("a kube chat granted thulu-browser would launch without its sidecar")
	}
	if !strings.Contains(env[mcp.ServersEnv], `"url":"http://127.0.0.1:3010/sse`) {
		t.Errorf("%s = %s, want the sidecar browser", mcp.ServersEnv, env[mcp.ServersEnv])
	}
}

func TestChatSpeaksTheLanguageOfTheRequest(t *testing.T) {
	prev := i18n.DaemonLocale()
	i18n.SetDaemonLocale("en")
	t.Cleanup(func() { i18n.SetDaemonLocale(prev) })

	if _, env := chatLaunchEnvIn(t, "es", credentialForwarder(), browserrun.Config{}); env["OCTOPUS_LOCALE"] != "es" {
		t.Errorf("chat asked for in es: OCTOPUS_LOCALE = %q, want es", env["OCTOPUS_LOCALE"])
	}
	if _, env := chatLaunchEnvIn(t, "", credentialForwarder(), browserrun.Config{}); env["OCTOPUS_LOCALE"] != "en" {
		t.Errorf("chat nobody asked for: OCTOPUS_LOCALE = %q, want the daemon's en", env["OCTOPUS_LOCALE"])
	}
}
