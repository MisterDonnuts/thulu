package browserrun

import (
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestModeFollowsSaaSModeUnlessSet(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Mode
	}{
		{"self-hosted default", nil, ModeShared},
		{"saas default", map[string]string{envSaaSMode: "1"}, ModePerRun},
		{"saas true", map[string]string{envSaaSMode: "TRUE"}, ModePerRun},
		{"explicit shared on saas", map[string]string{envSaaSMode: "1", EnvMode: "shared"}, ModeShared},
		{"explicit per-run self-hosted", map[string]string{EnvMode: "Per-Run"}, ModePerRun},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ConfigFromEnv(envOf(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Mode != tc.want {
				t.Errorf("mode = %q, want %q", cfg.Mode, tc.want)
			}
		})
	}
}

func TestConfigRefusesBadValues(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unknown mode":         {EnvMode: "per-tenant"},
		"port zero":            {EnvPort: "0"},
		"port text":            {EnvPort: "http"},
		"port too high":        {EnvPort: "70000"},
		"bad quantity":         {EnvMemoryLimit: "lots"},
		"negative cpu":         {EnvCPULimit: "-1"},
		"cpu request > limit":  {EnvCPURequest: "2", EnvCPULimit: "1"},
		"memory request > lim": {EnvMemoryRequest: "2Gi"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ConfigFromEnv(envOf(env)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := ConfigFromEnv(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 3010 || cfg.LocalURL() != "http://127.0.0.1:3010/sse" || cfg.HealthURL() != "http://127.0.0.1:3010/health" {
		t.Errorf("port %d, url %q, health %q", cfg.Port, cfg.LocalURL(), cfg.HealthURL())
	}
	if cfg.CPURequest.String() != "500m" || cfg.CPULimit.String() != "1" ||
		cfg.MemoryRequest.String() != "1Gi" || cfg.MemoryLimit.String() != "1536Mi" {
		t.Errorf("resources %s/%s %s/%s", cfg.CPURequest.String(), cfg.CPULimit.String(),
			cfg.MemoryRequest.String(), cfg.MemoryLimit.String())
	}
	if cfg.NanoCPUs() != 1_000_000_000 || cfg.MemoryLimitBytes() != 1536<<20 {
		t.Errorf("docker limits %d cpu, %d bytes", cfg.NanoCPUs(), cfg.MemoryLimitBytes())
	}
	if env := cfg.ServerEnv("thulu-browser"); env["LISTEN_HOST"] != "127.0.0.1" || env["PORT"] != "3010" || env["HOME"] != "/tmp" {
		t.Errorf("server env %v", env)
	}
}

func TestImageResolution(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"published latest", nil, "ghcr.io/misterdonnuts/octopus-mcp-browser:latest"},
		{"release tag", map[string]string{envVersion: "v1.4.0"}, "ghcr.io/misterdonnuts/octopus-mcp-browser:1.4.0"},
		{"dev build", map[string]string{envVersion: "dev"}, "ghcr.io/misterdonnuts/octopus-mcp-browser:latest"},
		{"mirror", map[string]string{envRegistry: "registry.example/octo/", envVersion: "2.0.0"}, "registry.example/octo/octopus-mcp-browser:2.0.0"},
		{"pinned mcp image", map[string]string{envPinnedImage: "thulu-browser:local"}, "thulu-browser:local"},
		{"browser image wins", map[string]string{envPinnedImage: "thulu-browser:local", EnvImage: "b:1"}, "b:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ConfigFromEnv(envOf(tc.env))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Image != tc.want {
				t.Errorf("image = %q, want %q", cfg.Image, tc.want)
			}
		})
	}
}

func TestGranted(t *testing.T) {
	for in, want := range map[string]bool{
		"":                                   false,
		"octopus-api":                        false,
		"thulu-browser":                      true,
		" octopus-api , thulu-browser@write": true,
		"thulu-browser@browser_navigate+browser_screenshot": true,
		"thulu-browser-fork": false,
		"x@thulu-browser":    false,
	} {
		if got := Granted(in); got != want {
			t.Errorf("Granted(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestGrantedTools(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "",
		"octopus-api@a+b":     "",
		"thulu-browser":       "",
		"thulu-browser@write": "",
		"thulu-browser@read":  "",
		"octopus-api@x, thulu-browser@screenshot+ get_text +": "screenshot,get_text",
	} {
		if got := GrantedTools(in); got != want {
			t.Errorf("GrantedTools(%q) = %q, want %q", in, got, want)
		}
	}
	cfg := Config{Port: DefaultPort}
	if env := cfg.ServerEnv("thulu-browser@screenshot"); env[GrantedToolsEnv] != "screenshot" {
		t.Errorf("a partial grant is not pinned: %v", env)
	}
	if _, ok := cfg.ServerEnv("thulu-browser")[GrantedToolsEnv]; ok {
		t.Error("a whole-server grant pins a tool list")
	}
}

func TestPrepareAgentEnv(t *testing.T) {
	perRun := Config{Mode: ModePerRun, Port: 4000}
	env := map[string]string{"MCP_TOOLS": "thulu-browser", URLEnv: "http://shared:3000/sse"}
	perRun.PrepareAgentEnv(env)
	if env[URLEnv] != "http://127.0.0.1:4000/sse" || env[PerRunEnv] != "1" {
		t.Errorf("per-run env %v", env)
	}
	if !Wanted(env) {
		t.Error("not wanted after prepare")
	}
	if Startable(env) {
		t.Error("startable without a run key")
	}
	env[RunKeyEnv] = "oct_run"
	if !Startable(env) {
		t.Error("not startable with a run key")
	}
	delete(env, "MCP_TOOLS")
	if Wanted(env) || Startable(env) {
		t.Error("wanted without MCP_TOOLS")
	}

	shared := Config{Mode: ModeShared, Port: 4000}
	env = map[string]string{"MCP_TOOLS": "thulu-browser", URLEnv: "http://shared:3000/sse"}
	shared.PrepareAgentEnv(env)
	if env[URLEnv] != "http://shared:3000/sse" || env[PerRunEnv] != "" {
		t.Errorf("shared mode edited the env: %v", env)
	}

	env = map[string]string{"MCP_TOOLS": "octopus-api"}
	perRun.PrepareAgentEnv(env)
	if len(env) != 1 {
		t.Errorf("ungranted run edited: %v", env)
	}
}
