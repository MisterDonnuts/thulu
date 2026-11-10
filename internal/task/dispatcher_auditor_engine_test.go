package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestAuditorEngineFollowsTheRunsEngine(t *testing.T) {
	stage := string(types.StageInProgress)
	env := map[string]string{"ENGINE": "claude-code"}
	applyAuditorEnv(env, &types.AuditorSpec{Stages: []string{stage}}, stage)
	if env["AUDITOR_ENGINE"] != "claude-code" {
		t.Fatalf("AUDITOR_ENGINE = %q, want the agent's engine", env["AUDITOR_ENGINE"])
	}

	applyEngineEnvForName(env, "codex", "developer")
	if env["AUDITOR_ENGINE"] != "codex" {
		t.Errorf("after the switch AUDITOR_ENGINE = %q, want codex", env["AUDITOR_ENGINE"])
	}

	agent := map[string]string{}
	applyEngineEnvForName(agent, "codex", "developer")
	applyAuditorEnv(agent, &types.AuditorSpec{Stages: []string{"refinement"}}, stage)
	if v, ok := agent["AUDITOR_ENGINE"]; ok {
		t.Errorf("agent env carries AUDITOR_ENGINE=%q", v)
	}
}

type scopeRecorder struct{ scopes []auth.RunScope }

func (r *scopeRecorder) MintRunKey(_ context.Context, s auth.RunScope, _ string, _ time.Duration) (string, string, error) {
	r.scopes = append(r.scopes, s)
	return "run-key", "key-id", nil
}

func (r *scopeRecorder) RevokeAPIKey(context.Context, string) error { return nil }

// profilesByEngine answers like the secrets store: the agent's binding first, then the engine's
// default.
func profilesByEngine(calls *[]string) LLMResolver {
	return func(_ context.Context, _, agent, fallback string) (secrets.ResolvedLLM, error) {
		*calls = append(*calls, agent+"/"+fallback)
		if agent == "developer" {
			return secrets.ResolvedLLM{Source: "binding", ProfileID: "bound", Engine: "claude-code",
				Credential: "sk-ant-api-bound", Model: "opus"}, nil
		}
		switch fallback {
		case "codex":
			return secrets.ResolvedLLM{Source: "workspace_default", ProfileID: "dflt-codex", Engine: "codex",
				Credential: "sk-openai", Provider: "openai"}, nil
		case "claude-code":
			return secrets.ResolvedLLM{Source: "workspace_default", ProfileID: "dflt-claude", Engine: "claude-code",
				Credential: "sk-ant-api-default"}, nil
		}
		return secrets.ResolvedLLM{}, errors.New("no default profile for " + fallback)
	}
}

func auditedOn(t *testing.T, eng string) (*Dispatcher, *Store, *convLauncher, *Task, *scopeRecorder, *[]string) {
	t.Helper()
	d, store, l, tk := auditedDispatcher(t)
	stage := string(types.StageInProgress)
	l.agent.Engine, l.agent.Model, l.agent.Effort = "claude-code", "sonnet", "high"
	l.agent.Auditor.EngineByStage = map[string]string{stage: eng}
	keys := &scopeRecorder{}
	calls := &[]string{}
	d.WithTaskKeys(keys)
	d.WithLLMResolver(profilesByEngine(calls), nil, nil)
	return d, store, l, tk, keys, calls
}

func TestAuditorRunsOnTheEngineChosenForTheStage(t *testing.T) {
	d, _, l, tk, keys, _ := auditedOn(t, "codex")
	d.launchStage(t.Context(), tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exit(t, "ctr-a")

	launches := l.launches()
	if len(launches) != 2 {
		t.Fatalf("launches = %d, want agent and auditor", len(launches))
	}
	agent, audit := launches[0], launches[1]
	if agent["ENGINE"] != "claude-code" || agent["ANTHROPIC_API_KEY"] != "sk-ant-api-bound" {
		t.Errorf("the agent left its engine: ENGINE=%q key=%q", agent["ENGINE"], agent["ANTHROPIC_API_KEY"])
	}
	if audit["ENGINE"] != "codex" || audit["AUDITOR_ENGINE"] != "codex" {
		t.Errorf("auditor ENGINE=%q AUDITOR_ENGINE=%q, want codex", audit["ENGINE"], audit["AUDITOR_ENGINE"])
	}
	if audit["OPENAI_API_KEY"] != "sk-openai" {
		t.Errorf("auditor OPENAI_API_KEY=%q, want the codex default profile's", audit["OPENAI_API_KEY"])
	}
	for _, k := range []string{"ANTHROPIC_API_KEY", "MODEL", "EFFORT", "AUDITOR_MODEL"} {
		if v := audit[k]; v != "" {
			t.Errorf("auditor carries %s=%q from the agent's engine", k, v)
		}
	}
	last := keys.scopes[len(keys.scopes)-1]
	if last.Agent != auth.AuditorAgent || last.LLMEngine != "codex" || last.LLMAgent != "developer" {
		t.Errorf("auditor key scope = %+v, want the codex default for the gateway", last)
	}
}

func TestAuditorModelAppliesOnTheChosenEngine(t *testing.T) {
	d, _, l, tk, _, _ := auditedOn(t, "codex")
	l.agent.Auditor.ModelByStage = map[string]string{string(types.StageInProgress): "o3"}
	d.launchStage(t.Context(), tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exit(t, "ctr-a")
	if got := l.launches()[1]["AUDITOR_MODEL"]; got != "o3" {
		t.Errorf("AUDITOR_MODEL = %q, want o3", got)
	}
}

func TestAuditorOnTheAgentsEngineKeepsTheAgentsProfile(t *testing.T) {
	for _, eng := range []string{"", "claude-code"} {
		t.Run("engine="+eng, func(t *testing.T) {
			d, _, l, tk, keys, _ := auditedOn(t, eng)
			d.launchStage(t.Context(), tk, types.StageInProgress, "developer", l.agent, launchOpts{})
			l.exit(t, "ctr-a")
			audit := l.launches()[1]
			if audit["ENGINE"] != "claude-code" || audit["ANTHROPIC_API_KEY"] != "sk-ant-api-bound" {
				t.Errorf("ENGINE=%q key=%q, want the agent's", audit["ENGINE"], audit["ANTHROPIC_API_KEY"])
			}
			if last := keys.scopes[len(keys.scopes)-1]; last.LLMAgent != "developer" || last.LLMEngine != "" {
				t.Errorf("key scope = %+v, want the gateway to resolve the audited agent", last)
			}
		})
	}
}

func TestAuditorAgentTypeDropsWhatBelongsToTheAgentsEngine(t *testing.T) {
	d, _, l, _, _, _ := auditedOn(t, "codex")
	l.agent.Image, l.agent.Dir = "registry/custom:1", "/agents/developer"
	view := d.auditorAgentType(t.Context(), testWS, l.agent, types.StageInProgress)
	if view == l.agent {
		t.Fatal("got the agent itself, want a copy")
	}
	if view.Engine != "codex" || view.Model != "" || view.Effort != "" || view.Image != "" || view.Dir != "" || !view.IgnoreLLMBinding {
		t.Errorf("view = %+v", view)
	}
	if l.agent.Engine != "claude-code" || l.agent.Image == "" || l.agent.IgnoreLLMBinding {
		t.Errorf("the stored agent changed: %+v", l.agent)
	}
}

func TestAuditorThatEndsWithoutAVerdictBlocksTheTask(t *testing.T) {
	d, store, l, tk := auditedDispatcher(t)
	ctx := t.Context()
	d.launchStage(ctx, tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exit(t, "ctr-a")

	l.mu.Lock()
	end := l.exits["ctr-b"]
	l.mu.Unlock()
	if end == nil {
		t.Fatal("no auditor run to end")
	}
	end(91, "", "")

	got, err := store.Get(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusBlocked || got.Stage != types.StageInProgress {
		t.Errorf("status %q stage %q, want blocked where it was", got.Status, got.Stage)
	}
}

func TestAuditorThatCannotStartBlocksTheTask(t *testing.T) {
	d, store, l, tk, _, _ := auditedOn(t, "opencode")
	d.launchStage(t.Context(), tk, types.StageInProgress, "developer", l.agent, launchOpts{})
	l.exit(t, "ctr-a")

	if n := len(l.launches()); n != 1 {
		t.Fatalf("launches = %d, want only the agent's", n)
	}
	got, err := store.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusBlocked || got.Stage != types.StageInProgress {
		t.Errorf("status %q stage %q, want blocked where it was", got.Status, got.Stage)
	}
}
