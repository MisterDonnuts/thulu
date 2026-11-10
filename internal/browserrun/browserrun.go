// thulu-browser's browser_run_script runs caller JS inside the server's Node process, so on a
// shared server
// one run can patch fetch and read other runs' keys. Multi-tenant installs must use per-run.
package browserrun

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

type Mode string

const (
	ModeShared Mode = "shared"
	ModePerRun Mode = "per-run"
)

const (
	EnvMode          = "OCTOPUS_BROWSER_MODE"
	EnvImage         = "OCTOPUS_BROWSER_IMAGE"
	EnvPort          = "OCTOPUS_BROWSER_PORT"
	EnvCPURequest    = "OCTOPUS_BROWSER_CPU_REQUEST"
	EnvCPULimit      = "OCTOPUS_BROWSER_CPU_LIMIT"
	EnvMemoryRequest = "OCTOPUS_BROWSER_MEMORY_REQUEST"
	EnvMemoryLimit   = "OCTOPUS_BROWSER_MEMORY_LIMIT"

	envSaaSMode     = "OCTOPUS_SAAS_MODE"
	envPinnedImage  = "OCTOPUS_MCP_BROWSER_IMAGE"
	envRegistry     = "OCTOPUS_IMAGE_REGISTRY"
	envVersion      = "OCTOPUS_VERSION"
	defaultRegistry = "ghcr.io/misterdonnuts"
	// Must match the repository the release workflow pushes and dockergc.KeepFromEnv keeps.
	publishedImage = "octopus-mcp-browser"
)

// Sized for one Chromium driven by one agent; the limit leaves headroom for heavy pages without
// taking the node.
const (
	DefaultPort          = 3010
	defaultCPURequest    = "500m"
	defaultCPULimit      = "1"
	defaultMemoryRequest = "1Gi"
	defaultMemoryLimit   = "1536Mi"
)

const (
	ServerName = "thulu-browser"
	// "1" on the SSE URL of a run on a runner makes thulu-browser sessions open localhost on the
	// Docker host
	// (HOST_GATEWAY), where that run's agent serves the app.
	HostLoopbackQuery = "host_loopback"
	// mcp-config.sh reads the server URL from this var.
	URLEnv = "MCP_THULU_BROWSER_URL"
	// mcp-config.sh also reads this to wait for the server before writing the MCP config.
	PerRunEnv      = "OCTOPUS_BROWSER_PER_RUN"
	RunKeyEnv      = "OCTOPUS_API_KEY"
	DaemonURLEnv   = "OCTOPUS_API"
	RequiredKeyEnv = "MCP_REQUIRED_KEY"
	// Pinned server-side because the agent writes its own ?tools=: it may narrow the list, never
	// widen it.
	GrantedToolsEnv = "MCP_ALLOWED_TOOLS"
)

const (
	LabelBrowser = "thulu.browser"
	LabelOwner   = "thulu.browser.agent"
)

type Config struct {
	Mode          Mode
	Image         string
	Port          int
	CPURequest    resource.Quantity
	CPULimit      resource.Quantity
	MemoryRequest resource.Quantity
	MemoryLimit   resource.Quantity
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	cfg := Config{Port: DefaultPort}

	switch m := Mode(strings.ToLower(env(EnvMode))); m {
	case "":
		cfg.Mode = ModeShared
		if truthy(env(envSaaSMode)) {
			cfg.Mode = ModePerRun
		}
	case ModeShared, ModePerRun:
		cfg.Mode = m
	default:
		return Config{}, apperr.Invalid("browserrun.errors.mode",
			"env", EnvMode, "value", string(m), "shared", string(ModeShared), "per_run", string(ModePerRun))
	}

	if v := env(EnvPort); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return Config{}, apperr.Invalid("browserrun.errors.port", "env", EnvPort, "value", v)
		}
		cfg.Port = p
	}

	cfg.Image = env(EnvImage)
	if cfg.Image == "" {
		cfg.Image = env(envPinnedImage)
	}
	if cfg.Image == "" {
		registry := strings.TrimRight(env(envRegistry), "/")
		if registry == "" {
			registry = defaultRegistry
		}
		cfg.Image = registry + "/" + publishedImage + ":" + imageTag(env(envVersion))
	}

	for _, q := range []struct {
		dst      *resource.Quantity
		key, def string
	}{
		{&cfg.CPURequest, EnvCPURequest, defaultCPURequest},
		{&cfg.CPULimit, EnvCPULimit, defaultCPULimit},
		{&cfg.MemoryRequest, EnvMemoryRequest, defaultMemoryRequest},
		{&cfg.MemoryLimit, EnvMemoryLimit, defaultMemoryLimit},
	} {
		v := env(q.key)
		if v == "" {
			v = q.def
		}
		parsed, err := resource.ParseQuantity(v)
		if err != nil || parsed.Sign() <= 0 {
			return Config{}, apperr.Invalid("browserrun.errors.quantity", "env", q.key, "value", v)
		}
		*q.dst = parsed
	}
	if cfg.CPURequest.Cmp(cfg.CPULimit) > 0 {
		return Config{}, apperr.Invalid("browserrun.errors.request_over_limit", "request_env", EnvCPURequest, "limit_env", EnvCPULimit)
	}
	if cfg.MemoryRequest.Cmp(cfg.MemoryLimit) > 0 {
		return Config{}, apperr.Invalid("browserrun.errors.request_over_limit", "request_env", EnvMemoryRequest, "limit_env", EnvMemoryLimit)
	}
	return cfg, nil
}

func (c Config) PerRun() bool { return c.Mode == ModePerRun }

func (c Config) LocalURL() string {
	return (&url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(c.Port), Path: "/sse"}).String()
}

// Call before the forwarding table is merged so the loopback URL wins.
func (c Config) PrepareAgentEnv(env map[string]string) {
	if !c.PerRun() || !Granted(env["MCP_TOOLS"]) {
		return
	}
	env[URLEnv] = c.LocalURL()
	env[PerRunEnv] = "1"
}

// Recheck MCP_TOOLS: the env is edited after PrepareAgentEnv and engines without MCP support drop
// it.
func Wanted(env map[string]string) bool {
	return env[PerRunEnv] == "1" && Granted(env["MCP_TOOLS"])
}

// Without a run key the browser is useless, and in Kubernetes the missing Secret key would leave
// the pod in CreateContainerConfigError.
func Startable(env map[string]string) bool {
	return Wanted(env) && strings.TrimSpace(env[RunKeyEnv]) != ""
}

func Granted(mcpTools string) bool {
	_, ok := browserEntry(mcpTools)
	return ok
}

// mcp-config.sh treats "read"/"write" suffixes as scopes, not tool names; thulu-browser has no
// scopes, so they mean the whole server.
func GrantedTools(mcpTools string) string {
	suffix, ok := browserEntry(mcpTools)
	if !ok || suffix == "" || suffix == "read" || suffix == "write" {
		return ""
	}
	var names []string
	for _, name := range strings.Split(suffix, "+") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}

func browserEntry(mcpTools string) (suffix string, ok bool) {
	for _, entry := range strings.Split(mcpTools, ",") {
		name, rest, _ := strings.Cut(strings.TrimSpace(entry), "@")
		if name == ServerName {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// HOME=/tmp because the root filesystem is read-only. The tool list goes in env because the agent
// controls ?tools=.
func (c Config) ServerEnv(mcpTools string) map[string]string {
	env := map[string]string{
		"PORT":        strconv.Itoa(c.Port),
		"LISTEN_HOST": "127.0.0.1",
		"HOME":        "/tmp",
	}
	if tools := GrantedTools(mcpTools); tools != "" {
		env[GrantedToolsEnv] = tools
	}
	return env
}

func (c Config) HealthURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/health", c.Port)
}

func (c Config) MemoryLimitBytes() int64 { return c.MemoryLimit.Value() }

func (c Config) NanoCPUs() int64 { return c.CPULimit.MilliValue() * 1_000_000 }

// Must match dockergc's tag logic or the janitor prunes the browser image.
func imageTag(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || version == "dev" {
		return "latest"
	}
	return version
}

func truthy(v string) bool {
	return v == "1" || strings.EqualFold(v, "true")
}
