package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishesPostgres(t *testing.T) {
	repo := filepath.Join("..", "..")
	for _, file := range []string{
		"deploy/docker-compose.dev.yml",
		"deploy/docker-compose.standalone.yml",
		"deploy/docker-compose.yml",
		"deploy/docker-compose.daemon.yml",
	} {
		in := &Install{Root: repo, ComposeFile: file}
		if !in.PublishesPostgres() {
			t.Errorf("%s: PublishesPostgres = false, but the stack publishes 127.0.0.1:5433", file)
		}
	}

	root := t.TempDir()
	private := "services:\n  pg:\n    image: postgres:16-alpine\n    expose: [\"5432\"]\n"
	if err := os.WriteFile(filepath.Join(root, "private.yml"), []byte(private), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"private.yml", "missing.yml", ""} {
		if (&Install{Root: root, ComposeFile: file}).PublishesPostgres() {
			t.Errorf("%q: PublishesPostgres = true", file)
		}
	}
}

func TestHasSource(t *testing.T) {
	release := t.TempDir()
	if err := os.MkdirAll(filepath.Join(release, "deploy"), 0o750); err != nil {
		t.Fatal(err)
	}
	if (&Install{Root: release}).HasSource() {
		t.Error("a materialized install claims to have source to build from")
	}

	checkout := t.TempDir()
	for _, name := range []string{"go.mod", "main.go"} {
		if err := os.WriteFile(filepath.Join(checkout, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !(&Install{Root: checkout}).HasSource() {
		t.Error("a checkout claims to have no source")
	}
}
