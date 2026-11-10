package task

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type fakeLogins map[string]string

func (f fakeLogins) Volume(_ context.Context, userID, engineName string) (string, bool) {
	if engineName != claudecode.Name {
		return "", false
	}
	v, ok := f[userID]
	return v, ok
}

const wsKey = "sk-ant-api-workspace"

func newPersonalDispatcher(t *testing.T) (*Dispatcher, *Store, *recordingLauncher) {
	t.Helper()
	d, store := newStageTestDispatcher(t, customBoard())
	l := newRecordingLauncher()
	d.launcher = l
	d.WithLLMKeys(func(context.Context, string, string) (string, bool) { return wsKey, true })
	d.WithPersonalLogins(fakeLogins{"ada": "octopus-claude-ada"})
	return d, store, l
}

func loginAgent() *types.AgentType {
	return &types.AgentType{Name: "developer", Engine: claudecode.Name}
}

func runTrigger(t *testing.T, store *Store, taskID string, env map[string]string) string {
	t.Helper()
	who, err := store.RunTriggeredBy(t.Context(), taskID, env["INSTANCE_ID"])
	if err != nil {
		t.Fatalf("RunTriggeredBy: %v", err)
	}
	return who
}

func TestPersonalLogin_DispatchSelection(t *testing.T) {
	cases := []struct {
		name     string
		trigger  string
		agent    *types.AgentType
		personal bool
	}{
		{"person with a login runs on it", "ada", loginAgent(), true},
		{"person without one runs on the workspace key", "bob", loginAgent(), false},
		{"nobody (automation) runs on the workspace key", "", loginAgent(), false},
		{"an engine with no personal login runs on the workspace key", "ada",
			&types.AgentType{Name: "developer", Engine: "codex"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, store, l := newPersonalDispatcher(t)
			tk := gatedTask(t, store)
			ctx := WithTriggeredBy(context.Background(), tc.trigger)
			d.launchStage(ctx, tk, tk.Stage, "developer", tc.agent, launchOpts{})

			envs := l.launches()
			if len(envs) != 1 {
				t.Fatalf("launches = %d, want 1", len(envs))
			}
			env := envs[0]
			if got := runTrigger(t, store, tk.ID, env); got != tc.trigger {
				t.Errorf("triggered_by = %q, want %q", got, tc.trigger)
			}
			_, onLogin := env["CLAUDE_CONFIG_DIR"]
			if onLogin != tc.personal || (env[EnvPersonalLoginVolume] != "") != tc.personal {
				t.Fatalf("personal login = %v (volume %q), want %v", onLogin, env[EnvPersonalLoginVolume], tc.personal)
			}
			if !tc.personal {
				return
			}
			if env["CLAUDE_CONFIG_DIR"] != claudecode.LoginConfigDir || env[EnvPersonalLoginVolume] != "octopus-claude-ada" {
				t.Errorf("config dir %q volume %q", env["CLAUDE_CONFIG_DIR"], env[EnvPersonalLoginVolume])
			}
			for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "LLM_API_KEY"} {
				if v, ok := env[k]; ok {
					t.Errorf("%s = %q on a run that uses the person's login", k, v)
				}
			}
			if engine.HostAuthApplies(env) {
				t.Error("the launcher would overlay the host credential on the person's login")
			}
		})
	}
}

func TestApplyPersonalLogin_ClearsWhatOutranksTheLogin(t *testing.T) {
	env := map[string]string{
		"ENGINE":                  claudecode.Name,
		"ANTHROPIC_API_KEY":       "octopus-run-token",
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat-host",
		"ANTHROPIC_AUTH_TOKEN":    "bearer",
		"ANTHROPIC_BASE_URL":      "http://gateway/anthropic",
		"CLAUDE_CODE_USE_BEDROCK": "1",
		"LLM_API_KEY":             "x",
		engine.LLMGatewayEnv:      "1",
		engine.LLMProfileEnv:      "prof-1",
		"MODEL":                   "opus",
	}
	if err := applyPersonalLogin(env, personalRun{user: "ada", engine: claudecode.Name, volume: "octopus-claude-ada"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
		"CLAUDE_CODE_USE_BEDROCK", "LLM_API_KEY", engine.LLMGatewayEnv, engine.LLMProfileEnv,
	} {
		if v, ok := env[k]; ok {
			t.Errorf("%s survived as %q", k, v)
		}
	}
	if env["MODEL"] != "opus" {
		t.Errorf("MODEL = %q: the agent's own model must stay", env["MODEL"])
	}
	if env[engine.LLMSourceEnv] != engine.LLMSourcePersonal {
		t.Errorf("source = %q", env[engine.LLMSourceEnv])
	}
}

func TestPersonalLogin_NoLoginNoProfileFails(t *testing.T) {
	d, store, l := newPersonalDispatcher(t)
	d.WithLLMKeys(func(context.Context, string, string) (string, bool) { return "", false })
	tk := gatedTask(t, store)
	d.launchStage(WithTriggeredBy(context.Background(), "bob"), tk, tk.Stage, "developer", loginAgent(), launchOpts{})
	if len(l.launches()) != 0 {
		t.Fatal("a run with no credential at all launched")
	}
	after, _ := store.Get(t.Context(), tk.ID)
	if after.Status != StatusNotStarted {
		t.Errorf("status = %q, want not_started", after.Status)
	}
}

func TestPersonalLogin_RunsOfOnePersonOverlap(t *testing.T) {
	d, store, l := newPersonalDispatcher(t)
	first, second := gatedTask(t, store), gatedTask(t, store)
	ada := WithTriggeredBy(context.Background(), "ada")

	d.launchStage(ada, first, first.Stage, "developer", loginAgent(), launchOpts{})
	d.launchStage(ada, second, second.Stage, "developer", loginAgent(), launchOpts{})
	if got := l.launchedTasks(); len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("launches = %v, want both of ada's runs running at once", got)
	}
	for _, e := range l.launches() {
		if e[EnvPersonalLoginVolume] != "octopus-claude-ada" {
			t.Errorf("%s = %q, want ada's volume", EnvPersonalLoginVolume, e[EnvPersonalLoginVolume])
		}
	}
}

func TestPersonalLogin_TriggerNotInPayload(t *testing.T) {
	d, store, _ := newPersonalDispatcher(t)
	tk := gatedTask(t, store)
	d.launchStage(WithTriggeredBy(context.Background(), "ada"), tk, tk.Stage, "developer", loginAgent(), launchOpts{})
	events, err := store.ListEvents(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind != EventAgentRunStarted {
			continue
		}
		var p map[string]string
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["llm_source"] != engine.LLMSourcePersonal {
			t.Errorf("llm_source = %q", p["llm_source"])
		}
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), `"ada"`) {
			t.Errorf("the event as listed carries the user id: %s", raw)
		}
		return
	}
	t.Fatal("no run-started event")
}
