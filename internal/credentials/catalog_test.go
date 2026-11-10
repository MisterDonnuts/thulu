package credentials

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCatalog_LookupAndProviders(t *testing.T) {
	c := NewCatalog()

	spec, ok := c.Lookup("thulu-cloud", "logs_search")
	if !ok {
		t.Fatal("logs_search is not catalogued")
	}
	if got := spec.Providers(); strings.Join(got, ",") != "aws,gcp" {
		t.Errorf("providers = %v, want [aws gcp]", got)
	}
	if _, ok := c.Lookup("thulu-browser", "screenshot"); ok {
		t.Error("an uncatalogued tool reported as credential-backed")
	}
}

func TestCatalog_PermissionsAreNamespacedAndDeduplicated(t *testing.T) {
	perms := NewCatalog().Permissions()
	if len(perms) == 0 {
		t.Fatal("catalog declares no permissions")
	}
	seen := map[string]bool{}
	for _, p := range perms {
		if seen[p] {
			t.Errorf("duplicate permission %q", p)
		}
		seen[p] = true
		parts := strings.SplitN(p, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			t.Errorf("permission %q is not <provider>:<permission>", p)
		}
	}
	if !sort.StringsAreSorted(perms) {
		t.Error("Permissions() is not sorted — callers rely on a stable order")
	}
}

func TestCatalog_RequiredPermissionsMatchTheirProvider(t *testing.T) {
	for _, spec := range NewCatalog().All() {
		for provider, perms := range spec.Requires {
			for _, p := range perms {
				if !strings.HasPrefix(p, provider+":") {
					t.Errorf("%s/%s requires %q under provider %q",
						spec.Server, spec.Tool, p, provider)
				}
			}
		}
	}
}

func TestCatalog_AgreesWithToolYAML(t *testing.T) {
	root := filepath.Join("..", "..", "tools")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("no tools/ directory here: %v", err)
	}
	catalog := NewCatalog()
	checked := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(root, entry.Name(), "tool.yaml"))
		if readErr != nil {
			continue
		}
		var doc struct {
			Name  string `yaml:"name"`
			Tools []struct {
				Name     string              `yaml:"name"`
				Requires map[string][]string `yaml:"requires"`
			} `yaml:"tools"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s/tool.yaml: %v", entry.Name(), err)
			continue
		}
		for _, tool := range doc.Tools {
			spec, ok := catalog.Lookup(doc.Name, tool.Name)
			if len(tool.Requires) == 0 {
				if ok {
					t.Errorf("%s/%s is in the Go catalog but declares no `requires` in tool.yaml",
						doc.Name, tool.Name)
				}
				continue
			}
			checked++
			if !ok {
				t.Errorf("%s/%s declares `requires` in tool.yaml but is missing from the Go catalog",
					doc.Name, tool.Name)
				continue
			}
			if !sameRequires(spec.Requires, tool.Requires) {
				t.Errorf("%s/%s: catalog has %v, tool.yaml has %v",
					doc.Name, tool.Name, spec.Requires, tool.Requires)
			}
		}
	}
	if checked == 0 {
		t.Skip("no tool.yaml declared any requirements — nothing to cross-check")
	}
}

func sameRequires(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for provider, wantPerms := range a {
		gotPerms, ok := b[provider]
		if !ok || len(gotPerms) != len(wantPerms) {
			return false
		}
		want := append([]string(nil), wantPerms...)
		got := append([]string(nil), gotPerms...)
		sort.Strings(want)
		sort.Strings(got)
		for i := range want {
			if want[i] != got[i] {
				return false
			}
		}
	}
	return true
}
