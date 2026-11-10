package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListWithoutAComposeFileIsEmptyNotAnError(t *testing.T) {
	m := NewManager(nil, filepath.Join(t.TempDir(), "tools", "docker-compose.yml"))

	got, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("a missing compose file is not an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no services, got %d", len(got))
	}
}

func TestListStillFailsOnAComposeFileThatDoesNotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte("services: [this is not a mapping\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, path)

	_, err := m.List(context.Background())
	if err == nil {
		t.Fatal("a malformed compose file must still fail")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error should name the file it could not read: %v", err)
	}
}
