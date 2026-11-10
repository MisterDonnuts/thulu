package api

import (
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/secrets"

	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
)

func TestAvailableEnginesCarryModelAndTurnBudget(t *testing.T) {
	want := map[string]struct {
		model      string
		turnBudget bool
	}{
		"claude-code": {"sonnet", true},
		"codex":       {"gpt-5.4-mini", false},
		"opencode":    {"deepseek-chat", false},
	}
	seen := 0
	for _, d := range availableEngines(context.Background()) {
		w, ok := want[d.Name]
		if !ok {
			continue
		}
		seen++
		if d.DefaultModel != w.model || d.TurnBudget != w.turnBudget {
			t.Errorf("%s: default_model=%q turn_budget=%v, want %q/%v", d.Name, d.DefaultModel, d.TurnBudget, w.model, w.turnBudget)
		}
	}
	if seen != len(want) {
		t.Errorf("saw %d of %d engines", seen, len(want))
	}
}

func TestEngineInfoDefaultModel(t *testing.T) {
	d := &engineDescriptor{Name: "codex", DefaultModel: "gpt-4o-mini"}
	if got := engineInfo(d, nil, llmKeyStatusView{}, false, false).DefaultModel; got != "gpt-4o-mini" {
		t.Errorf("no profile: default_model = %q, want the engine's", got)
	}
	bare := &secrets.LLMProfile{ID: "p1", Name: "Team", Engine: "codex"}
	if got := engineInfo(d, bare, llmKeyStatusView{}, false, false).DefaultModel; got != "gpt-4o-mini" {
		t.Errorf("profile without model: default_model = %q, want the engine's", got)
	}
	pinned := &secrets.LLMProfile{ID: "p2", Name: "Team", Engine: "codex", Model: "o3"}
	info := engineInfo(d, pinned, llmKeyStatusView{hint: "sk-…abcd"}, true, false)
	if info.DefaultModel != "o3" || info.DefaultProfileID != "p2" || !info.HasKey || info.KeyHint != "sk-…abcd" {
		t.Errorf("pinned profile: %+v", info)
	}
	if info.FallbackModel != "gpt-4o-mini" {
		t.Errorf("pinned profile: fallback_model = %q, want the engine's", info.FallbackModel)
	}
}

func TestAvailableEnginesFollowTheRequestLocale(t *testing.T) {
	label := func(locale string) string {
		for _, d := range availableEngines(i18n.WithLocale(context.Background(), locale)) {
			if d.Name == "claude-code" {
				for _, k := range d.KeyKinds {
					if k.ID == "api" {
						return k.Label
					}
				}
			}
		}
		return ""
	}
	if es, en := label("es"), label("en"); es == "" || es == en {
		t.Errorf("api key label es=%q en=%q, want two wordings", es, en)
	}
}

func TestAvailableEnginesListEveryProvider(t *testing.T) {
	want := map[string]string{
		"claude-code": "anthropic,bedrock,vertex",
		"codex":       "openai",
		"opencode":    "openai-compatible,anthropic,openai",
	}
	for _, d := range availableEngines(context.Background()) {
		if w, ok := want[d.Name]; ok && strings.Join(d.Providers, ",") != w {
			t.Errorf("%s: providers = %v, want %s", d.Name, d.Providers, w)
		}
	}
}
