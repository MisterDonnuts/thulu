package task

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestApplyLLMAuth_RichResolverOverridesAgentLabels(t *testing.T) {
	d := &Dispatcher{
		llmResolver: func(_ context.Context, ws, agent, fallback string) (secrets.ResolvedLLM, error) {
			return secrets.ResolvedLLM{
				Source:      "binding",
				ProfileID:   "lp_1",
				ProfileName: "team-anthropic",
				Engine:      "claude-code",
				Model:       "opus",
				Effort:      "high",
				BaseURL:     "https://litellm.internal",
				Provider:    "anthropic",
				Credential:  "sk-ant-api-binding",
			}, nil
		},
	}
	env := map[string]string{
		"ENGINE": "claude-code",
		"MODEL":  "sonnet",
		"EFFORT": "low",
	}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "reviewer", &Task{WorkspaceID: "ws-1"}); err != nil {
		t.Fatal(err)
	}

	if env["MODEL"] != "opus" {
		t.Errorf("MODEL override failed: %q", env["MODEL"])
	}
	if env["EFFORT"] != "high" {
		t.Errorf("EFFORT override failed: %q", env["EFFORT"])
	}
	if env["ANTHROPIC_BASE_URL"] != "https://litellm.internal" {
		t.Errorf("BASE_URL injection failed: %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_API_KEY"] != "sk-ant-api-binding" {
		t.Errorf("credential routing failed: %q", env["ANTHROPIC_API_KEY"])
	}
	if _, present := env["CLAUDE_CODE_OAUTH_TOKEN"]; present {
		t.Error("API-key kind should clear the OAuth slot")
	}
	if env[engine.LLMProfileEnv] != "lp_1" {
		t.Errorf("%s = %q", engine.LLMProfileEnv, env[engine.LLMProfileEnv])
	}
}

func TestApplyLLMAuth_CodexEngineOverrideRoutesOpenAIEnv(t *testing.T) {
	d := &Dispatcher{
		llmResolver: func(_ context.Context, ws, agent, fallback string) (secrets.ResolvedLLM, error) {
			return secrets.ResolvedLLM{
				Source:     "binding",
				ProfileID:  "lp_codex_litellm",
				Engine:     "codex",
				Model:      "gpt-4o",
				Effort:     "high",
				BaseURL:    "https://litellm.internal/v1",
				OrgID:      "org-abc123",
				Provider:   "openai",
				Credential: "sk-test-codex",
			}, nil
		},
	}
	env := map[string]string{
		"ENGINE":            "claude-code",
		"MODEL":             "sonnet",
		"EFFORT":            "low",
		"ANTHROPIC_API_KEY": "stale-anthropic",
	}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "reviewer", &Task{WorkspaceID: "ws-1"}); err != nil {
		t.Fatal(err)
	}

	if env["ENGINE"] != "codex" {
		t.Errorf("ENGINE override failed: %q", env["ENGINE"])
	}
	if env["MODEL"] != "gpt-4o" {
		t.Errorf("MODEL override failed: %q", env["MODEL"])
	}
	if _, present := env["EFFORT"]; present {
		t.Errorf("EFFORT should be dropped (codex EffortFlag=false), got %q", env["EFFORT"])
	}
	if env["OPENAI_API_KEY"] != "sk-test-codex" {
		t.Errorf("OPENAI_API_KEY routing failed: %q", env["OPENAI_API_KEY"])
	}
	if env["OPENAI_BASE_URL"] != "https://litellm.internal/v1" {
		t.Errorf("OPENAI_BASE_URL injection failed: %q", env["OPENAI_BASE_URL"])
	}
	if env["OPENAI_ORG_ID"] != "org-abc123" {
		t.Errorf("OPENAI_ORG_ID injection failed: %q", env["OPENAI_ORG_ID"])
	}
	if env["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("ANTHROPIC_API_KEY must be cleared when engine flips to codex, got %q", env["ANTHROPIC_API_KEY"])
	}
}

func TestApplyLLMAuth_UnknownEngineFallsThroughGeneric(t *testing.T) {
	d := &Dispatcher{
		llmResolver: func(_ context.Context, ws, agent, fallback string) (secrets.ResolvedLLM, error) {
			return secrets.ResolvedLLM{
				Source:     "binding",
				Engine:     "no-such-engine",
				Credential: "any-credential",
			}, nil
		},
	}
	env := map[string]string{}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "reviewer", &Task{WorkspaceID: "ws-1"}); err != nil {
		t.Fatal(err)
	}
	if env["ENGINE"] != "no-such-engine" {
		t.Errorf("ENGINE not overridden: %q", env["ENGINE"])
	}
	if env["LLM_API_KEY"] != "any-credential" {
		t.Errorf("expected LLM_API_KEY passthrough, got %q", env["LLM_API_KEY"])
	}
}

func TestApplyLLMAuth_HostSourceLeavesEnvAlone(t *testing.T) {
	d := &Dispatcher{
		llmResolver: func(_ context.Context, ws, agent, fallback string) (secrets.ResolvedLLM, error) {
			return secrets.ResolvedLLM{Source: "host", Engine: fallback}, nil
		},
	}
	d.WithHostLLMCredential(true)
	env := map[string]string{
		"MODEL":             "sonnet",
		"ANTHROPIC_API_KEY": "inherited",
	}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "reviewer", &Task{WorkspaceID: "ws-1"}); err != nil {
		t.Fatal(err)
	}
	if env["MODEL"] != "sonnet" {
		t.Errorf("MODEL should not be overridden by host source, got %q", env["MODEL"])
	}
	if env["ANTHROPIC_API_KEY"] != "inherited" {
		t.Errorf("ANTHROPIC_API_KEY should be untouched, got %q", env["ANTHROPIC_API_KEY"])
	}
}

func TestApplyLLMAuth_FallsBackToLegacyResolverWhenRichAbsent(t *testing.T) {
	d := &Dispatcher{
		llmKeys: func(_ context.Context, ws, eng string) (string, bool) { return "sk-ant-api-legacy", true },
	}
	env := map[string]string{}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", &types.AgentType{Engine: "claude-code"}, "reviewer", &Task{WorkspaceID: "ws-1"}); err != nil {
		t.Fatal(err)
	}
	if env["ANTHROPIC_API_KEY"] != "sk-ant-api-legacy" {
		t.Errorf("legacy resolver path broken: %q", env["ANTHROPIC_API_KEY"])
	}
}

func TestApplyLLMAuth_TouchAndAuditFireForBoundProfile(t *testing.T) {
	touched := ""
	audited := ""
	d := &Dispatcher{
		llmResolver: func(_ context.Context, ws, agent, fallback string) (secrets.ResolvedLLM, error) {
			return secrets.ResolvedLLM{
				Source: "binding", ProfileID: "lp_X", Engine: "claude-code",
				Credential: "sk-ant-api-bound",
			}, nil
		},
		llmProfTouch: func(_ context.Context, ws, id string) { touched = ws + "/" + id },
		llmAudit:     func(t *Task, agentName string, r secrets.ResolvedLLM) { audited = agentName + ":" + r.Source },
	}
	if err := d.applyLLMAuth(t.Context(), map[string]string{}, "ws-1",
		&types.AgentType{Engine: "claude-code"}, "reviewer", &Task{WorkspaceID: "ws-1", ID: "t-1"}); err != nil {
		t.Fatal(err)
	}
	if touched != "ws-1/lp_X" {
		t.Errorf("touch hook = %q", touched)
	}
	if audited != "reviewer:binding" {
		t.Errorf("audit hook = %q", audited)
	}
}
