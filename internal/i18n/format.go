package i18n

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)(?::(%[^}]+))?\}`)

func interpolate(tmpl string, vals map[string]any) string {
	if !strings.Contains(tmpl, "{") {
		return tmpl
	}
	return placeholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		v, ok := vals[sub[1]]
		if !ok {
			return m
		}
		if sub[2] != "" {
			return fmt.Sprintf(sub[2], v)
		}
		return fmt.Sprint(v)
	})
}

func Placeholders(tmpl string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(tmpl, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// English and Spanish share the one/other rule; a locale with other CLDR plural rules needs its own
// case here.
func pluralForm(_ string, count any) string {
	switch n := count.(type) {
	case int:
		return oneOrOther(n == 1)
	case int32:
		return oneOrOther(n == 1)
	case int64:
		return oneOrOther(n == 1)
	case uint:
		return oneOrOther(n == 1)
	case uint32:
		return oneOrOther(n == 1)
	case uint64:
		return oneOrOther(n == 1)
	case float64:
		return oneOrOther(n == 1)
	}
	return "other"
}

func oneOrOther(one bool) string {
	if one {
		return "one"
	}
	return "other"
}
