package automation

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestShippedTemplatesParse(t *testing.T) {
	const path = "../../web/src/lib/automation-templates.ts"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	re := regexp.MustCompile("(?s)spec: `(.*?)`,\n")
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 5 {
		t.Fatalf("found %d template specs, expected at least 5 — did the file's shape change?", len(matches))
	}
	for _, m := range matches {
		spec := strings.ReplaceAll(m[1], "\\`", "`")
		parsed, err := ParseSpec(spec)
		if err != nil {
			t.Errorf("template %q does not parse: %v", firstLine(spec), err)
			continue
		}
		if parsed.Name == "" {
			t.Errorf("template %q has no name", firstLine(spec))
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
