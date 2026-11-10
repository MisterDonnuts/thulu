//go:build unix

package fsx

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestWriteFileModeIgnoresTheUmask(t *testing.T) {
	for _, tc := range []struct {
		umask int
		perm  fs.FileMode
	}{{0, 0o600}, {0o077, 0o644}, {0o022, 0o755}} {
		old := syscall.Umask(tc.umask)
		path := filepath.Join(t.TempDir(), "f")
		err := WriteFile(path, []byte("x"), tc.perm)
		syscall.Umask(old)
		if err != nil {
			t.Fatal(err)
		}
		if got := mode(t, path); got != tc.perm {
			t.Errorf("umask %03o, perm %v: file is %v", tc.umask, tc.perm, got)
		}
	}
}

func TestWriteFileReplacesALooserFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, path); got != 0o600 {
		t.Errorf("mode %v, want 0600", got)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Errorf("body %q, %v", b, err)
	}
	assertOnly(t, filepath.Dir(path), ".env")
}

func TestWriteFileLeavesNoTempOnError(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "taken")
	if err := os.MkdirAll(filepath.Join(target, "inside"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("x"), 0o600); err == nil {
		t.Fatal("WriteFile replaced a non-empty directory")
	}
	assertOnly(t, dir, "taken")
	if _, err := os.Stat(filepath.Join(target, "inside")); err != nil {
		t.Errorf("the directory lost its contents: %v", err)
	}
}

func TestWriteFileIntoAReadOnlyDirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	})
	if err := WriteFile(path, []byte("new"), 0o600); err == nil {
		t.Fatal("WriteFile wrote into a read-only directory")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "old" {
		t.Errorf("body %q, %v; want the old one untouched", b, err)
	}
	assertOnly(t, dir, "f")
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, names) {
		t.Errorf("%s holds %v, want %v", dir, got, names)
	}
}
