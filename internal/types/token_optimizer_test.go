package types

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func TestParseTokenOptimizerLabels(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   TokenOptimizer
	}{
		{"nothing", map[string]string{}, TokenOptimizer{}},
		{"complete", map[string]string{
			LabelTokenOptimizer: " Enforce ", LabelTokenOptimizerEngine: "claude-code", LabelTokenOptimizerModel: " haiku ",
		}, TokenOptimizer{Level: TokenOptimizerEnforce, Engine: "claude-code", Model: "haiku"}},
		{"typo is off", map[string]string{
			LabelTokenOptimizer: "enforced", LabelTokenOptimizerEngine: "claude-code", LabelTokenOptimizerModel: "haiku",
		}, TokenOptimizer{}},
		{"off keeps nothing", map[string]string{
			LabelTokenOptimizer: "off", LabelTokenOptimizerEngine: "claude-code", LabelTokenOptimizerModel: "haiku",
		}, TokenOptimizer{}},
		{"firm", map[string]string{LabelTokenOptimizer: "firm"}, TokenOptimizer{}},
		{"level without a worker", map[string]string{
			LabelTokenOptimizer: "suggest", LabelTokenOptimizer + ".qa": "off", LabelTokenOptimizer + ".threshold_bytes": "4096",
		}, TokenOptimizer{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTokenOptimizerLabels(tc.labels); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTokenOptimizerLabelsRoundTrip(t *testing.T) {
	in := TokenOptimizer{Level: "enforce", Engine: "codex", Model: "gpt-5-mini"}
	labels := TokenOptimizerLabels(in)
	for k := range labels {
		if !IsTokenOptimizerLabel(k) {
			t.Errorf("%s is not recognized as a token optimizer label", k)
		}
	}
	if out := parseTokenOptimizerLabels(labels); out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
	if got := TokenOptimizerLabels(TokenOptimizer{Level: "off", Engine: "codex"}); len(got) != 0 {
		t.Fatalf("off writes labels: %v", got)
	}
	for _, old := range []string{LabelTokenOptimizer + ".review", LabelTokenOptimizer + ".threshold_bytes"} {
		if !IsTokenOptimizerLabel(old) {
			t.Errorf("%s is not recognized as a token optimizer label", old)
		}
	}
}

func TestTokenOptimizerValidate(t *testing.T) {
	known := func(name string) bool { return name == "claude-code" || name == "codex" }
	for name, ok := range map[string]TokenOptimizer{
		"zero":     {},
		"suggest":  {Level: "suggest", Engine: "claude-code", Model: "haiku"},
		"enforce":  {Level: "enforce", Engine: "codex", Model: "gpt-5-mini"},
		"off said": TokenOptimizer{Level: " OFF ", Engine: "nope"}.Normalize(),
	} {
		if err := ok.Validate(known); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, tc := range map[string]struct {
		in  TokenOptimizer
		key string
	}{
		"level":   {TokenOptimizer{Level: "firm"}, "tokenopt.errors.invalid_level"},
		"engine":  {TokenOptimizer{Level: "suggest", Model: "haiku"}, "tokenopt.errors.engine_required"},
		"unknown": {TokenOptimizer{Level: "suggest", Engine: "aider", Model: "haiku"}, "tokenopt.errors.unknown_engine"},
		"model":   {TokenOptimizer{Level: "enforce", Engine: "claude-code"}, "tokenopt.errors.model_required"},
	} {
		err := tc.in.Validate(known)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Key != tc.key || ae.Kind != apperr.KindInvalid {
			t.Errorf("%s: %v, want %s", name, err, tc.key)
		}
	}
}

func TestTokenOptimizerOldShapeLoadsAsOff(t *testing.T) {
	for _, raw := range []string{
		`{"level":"enforce","by_stage":{"review":"off"},"threshold_bytes":8192}`,
		`{"firm":true}`,
		`{"level":"suggest","engine":"claude-code"}`,
	} {
		var got TokenOptimizer
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got = got.OrOff(); !got.IsZero() {
			t.Fatalf("%s loads as %+v, want off", raw, got)
		}
	}
	complete := TokenOptimizer{Level: "Suggest", Engine: "claude-code", Model: "haiku"}.OrOff()
	if complete != (TokenOptimizer{Level: "suggest", Engine: "claude-code", Model: "haiku"}) {
		t.Fatalf("a complete policy changed: %+v", complete)
	}
}

func TestTokenOptimizerZeroIsOmitted(t *testing.T) {
	b, err := json.Marshal(AgentType{Name: "dev", TokenOptimizer: TokenOptimizer{Level: "off"}.Normalize()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "token_optimizer") {
		t.Fatalf("zero policy serialized: %s", b)
	}
	b, err = json.Marshal(AgentType{Name: "dev", TokenOptimizer: TokenOptimizer{Level: "suggest", Engine: "claude-code", Model: "haiku"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"token_optimizer":{"level":"suggest","engine":"claude-code","model":"haiku"}`) {
		t.Fatalf("policy missing: %s", b)
	}
}

func TestStageSkillsHonorsWhen(t *testing.T) {
	at := AgentType{
		SkillsByStage: map[string][]string{"dev": {"notes", "octopus-token-optimizer"}},
		SystemSkills: []SystemSkillRef{
			{Name: "octopus-run"},
			{Name: "octopus-token-optimizer", When: SkillWhenTokenOptimizer},
		},
	}
	if got, want := at.StageSkills("dev"), []string{"notes", "octopus-run"}; !slices.Equal(got, want) {
		t.Fatalf("condition off: %v, want %v", got, want)
	}
	at.ActiveWhen = []string{SkillWhenTokenOptimizer}
	if got, want := at.StageSkills("dev"), []string{"notes", "octopus-token-optimizer", "octopus-run"}; !slices.Equal(got, want) {
		t.Fatalf("condition on: %v, want %v", got, want)
	}
	at.SystemSkillsOff = map[string][]string{"dev": {"octopus-token-optimizer"}}
	if got, want := at.StageSkills("dev"), []string{"notes", "octopus-run"}; !slices.Equal(got, want) {
		t.Fatalf("turned off still wins: %v, want %v", got, want)
	}
}
