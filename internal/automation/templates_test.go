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
	if len(matches) < 7 {
		t.Fatalf("found %d template specs, expected at least 7 — did the file's shape change?", len(matches))
	}
	events := map[string]bool{}
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
		// Save runs the same check: a filter must read only fields its event publishes.
		if err := CheckFilterFields(&parsed.Trigger); err != nil {
			t.Errorf("template %q filter: %v", firstLine(spec), err)
		}
		events[parsed.Trigger.Event] = true
	}
	for _, e := range []string{EventDeployment, EventWorkflowRun} {
		if !events[e] {
			t.Errorf("no shipped template listens to %s", e)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
