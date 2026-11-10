package tools

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeCoversEveryServerDirectory(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read tools/: %v", err)
	}
	servers := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, file := range []string{"tool.yaml", "skill.md"} {
			onDisk, readErr := os.ReadFile(filepath.Join(e.Name(), file))
			if readErr != nil {
				continue
			}
			if file == "tool.yaml" {
				servers++
			}
			embedded, embErr := fs.ReadFile(Native(), e.Name()+"/"+file)
			if embErr != nil {
				t.Errorf("%s/%s exists but is not embedded: add it to embed.go", e.Name(), file)
				continue
			}
			if !bytes.Equal(onDisk, embedded) {
				t.Errorf("%s/%s: embedded copy differs from the file", e.Name(), file)
			}
		}
	}
	if servers == 0 {
		t.Fatal("found no server directories; the test is not looking at tools/")
	}
}
