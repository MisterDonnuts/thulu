package tokenopt

import (
	"maps"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestResolve(t *testing.T) {
	agent := func(p types.TokenOptimizer) *types.AgentType { return &types.AgentType{Name: "dev", TokenOptimizer: p} }
	haiku := func(level string) *types.AgentType {
		return agent(types.TokenOptimizer{Level: level, Engine: "claude-code", Model: "haiku"})
	}
	api := WorkerInfo{Engine: "claude-code", Available: true, Via: ViaAPI, Provider: "anthropic", ProfileID: "p1"}
	cli := WorkerInfo{Engine: "claude-code", Available: true, Via: ViaCLI, Provider: "anthropic", ProfileID: "p2"}
	none := WorkerInfo{Engine: "claude-code", Reason: WorkerNoDefaultProfile}
	off := Resolved{Level: "off", ThresholdBytes: DefaultThresholdBytes}
	noWorker := Resolved{Level: "off", ThresholdBytes: DefaultThresholdBytes, Reason: ReasonNoWorker}
	on := func(level, via string) Resolved {
		return Resolved{Level: level, ThresholdBytes: DefaultThresholdBytes, ReadEngine: "claude-code", ReadVia: via, ReadModel: "haiku"}
	}

	cases := []struct {
		name   string
		at     *types.AgentType
		engine string
		worker WorkerInfo
		want   Resolved
	}{
		{"no agent", nil, "", api, off},
		{"off by default", agent(types.TokenOptimizer{}), "claude-code", api, off},
		{"off said aloud", agent(types.TokenOptimizer{Level: "off", Engine: "claude-code", Model: "haiku"}), "", api, off},
		{"garbage level is off", haiku("maybe"), "", api, off},
		{"suggest through the api", haiku("suggest"), "claude-code", api, on("suggest", ViaAPI)},
		{"enforce through the api", haiku("enforce"), "", api, on("enforce", ViaAPI)},
		{"enforce through the cli", haiku("enforce"), "claude-code", cli, on("enforce", ViaCLI)},
		{"engine without a default profile", haiku("suggest"), "claude-code", none, noWorker},
		{"unusable credential", haiku("suggest"), "claude-code",
			WorkerInfo{Engine: "claude-code", Reason: WorkerUnsupported, ProfileID: "p3"}, noWorker},
		{"model required", agent(types.TokenOptimizer{Level: "suggest", Engine: "claude-code"}), "", api, noWorker},
		{"engine required", agent(types.TokenOptimizer{Level: "suggest", Model: "haiku"}), "", api, noWorker},
		{"worker of another engine", haiku("suggest"), "", WorkerInfo{Engine: "codex", Available: true, Via: ViaAPI}, noWorker},
		{"enforce on codex is suggest", haiku("enforce"), "codex", api,
			Resolved{Level: "suggest", ThresholdBytes: DefaultThresholdBytes, Reason: ReasonEngineWithoutHooks,
				ReadEngine: "claude-code", ReadVia: ViaAPI, ReadModel: "haiku"}},
		{"suggest on codex stays suggest", haiku("suggest"), "codex", api, on("suggest", ViaAPI)},
		{"cli worker for an opencode agent", haiku("suggest"), "opencode", cli, noWorker},
		{"cli worker for the default engine", haiku("suggest"), "", cli, on("suggest", ViaCLI)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.at, tc.engine, tc.worker); got != tc.want {
				t.Fatalf("Resolve = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEnv(t *testing.T) {
	if env := Env(Resolved{Level: "off", ThresholdBytes: 10}, "sk-ant-oat01-x"); env != nil {
		t.Fatalf("off must set nothing, got %v", env)
	}
	got := Env(Resolved{Level: "enforce", ThresholdBytes: 24576, ReadEngine: "claude-code", ReadVia: ViaAPI, ReadModel: "haiku"}, "sk-ant-oat01-x")
	want := map[string]string{EnvLevel: "enforce", EnvThresholdBytes: "24576", EnvReadVia: "api"}
	if !maps.Equal(got, want) {
		t.Fatalf("Env = %v, want %v", got, want)
	}
	got = Env(Resolved{Level: "suggest", ThresholdBytes: 1, ReadEngine: "claude-code", ReadVia: ViaCLI, ReadModel: "claude-haiku-4-5"}, "sk-ant-oat01-x")
	want = map[string]string{EnvLevel: "suggest", EnvThresholdBytes: "1", EnvReadVia: "cli",
		EnvReadModel: "claude-haiku-4-5", EnvWorkerOAuth: "sk-ant-oat01-x"}
	if !maps.Equal(got, want) {
		t.Fatalf("cli Env = %v, want %v", got, want)
	}
	for k := range got {
		if !slices.Contains(EnvKeys, k) {
			t.Errorf("%s is missing from EnvKeys", k)
		}
	}
}
