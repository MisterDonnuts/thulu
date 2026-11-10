package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
)

func readFixture(t *testing.T, eng, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", eng, name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestResultOutcome(t *testing.T) {
	maxTurns := readFixture(t, "claude-code", "max_turns")
	codexErr := readFixture(t, "codex", "error")
	opencodeOK := readFixture(t, "opencode", "ok")
	opencodeErr := readFixture(t, "opencode", "error")

	cases := []struct {
		name      string
		stream    []byte
		preferred string
		want      string
	}{
		{"own engine", maxTurns, "claude-code", engine.OutcomeMaxTurns},
		{"default engine", maxTurns, "", engine.OutcomeMaxTurns},
		{"routed elsewhere", codexErr, "claude-code", engine.OutcomeError},
		{"opencode", opencodeOK, "opencode", engine.OutcomeOK},
		{"opencode routed elsewhere", opencodeOK, "claude-code", engine.OutcomeOK},
		{"opencode error routed elsewhere", opencodeErr, "claude-code", engine.OutcomeError},
		{"codex run believed opencode", codexErr, "opencode", engine.OutcomeError},
		{"claude run believed opencode", maxTurns, "opencode", engine.OutcomeMaxTurns},
		{"unknown preferred", maxTurns, "no-such-engine", engine.OutcomeMaxTurns},
		{"tail", maxTurns[len(maxTurns)-600:], "claude-code", engine.OutcomeMaxTurns},
		{"no result", []byte("Engine: claude-code\nexit 1\n"), "claude-code", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := engine.ResultOutcome(tc.stream, tc.preferred); got != tc.want {
				t.Errorf("ResultOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPriceFor(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"sonnet", "sonnet"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-sonnet-4-5-20250929", "sonnet"},
		{"gpt-4o", "gpt-4o"},
		{"gpt-4o-mini", "gpt-4o-mini"},
		{"gpt-5-codex", ""},
		{"", ""},
	}
	for _, tc := range cases {
		p, ok := engine.PriceFor(tc.model)
		if ok != (tc.want != "") || p.Model != tc.want {
			t.Errorf("PriceFor(%q) = %q, %v; want %q", tc.model, p.Model, ok, tc.want)
		}
	}
}

func TestPricesKeepsClaudeList(t *testing.T) {
	want := map[string][4]float64{
		"claude-opus-4-7":           {15.0, 75.0, 1.50, 18.75},
		"claude-sonnet-4-6":         {3.0, 15.0, 0.30, 3.75},
		"claude-haiku-4-5-20251001": {1.0, 5.0, 0.10, 1.25},
		"opus":                      {15.0, 75.0, 1.50, 18.75},
		"sonnet":                    {3.0, 15.0, 0.30, 3.75},
		"haiku":                     {1.0, 5.0, 0.10, 1.25},
	}
	got := map[string][4]float64{}
	for _, p := range engine.Prices() {
		got[p.Model] = [4]float64{p.InPerMTok, p.OutPerMTok, p.CacheReadPerMTok, p.CacheCreatePerMTok}
	}
	for model, w := range want {
		if got[model] != w {
			t.Errorf("%s = %v, want %v", model, got[model], w)
		}
	}
}
