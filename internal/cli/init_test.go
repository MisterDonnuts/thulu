package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRootMaterializesIntoAnEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer

	root, err := prepareRoot(dir, false, &out)
	if err != nil {
		t.Fatalf("prepareRoot: %v", err)
	}
	if root != dir {
		t.Fatalf("root = %q, want %q", root, dir)
	}
	for _, name := range []string{
		filepath.Join("deploy", "docker-compose.standalone.yml"),
		filepath.Join("deploy", ".env.example"),
	} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Errorf("%s missing after prepareRoot: %v", name, statErr)
		}
	}
	if hasSource(dir) {
		t.Error("a materialized directory reports having source; the wizard would offer stacks that cannot build")
	}
}

func TestPrepareRootWritesNothingOnADryRun(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if _, err := prepareRoot(dir, true, &out); err != nil {
		t.Fatalf("prepareRoot: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a dry run wrote %d entries, the first %q", len(entries), entries[0].Name())
	}

	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("OCTOPUS_HOME", home)
	t.Chdir(t.TempDir())
	root, err := prepareRoot("", true, &out)
	if err != nil {
		t.Fatalf("prepareRoot: %v", err)
	}
	if root != home {
		t.Errorf("root = %q, want %q", root, home)
	}
	if _, statErr := os.Stat(home); statErr == nil {
		t.Error("a dry run created the install home")
	}
}

func TestPrepareRootLeavesACheckoutAlone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module x\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "deploy", "docker-compose.yml"), "services: {}\n")

	sub := filepath.Join(dir, "internal", "somewhere")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	var out bytes.Buffer
	root, err := prepareRoot("", false, &out)
	if err != nil {
		t.Fatalf("prepareRoot: %v", err)
	}
	// t.TempDir can be a symlink on macOS (/var -> /private/var); compare files, not path strings.
	if !sameDir(t, root, dir) {
		t.Fatalf("root = %q, want the checkout at %q", root, dir)
	}
	if !hasSource(root) {
		t.Error("a checkout reports no source")
	}
	body, err := os.ReadFile(filepath.Join(dir, "deploy", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "services: {}\n" {
		t.Errorf("the checkout's compose file was overwritten: %q", body)
	}
}

func TestResolveRootFallsBackToTheInstallHome(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "deploy", "docker-compose.yml"), "services: {}\n")
	t.Setenv("OCTOPUS_HOME", home)
	t.Chdir(t.TempDir())

	root, err := resolveRoot("")
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	if !sameDir(t, root, home) {
		t.Errorf("root = %q, want %q", root, home)
	}
}

func TestResolveRootSaysWhatToRunWhenThereIsNoInstall(t *testing.T) {
	t.Setenv("OCTOPUS_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	_, err := resolveRoot("")
	if err == nil {
		t.Fatal("resolveRoot found an install in an empty tree")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("thulu init")) {
		t.Errorf("the error does not say how to make one: %v", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}
