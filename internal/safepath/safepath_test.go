package safepath

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadFileWithinDir(t *testing.T) {
	dir := t.TempDir()
	want := []byte("hello")
	if err := os.WriteFile(filepath.Join(dir, "note.md"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(dir, "note.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReadFileLeadingSlashIsRelative(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir, "/main.md"); err != nil {
		t.Fatalf("leading slash should resolve relative to dir: %v", err)
	}
}

func TestReadFileNested(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "stages"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stages", "plan.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir, "stages/plan.md"); err != nil {
		t.Fatalf("nested read: %v", err)
	}
}

func TestReadFileRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Dir(dir)
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir, "../secret.txt"); err == nil {
		t.Fatal("expected traversal to be refused")
	}
}

func TestReadFileRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir, "link.txt"); err == nil {
		t.Fatal("expected symlink escape to be refused")
	}
}

func TestWriteAndCreateWithinDir(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(dir, "sub/w.txt", []byte("x"), 0o600); err == nil {
		t.Fatal("expected WriteFile to fail when parent dir is missing")
	}
	if err := MkdirAll(dir, "sub", 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := WriteFile(dir, "sub/w.txt", []byte("hi"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadFile(dir, "sub/w.txt")
	if err != nil || string(got) != "hi" {
		t.Fatalf("readback: %q err=%v", got, err)
	}

	f, err := Create(dir, "c.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.WriteString("created"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got, _ := ReadFile(dir, "c.txt"); string(got) != "created" {
		t.Fatalf("create readback: %q", got)
	}
}

func TestWriteFileRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(dir, "../escape.txt", []byte("x"), 0o600); err == nil {
		t.Fatal("expected traversal write to be refused")
	}
}

func TestRel(t *testing.T) {
	base := filepath.FromSlash("/data/attachments")
	rel, err := Rel(base, filepath.FromSlash("/data/attachments/task1/file.png"))
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if rel != filepath.FromSlash("task1/file.png") {
		t.Fatalf("Rel = %q", rel)
	}
	if _, err := Rel(base, filepath.FromSlash("/data/other/x")); err == nil {
		t.Fatal("expected Rel to reject a path outside base")
	}
}

func TestStatWithinDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := Stat(dir, "f")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.IsDir() {
		t.Fatal("expected a file")
	}
}

func TestRemoveAllWithinDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "task-1", "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task-1", "nested", "a.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll(dir, "task-1"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "task-1")); !os.IsNotExist(err) {
		t.Fatalf("task-1 should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatalf("sibling must survive: %v", err)
	}
	if err := RemoveAll(dir, "task-1"); err != nil {
		t.Fatalf("second RemoveAll: %v", err)
	}
	if err := RemoveAll(filepath.Join(dir, "missing"), "x"); err != nil {
		t.Fatalf("missing base: %v", err)
	}
}

func TestRemoveAllRefusesBaseAndTraversal(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "/", "a/..", "../outside.txt"} {
		if err := RemoveAll(base, name); err == nil {
			t.Errorf("RemoveAll(%q) should be refused", name)
		}
	}
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("base must survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.txt")); err != nil {
		t.Fatalf("file outside base must survive: %v", err)
	}
}
