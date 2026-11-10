package types

import (
	"slices"
	"testing"

	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
)

func TestModelParamFollowsEngine(t *testing.T) {
	cases := []struct {
		engine, wantDefault, wantOption string
	}{
		{"", "sonnet", "opus"},
		{"claude-code", "sonnet", "haiku"},
		{"codex", "gpt-5.4-mini", "gpt-5.5"},
	}
	for _, tc := range cases {
		p := modelParam(tc.engine)
		if p.Type != "select" || p.Default != tc.wantDefault || !slices.Contains(p.Options, tc.wantOption) {
			t.Errorf("modelParam(%q) = %+v, want select defaulting to %q offering %q", tc.engine, p, tc.wantDefault, tc.wantOption)
		}
	}
	if slices.Contains(modelParam("codex").Options, "sonnet") {
		t.Error("codex MODEL offers sonnet")
	}
	if p := modelParam("no-such-engine"); p.Type != "text" || len(p.Options) != 0 {
		t.Errorf("unknown engine = %+v, want a free-text field", p)
	}
}

func TestParseServiceParamsModelPerEngine(t *testing.T) {
	params := parseServiceParams([]string{"MODEL=", "MAX_TURNS=20", "HOME=/home/node"}, "codex")
	if len(params) != 2 {
		t.Fatalf("params = %+v, want MODEL and MAX_TURNS", params)
	}
	if params[0].Name != "MODEL" || params[0].Default != "gpt-5.4-mini" {
		t.Errorf("MODEL = %+v, want codex's default", params[0])
	}
}
