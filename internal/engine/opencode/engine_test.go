package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestNameAndRegistration(t *testing.T) {
	got, err := engine.Get(Name)
	if err != nil {
		t.Fatalf("engine.Get(%q): %v", Name, err)
	}
	if got.Name() != Name {
		t.Fatalf("Name() = %q, want %q", got.Name(), Name)
	}
}

func TestCapabilities(t *testing.T) {
	caps := New(Config{}).Capabilities()
	if caps.Provider != engine.ProviderOpenAICompatible {
		t.Errorf("Provider = %q", caps.Provider)
	}
	for _, p := range []string{engine.ProviderOpenAICompatible, engine.ProviderAnthropic, engine.ProviderOpenAI} {
		if !caps.SupportsProvider(p) {
			t.Errorf("SupportsProvider(%q) = false", p)
		}
	}
	if caps.SupportsProvider(engine.ProviderBedrock) {
		t.Error("the adapter has no cloud route; bedrock must not be offered")
	}
	if got := caps.Providers(); strings.Join(got, ",") != "openai-compatible,anthropic,openai" {
		t.Errorf("Providers() = %v", got)
	}
	if caps.ProviderEnv != ProviderEnv || caps.BaseURLEnv != BaseURLEnv {
		t.Errorf("ProviderEnv = %q, BaseURLEnv = %q", caps.ProviderEnv, caps.BaseURLEnv)
	}
	if len(caps.KeyKinds) != 1 || caps.KeyKinds[0].ID != "api" || caps.KeyKinds[0].Prefix != "" ||
		strings.Join(caps.KeyKinds[0].EnvVars, ",") != KeyEnv {
		t.Errorf("KeyKinds = %+v, want one prefix-less api kind on %s", caps.KeyKinds, KeyEnv)
	}
	if !caps.MCP || !caps.Skills || !caps.ResumableSessions || !caps.StreamJSON {
		t.Errorf("MCP/Skills/ResumableSessions/StreamJSON must be on: %+v", caps)
	}
	if caps.EffortFlag || caps.TurnBudget || caps.StreamingInput {
		t.Errorf("EffortFlag/TurnBudget/StreamingInput must stay off until opencode.sh renders them")
	}
	if caps.OrgIDEnv != "" || caps.AuthorizationEnv != "" {
		t.Error("the config writer sends the key one way per provider; no org or bearer slot")
	}
	if got := New(Config{}).(interface {
		CapabilitiesIn(context.Context) engine.Capabilities
	}).CapabilitiesIn(context.Background()).Label; got == "" || got == "engine.labels.opencode" {
		t.Errorf("Label = %q, want a catalog string", got)
	}
}

func TestGatewayCanPresent(t *testing.T) {
	caps := New(Config{}).Capabilities()
	for _, p := range []string{"", engine.ProviderOpenAICompatible, engine.ProviderAnthropic, engine.ProviderOpenAI} {
		if !caps.GatewayCanPresent(p, "sk-whatever", "https://api.deepseek.com") {
			t.Errorf("GatewayCanPresent(%q) = false", p)
		}
	}
	if !caps.GatewayCanPresent(engine.ProviderAnthropic, "sk-ant-api03-x", "") {
		t.Error("an Anthropic key on Anthropic's own API must go through the gateway")
	}
	if caps.GatewayCanPresent(engine.ProviderBedrock, "{}", "") {
		t.Error("no cloud route")
	}
}

func TestAuthEnv(t *testing.T) {
	if got := New(Config{}).AuthEnv(context.Background()); got != nil {
		t.Errorf("AuthEnv with no env file = %v, want nil", got)
	}
	envFile := filepath.Join(t.TempDir(), ".env")
	body := "LLM_API_KEY=sk-x\nOCTOPUS_LLM_PROVIDER=anthropic\nANTHROPIC_API_KEY=nope\n"
	if err := os.WriteFile(envFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(New(Config{EnvFile: envFile}).AuthEnv(context.Background()), ",")
	if got != "LLM_API_KEY=sk-x,OCTOPUS_LLM_PROVIDER=anthropic" {
		t.Errorf("AuthEnv = %q", got)
	}
}
