package task

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func tokenOptAgent(level string) *types.AgentType {
	at := &types.AgentType{
		Name: "dev", ValidStages: []types.Stage{types.StageInProgress, types.StageMention},
		SkillsByStage: map[string][]string{"in_progress": {"notes"}},
		SystemSkills: []types.SystemSkillRef{
			{Name: "octopus-run"},
			{Name: "octopus-token-optimizer", When: types.SkillWhenTokenOptimizer},
		},
	}
	if level != "" {
		at.TokenOptimizer = types.TokenOptimizer{Level: level, Engine: "claude-code", Model: "haiku"}
	}
	return at
}

func workerOf(w tokenopt.WorkerInfo) TokenOptimizerWorker {
	return func(_ context.Context, _, engine string) (tokenopt.WorkerInfo, string, error) {
		w.Engine = engine
		if w.Via == tokenopt.ViaCLI {
			return w, "sk-ant-oat01-worker", nil
		}
		return w, "", nil
	}
}

var (
	apiWorker = tokenopt.WorkerInfo{Available: true, Via: tokenopt.ViaAPI, Provider: "anthropic", ProfileID: "p1"}
	cliWorker = tokenopt.WorkerInfo{Available: true, Via: tokenopt.ViaCLI, Provider: "anthropic", ProfileID: "p2"}
)

func TestApplyTokenOptimizer(t *testing.T) {
	cases := []struct {
		name      string
		worker    TokenOptimizerWorker
		level     string
		engine    string
		wantLevel string
		wantVia   string
	}{
		{"not wired", nil, "enforce", "", "", ""},
		{"agent off", workerOf(apiWorker), "", "", "", ""},
		{"lookup failed", func(context.Context, string, string) (tokenopt.WorkerInfo, string, error) {
			return tokenopt.WorkerInfo{}, "", errors.New("db down")
		}, "enforce", "", "", ""},
		{"no default profile", workerOf(tokenopt.WorkerInfo{Reason: tokenopt.WorkerNoDefaultProfile}), "enforce", "", "", ""},
		{"enforce on claude-code", workerOf(apiWorker), "enforce", "", "enforce", "api"},
		{"enforce on codex is suggest", workerOf(apiWorker), "enforce", "codex", "suggest", "api"},
		{"cli worker on claude-code", workerOf(cliWorker), "enforce", "claude-code", "enforce", "cli"},
		{"cli worker on codex is off", workerOf(cliWorker), "suggest", "codex", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{}
			d.WithTokenOptimizer(tc.worker)
			at := tokenOptAgent(tc.level)
			env := map[string]string{"SKILLS": at.DefaultSkills("in_progress")}
			if tc.engine != "" {
				env["ENGINE"] = tc.engine
			}
			d.applyTokenOptimizer(t.Context(), env, "ws", at, "in_progress")

			if tc.wantLevel == "" {
				for _, k := range tokenopt.EnvKeys {
					if _, ok := env[k]; ok {
						t.Fatalf("off set %s: %v", k, env)
					}
				}
				if strings.Contains(env["SKILLS"], "octopus-token-optimizer") {
					t.Fatalf("off let the skill in: %q", env["SKILLS"])
				}
				return
			}
			if env[tokenopt.EnvLevel] != tc.wantLevel || env[tokenopt.EnvThresholdBytes] != "24576" {
				t.Fatalf("env = %v, want level %s", env, tc.wantLevel)
			}
			if env[tokenopt.EnvReadVia] != tc.wantVia {
				t.Fatalf("read via = %q, want %q", env[tokenopt.EnvReadVia], tc.wantVia)
			}
			cli := tc.wantVia == tokenopt.ViaCLI
			if model, ok := env[tokenopt.EnvReadModel]; ok != cli || (ok && model != "haiku") {
				t.Fatalf("read model = %q (set %v) for via %s", model, ok, tc.wantVia)
			}
			if token, ok := env[tokenopt.EnvWorkerOAuth]; ok != cli || (ok && token != "sk-ant-oat01-worker") {
				t.Fatalf("worker token = %q (set %v) for via %s", token, ok, tc.wantVia)
			}
			if env["SKILLS"] != "notes,octopus-run,octopus-token-optimizer" {
				t.Fatalf("SKILLS = %q", env["SKILLS"])
			}
			if len(at.ActiveWhen) != 0 {
				t.Fatalf("the shared definition was changed: %v", at.ActiveWhen)
			}
		})
	}
}

func TestMentionGetsTokenOptimizerEnv(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	d.WithTokenOptimizer(workerOf(cliWorker))
	tk := &Task{WorkspaceID: testWS, Title: "t"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, _ := store.Get(t.Context(), tk.ID)

	d.HandleMention(context.Background(), full, tokenOptAgent("suggest"), &Comment{ID: "c1", Body: "hola"})
	launched := l.launches()
	if len(launched) != 1 || launched[0][tokenopt.EnvLevel] != "suggest" || launched[0][tokenopt.EnvWorkerOAuth] != "sk-ant-oat01-worker" {
		t.Fatalf("mention env = %v", launched)
	}
	l.exit(t, "ctr-a")

	quiet := tokenOptAgent("")
	quiet.Name = "quiet"
	d.HandleMention(context.Background(), full, quiet, &Comment{ID: "c2", Body: "hola"})
	launched = l.launches()
	if len(launched) != 2 {
		t.Fatalf("launched %d", len(launched))
	}
	if _, ok := launched[1][tokenopt.EnvLevel]; ok {
		t.Fatalf("an agent with the optimizer off got the env: %v", launched[1])
	}
}
