package engine

import (
	"strings"
	"unicode/utf8"
)

var toolHintKeys = []string{
	"command", "cmd", "file_path", "filePath", "path", "pattern", "query", "url", "description", "prompt",
}

func ToolHint(input map[string]any, maxRunes int) string {
	hint := ""
	for _, k := range toolHintKeys {
		if v, ok := input[k].(string); ok && strings.TrimSpace(v) != "" {
			hint = v
			break
		}
	}
	if hint == "" {
		return ""
	}
	hint = strings.Join(strings.Fields(hint), " ")
	if maxRunes > 0 && utf8.RuneCountInString(hint) > maxRunes {
		hint = string([]rune(hint)[:maxRunes]) + "…"
	}
	return hint
}
