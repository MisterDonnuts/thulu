package initwizard

import (
	"fmt"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func Diff(before, after string) string {
	oldLines := assignments(before)
	newLines := assignments(after)

	var b strings.Builder
	keys := make([]string, 0, len(newLines))
	seen := map[string]bool{}
	for _, k := range orderedKeys(after) {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	for _, k := range orderedKeys(before) {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}

	for _, k := range keys {
		oldV, hadOld := oldLines[k]
		newV, hasNew := newLines[k]
		switch {
		case hasNew && !hadOld:
			fmt.Fprintf(&b, "  + %s=%s\n", k, mask(k, newV))
		case hasNew && hadOld && oldV != newV:
			fmt.Fprintf(&b, "  ~ %s=%s → %s\n", k, mask(k, oldV), mask(k, newV))
		case !hasNew && hadOld:
			fmt.Fprintf(&b, "  - %s\n", i18n.Text("initwizard.diff.commented_out", "key", k))
		}
	}
	if b.Len() == 0 {
		return "  " + i18n.Text("initwizard.diff.no_changes") + "\n"
	}
	return b.String()
}

func mask(key, value string) string {
	if value == "" {
		return i18n.Text("initwizard.diff.empty")
	}
	if !isSecretKey(key) {
		return value
	}
	if len(value) <= 6 {
		return "••••"
	}
	return value[:3] + "…" + strings.Repeat("•", 4)
}

func isSecretKey(key string) bool {
	for _, needle := range []string{"SECRET", "TOKEN", "PASSWORD", "KEY"} {
		if strings.Contains(key, needle) {
			return key != "OCTOPUS_LICENSE_KEY"
		}
	}
	return strings.Contains(key, "DATABASE_URL")
}

func assignments(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := parseAssignment(line); ok {
			out[k] = v
		}
	}
	return out
}

func orderedKeys(text string) []string {
	var keys []string
	for _, line := range strings.Split(text, "\n") {
		if k, _, ok := parseAssignment(line); ok {
			keys = append(keys, k)
		}
	}
	return keys
}
