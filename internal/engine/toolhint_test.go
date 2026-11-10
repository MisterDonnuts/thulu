package engine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolHint(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		max   int
		want  string
	}{
		{"command wins over path", map[string]any{"command": "npm  test\n--run", "file_path": "a.ts"}, 0, "npm test --run"},
		{"path when there is no command", map[string]any{"file_path": "src/app.ts"}, 0, "src/app.ts"},
		{"OpenCode's filePath", map[string]any{"filePath": "/workspace/project/go.mod"}, 0, "/workspace/project/go.mod"},
		{"blank fields are skipped", map[string]any{"command": "  ", "pattern": "TODO"}, 0, "TODO"},
		{"nothing recognizable", map[string]any{"limit": 3}, 0, ""},
		{"nil input", nil, 0, ""},
		{"cut with an ellipsis", map[string]any{"command": "abcdef"}, 3, "abc…"},
	}
	for _, c := range cases {
		if got := ToolHint(c.input, c.max); got != c.want {
			t.Errorf("%s: ToolHint = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestToolHint_CutsOnRunes(t *testing.T) {
	got := ToolHint(map[string]any{"description": strings.Repeat("ñ", 10)}, 4)
	if !utf8.ValidString(got) || got != "ññññ…" {
		t.Errorf("ToolHint = %q, want four runes and an ellipsis", got)
	}
}
