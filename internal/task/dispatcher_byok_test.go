package task

import (
	"context"
	"strings"
	"testing"

	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestApplyBYOKKey_RoutesByPrefix_ClaudeCode(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		wantSet     map[string]string
		wantCleared []string
	}{
		{
			name:        "api key routes to ANTHROPIC_API_KEY and clears OAuth",
			key:         "sk-ant-api-abc123",
			wantSet:     map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api-abc123"},
			wantCleared: []string{"CLAUDE_CODE_OAUTH_TOKEN"},
		},
		{
			name:        "oauth token routes to CLAUDE_CODE_OAUTH_TOKEN and clears API key",
			key:         "sk-ant-oat-xyz789",
			wantSet:     map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat-xyz789"},
			wantCleared: []string{"ANTHROPIC_API_KEY"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{llmKeys: func(_ context.Context, ws, eng string) (string, bool) { return tc.key, true }}
			env := map[string]string{
				"ANTHROPIC_API_KEY":       "inherited-api",
				"CLAUDE_CODE_OAUTH_TOKEN": "inherited-oauth",
			}
			d.applyBYOKKey(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"})

			for k, want := range tc.wantSet {
				if got := env[k]; got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			for _, k := range tc.wantCleared {
				if _, present := env[k]; present {
					t.Errorf("%s should have been cleared, still %q", k, env[k])
				}
			}
		})
	}
}

func TestApplyBYOKKey_UnknownEngine_PassesThrough(t *testing.T) {
	d := &Dispatcher{llmKeys: func(_ context.Context, ws, eng string) (string, bool) { return "any-credential", true }}
	env := map[string]string{}
	d.applyBYOKKey(t.Context(), env, "ws-1", &types.AgentType{Engine: "no-such-engine"})
	if env["LLM_API_KEY"] != "any-credential" {
		t.Errorf("LLM_API_KEY = %q, want any-credential", env["LLM_API_KEY"])
	}
}

func TestApplyBYOKKey_NoWorkspace_NoOp(t *testing.T) {
	called := false
	d := &Dispatcher{llmKeys: func(_ context.Context, ws, eng string) (string, bool) { called = true; return "", false }}
	env := map[string]string{"ANTHROPIC_API_KEY": "inherited"}
	d.applyBYOKKey(t.Context(), env, "", &types.AgentType{Engine: "claude-code"})
	if called {
		t.Error("resolver called for empty WorkspaceID")
	}
	if env["ANTHROPIC_API_KEY"] != "inherited" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want inherited", env["ANTHROPIC_API_KEY"])
	}
}

// A subscription token never reaches a Kubernetes agent without the operator's opt-in, whichever
// path resolved it.
func TestResolveLLMCredential_KubernetesRefusesTheSubscriptionToken(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_RUNTIME", "kubernetes")
	t.Setenv("OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN", "")
	d := &Dispatcher{llmKeys: func(_ context.Context, ws, eng string) (string, bool) { return "sk-ant-oat-xyz789", true }}
	env := map[string]string{"ENGINE": "claude-code"}
	err := d.resolveLLMCredential(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "dev", nil, false)
	if err == nil || !strings.Contains(err.Error(), "OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN") {
		t.Fatalf("err = %v, want one naming the opt-in", err)
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		t.Error("the token stayed in the run's env")
	}

	t.Setenv("OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN", "1")
	env = map[string]string{"ENGINE": "claude-code"}
	if err := d.resolveLLMCredential(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "dev", nil, false); err != nil {
		t.Fatalf("with the opt-in: %v", err)
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat-xyz789" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q with the opt-in", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}

	t.Setenv("OCTOPUS_AGENT_RUNTIME", "docker")
	t.Setenv("OCTOPUS_KUBE_ALLOW_CLAUDE_OAUTH_TOKEN", "")
	env = map[string]string{"ENGINE": "claude-code"}
	if err := d.resolveLLMCredential(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "dev", nil, false); err != nil {
		t.Fatalf("docker mode: %v", err)
	}
}
