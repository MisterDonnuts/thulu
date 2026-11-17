package task

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func runnerModelEnv(t *testing.T, resolved secrets.ResolvedLLM, agent *types.AgentType) map[string]string {
	t.Helper()
	env := map[string]string{"ENGINE": engine.DefaultName, "MODEL": agent.Model, "EFFORT": agent.Effort}
	applyMachineRun(env, "rn1", resolved, agent, "dev")
	return env
}

// Case 13: a project on a machine with a key profile runs on the machine with the profile's model
// and without its key.
func TestRunnerRunsTakeTheProfileModelWithoutItsKey(t *testing.T) {
	def := secrets.ResolvedLLM{Source: "workspace_default", Engine: "claude-code", Provider: "anthropic",
		Model: "opus", Effort: "high", Credential: "sk-ant-secret"}

	env := runnerModelEnv(t, def, &types.AgentType{})
	if env["MODEL"] != "opus" || env["EFFORT"] != "high" {
		t.Errorf("an agent without a model should take the default profile's: %v", env)
	}
	for k, v := range env {
		if v == "sk-ant-secret" {
			t.Errorf("the profile's credential reached the run as %s", k)
		}
	}
	if env[engine.LLMSourceEnv] != engine.LLMSourceRunnerLogin || env[runner.RunnerIDEnv] != "rn1" {
		t.Error("the run no longer pays with the machine's login")
	}

	env = runnerModelEnv(t, def, &types.AgentType{Model: "haiku"})
	if env["MODEL"] != "haiku" {
		t.Errorf("the agent's own model should beat the default profile: %v", env)
	}

	bound := def
	bound.Source = "binding"
	if env = runnerModelEnv(t, bound, &types.AgentType{Model: "haiku"}); env["MODEL"] != "opus" {
		t.Errorf("a profile bound to the agent should beat the agent's model, as in Thulu: %v", env)
	}
}

func TestRunnerRunsKeepTheAgentModelWhenTheProfileCannotApply(t *testing.T) {
	agent := &types.AgentType{Model: "sonnet"}
	cases := map[string]secrets.ResolvedLLM{
		"another engine":   {Source: "binding", Engine: "codex", Model: "gpt-5"},
		"another provider": {Source: "binding", Engine: "claude-code", Provider: "bedrock", Model: "us.anthropic.x"},
	}
	for name, resolved := range cases {
		if env := runnerModelEnv(t, resolved, agent); env["MODEL"] != "sonnet" {
			t.Errorf("%s: MODEL = %q, want the agent's", name, env["MODEL"])
		}
	}
	if env := runnerModelEnv(t, secrets.ResolvedLLM{Source: "host"}, agent); env["MODEL"] != "sonnet" {
		t.Errorf("no profile at all: MODEL = %q", env["MODEL"])
	}
}
