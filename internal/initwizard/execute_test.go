package initwizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevOverrideReplacesPortsInsteadOfAppending(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	path, err := WriteDevOverride(root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "ports: !override") != 2 {
		t.Errorf("both services must replace the base port list, not extend it:\n%s", text)
	}
	if strings.Contains(text, "\"8585:8585\"") {
		t.Errorf("override publishes on every interface:\n%s", text)
	}
	if !strings.Contains(text, "127.0.0.1:8585:8585") {
		t.Errorf("override does not narrow the dashboard to loopback:\n%s", text)
	}
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}
