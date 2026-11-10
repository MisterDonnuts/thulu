package i18n

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Retranslator struct {
	catalog  *Catalog
	matchers []templateMatcher
	values   ValueFunc
}

type ValueFunc func(locale, key string, vals map[string]any)

func (rt *Retranslator) WithValues(fn ValueFunc) *Retranslator {
	out := *rt
	out.values = fn
	return &out
}

// Keeps rows stored before a template changed readable. Mutates rt: call only while building,
// before rt is shared.
func (rt *Retranslator) WithLegacy(legacy map[string]string) *Retranslator {
	olds := make([]string, 0, len(legacy))
	for old := range legacy {
		olds = append(olds, old)
	}
	sort.Strings(olds)
	for _, locale := range rt.catalog.Locales() {
		for _, old := range olds {
			for _, tmpl := range rt.catalog.Templates(locale, old) {
				if m, ok := compileTemplate(legacy[old], locale, tmpl); ok {
					m.legacy = true
					rt.matchers = append(rt.matchers, m)
				}
			}
		}
	}
	rt.sortMatchers()
	return rt
}

type templateMatcher struct {
	key, locale string
	re          *regexp.Regexp
	prefix      string
	vars, verbs []string
	literal     int
	legacy      bool
}

// Templates with less fixed text, like "{reason} (row {id})", would match text from anywhere.
const minLiteral = 8

func NewRetranslator(c *Catalog, keep func(key string) bool) *Retranslator {
	rt := &Retranslator{catalog: c}
	for _, locale := range c.Locales() {
		for _, key := range c.Keys(locale) {
			if !keep(key) {
				continue
			}
			for _, tmpl := range c.Templates(locale, key) {
				if m, ok := compileTemplate(key, locale, tmpl); ok {
					rt.matchers = append(rt.matchers, m)
				}
			}
		}
	}
	rt.sortMatchers()
	return rt
}

func (rt *Retranslator) sortMatchers() {
	sort.SliceStable(rt.matchers, func(i, j int) bool {
		a, b := &rt.matchers[i], &rt.matchers[j]
		if a.literal != b.literal {
			return a.literal > b.literal
		}
		return !a.legacy && b.legacy
	})
}

func compileTemplate(key, locale, tmpl string) (templateMatcher, bool) {
	m := templateMatcher{key: key, locale: locale}
	var pattern strings.Builder
	pattern.WriteString(`(?s)^`)
	last := 0
	for _, loc := range placeholder.FindAllStringSubmatchIndex(tmpl, -1) {
		lit := tmpl[last:loc[0]]
		if len(m.vars) == 0 {
			m.prefix = lit
		}
		m.literal += len(lit)
		pattern.WriteString(regexp.QuoteMeta(lit))
		verb := ""
		if loc[4] >= 0 {
			verb = tmpl[loc[4]:loc[5]]
		}
		switch verb {
		case "", "%s", "%v", "%q", "%d":
		default:
			// %.2f and similar verbs lose the value's exact form, so it cannot be read back.
			return m, false
		}
		pattern.WriteString(`(.+?)`)
		m.vars = append(m.vars, tmpl[loc[2]:loc[3]])
		m.verbs = append(m.verbs, verb)
		last = loc[1]
	}
	tail := tmpl[last:]
	if len(m.vars) == 0 {
		m.prefix = tail
	}
	m.literal += len(tail)
	pattern.WriteString(regexp.QuoteMeta(tail))
	pattern.WriteString(`$`)
	if m.literal < minLiteral {
		return m, false
	}
	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return m, false
	}
	m.re = re
	return m, true
}

func (rt *Retranslator) Translate(text, locale string) (out string, ok bool) {
	if rt == nil || text == "" {
		return text, false
	}
	for i := range rt.matchers {
		m := &rt.matchers[i]
		if !strings.HasPrefix(text, m.prefix) {
			continue
		}
		sub := m.re.FindStringSubmatch(text)
		if sub == nil {
			continue
		}
		if m.locale == locale && rt.values == nil && !m.legacy {
			return text, true
		}
		args, readable := m.args(sub[1:])
		if !readable {
			continue
		}
		if rt.values != nil {
			rt.relabel(locale, m.key, args)
		}
		return rt.catalog.Render(locale, m.key, args...), true
	}
	return text, false
}

func (rt *Retranslator) relabel(locale, key string, args []any) {
	vals := make(map[string]any, len(args)/2)
	for j := 0; j+1 < len(args); j += 2 {
		if name, ok := args[j].(string); ok {
			vals[name] = args[j+1]
		}
	}
	rt.values(locale, key, vals)
	for j := 0; j+1 < len(args); j += 2 {
		if name, ok := args[j].(string); ok {
			args[j+1] = vals[name]
		}
	}
}

func (m *templateMatcher) args(values []string) ([]any, bool) {
	seen := map[string]bool{}
	var out []any
	for i, raw := range values {
		name := m.vars[i]
		if seen[name] {
			continue
		}
		seen[name] = true
		var v any = raw
		switch {
		case m.verbs[i] == "%q":
			s, err := strconv.Unquote(raw)
			if err != nil {
				return nil, false
			}
			v = s
		case m.verbs[i] == "%d" || name == "count":
			n, err := strconv.Atoi(raw)
			if err != nil {
				return nil, false
			}
			v = n
		}
		out = append(out, name, v)
	}
	return out, true
}
