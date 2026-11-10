package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

var pluralCategories = map[string]bool{
	"zero": true, "one": true, "two": true, "few": true, "many": true, "other": true,
}

type message struct {
	text   string
	plural map[string]string
}

type Catalog struct {
	locales map[string]map[string]message
}

func Load(fsys fs.FS) (*Catalog, error) {
	c := &Catalog{locales: map[string]map[string]message{}}
	dirs, err := fs.ReadDir(fsys, "locales")
	if err != nil {
		return nil, fmt.Errorf("i18n: read locales: %w", err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		msgs := map[string]message{}
		dir := path.Join("locales", d.Name())
		err := fs.WalkDir(fsys, dir, func(file string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || path.Ext(file) != ".json" {
				return err
			}
			raw, err := fs.ReadFile(fsys, file)
			if err != nil {
				return fmt.Errorf("i18n: read %s: %w", file, err)
			}
			var tree map[string]any
			if err := json.Unmarshal(raw, &tree); err != nil {
				return fmt.Errorf("i18n: parse %s: %w", file, err)
			}
			rel := strings.TrimSuffix(strings.TrimPrefix(file, dir+"/"), ".json")
			if err := flatten(strings.ReplaceAll(rel, "/", "."), tree, msgs); err != nil {
				return fmt.Errorf("i18n: %s: %w", file, err)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("i18n: read %s: %w", dir, err)
		}
		c.locales[d.Name()] = msgs
	}
	if _, ok := c.locales[SourceLocale]; !ok {
		return nil, fmt.Errorf("i18n: the source locale %q has no catalog", SourceLocale)
	}
	return c, nil
}

func flatten(prefix string, node map[string]any, out map[string]message) error {
	for k, v := range node {
		key := prefix + "." + k
		if _, dup := out[key]; dup {
			return fmt.Errorf("%s is defined twice", key)
		}
		switch val := v.(type) {
		case string:
			out[key] = message{text: val}
		case map[string]any:
			if isPlural(val) {
				out[key] = message{plural: pluralForms(val)}
				continue
			}
			if err := flatten(key, val, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: a message is a string or an object, got %T", key, v)
		}
	}
	return nil
}

func pluralForms(val map[string]any) map[string]string {
	forms := make(map[string]string, len(val))
	for cat, t := range val {
		if text, ok := t.(string); ok {
			forms[cat] = text
		}
	}
	return forms
}

func isPlural(m map[string]any) bool {
	if _, ok := m["other"]; !ok {
		return false
	}
	for k, v := range m {
		if _, isString := v.(string); !pluralCategories[k] || !isString {
			return false
		}
	}
	return true
}

func (c *Catalog) Locales() []string {
	out := make([]string, 0, len(c.locales))
	for l := range c.locales {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) Keys(locale string) []string {
	msgs := c.locales[locale]
	out := make([]string, 0, len(msgs))
	for k := range msgs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) Has(locale, key string) bool {
	_, ok := c.locales[locale][key]
	return ok
}

func (c *Catalog) Templates(locale, key string) []string {
	m, ok := c.locales[locale][key]
	if !ok {
		return nil
	}
	if m.plural == nil {
		return []string{m.text}
	}
	out := make([]string, 0, len(m.plural))
	for _, t := range m.plural {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) Render(locale, key string, args ...any) string {
	m, ok := c.locales[locale][key]
	if !ok {
		if m, ok = c.locales[SourceLocale][key]; !ok {
			return key
		}
		locale = SourceLocale
	}
	vals := pairs(args)
	tmpl := m.text
	if m.plural != nil {
		tmpl = m.plural[pluralForm(locale, vals["count"])]
		if tmpl == "" {
			tmpl = m.plural["other"]
		}
	}
	return interpolate(tmpl, vals)
}

func pairs(args []any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok {
			out[k] = args[i+1]
		}
	}
	return out
}
