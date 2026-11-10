package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// Reads tools/*/server.js TOOLS and module-level *_TOOLS tables against each tool.yaml; the regexes
// are formatting-sensitive on purpose so a reshaped table fails here.
var (
	toolsTableRe = regexp.MustCompile(`(?m)^(export )?const ([A-Z]+_)?TOOLS = \[`)
	toolNameRe   = regexp.MustCompile(`(?m)^    name: "([a-z0-9_]+)",`)
	validScopes  = map[string]bool{"read": true, "write": true, "deploy": true, "board": true}
)

func TestToolYAMLDescribesEveryToolTheServerServes(t *testing.T) {
	root := filepath.Join("..", "..", "tools")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("no tools/ directory here: %v", err)
	}
	checked := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "server.js")); err != nil {
			continue
		}
		matches := servedTools(t, dir)
		if len(matches) == 0 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "tool.yaml"))
		if err != nil {
			t.Errorf("%s serves %d tools but ships no tool.yaml", entry.Name(), len(matches))
			continue
		}
		var doc toolYaml
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s/tool.yaml: %v", entry.Name(), err)
			continue
		}
		checked++

		described := map[string]string{}
		for _, tool := range doc.Tools {
			described[tool.Name] = tool.Scope
		}
		var missing []string
		for _, name := range matches {
			if _, ok := described[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s/tool.yaml does not describe %v — the picker cannot list what it cannot read",
				entry.Name(), missing)
		}

		var scoped int
		for name, scope := range described {
			if scope == "" {
				continue
			}
			scoped++
			if !validScopes[scope] {
				t.Errorf("%s/%s: unknown scope %q", entry.Name(), name, scope)
			}
		}
		if scoped > 0 && doc.DefaultScope == "" {
			t.Errorf("%s/tool.yaml declares per-tool scopes but no default_scope", entry.Name())
		}
	}
	if checked == 0 {
		t.Skip("no server ships a declarative TOOLS table here")
	}
}

func servedTools(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "test") {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !toolsTableRe.Match(src) {
			return nil
		}
		for _, m := range toolNameRe.FindAllSubmatch(src, -1) {
			names = append(names, string(m[1]))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	return names
}
