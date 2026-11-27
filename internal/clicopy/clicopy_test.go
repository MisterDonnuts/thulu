package clicopy

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

func TestReplaceWritesANewFileOverTheOld(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "thulu-new")
	dst := filepath.Join(dir, "thulu")
	if err := os.WriteFile(src, []byte("new binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A process running the old file holds it open; it must keep reading the old bytes.
	running, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	before := inode(t, dst)

	if err := Replace(src, dst); err != nil {
		t.Fatal(err)
	}
	if after := inode(t, dst); after == before {
		t.Fatal("Replace wrote into the old inode: macOS kills a signed binary changed in place")
	}
	if b, _ := os.ReadFile(dst); string(b) != "new binary" {
		t.Fatalf("dst = %q", b)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if old, _ := io.ReadAll(running); string(old) != "old binary" {
		t.Fatalf("the running process sees %q", old)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("Replace left files behind: %v", entries)
	}
}

func TestReplaceReportsAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "thulu")
	src := filepath.Join(t.TempDir(), "new")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := Replace(src, dst)
	if err == nil || !NoPermission(err) {
		t.Fatalf("err = %v, want a permission error", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Fatalf("dst changed: %q", b)
	}
}

// The command printed for a directory that needs sudo does the same replace: run without sudo on a
// writable directory, it must leave dst holding src, even with quotes and spaces in the paths.
func TestManualCommandReplacesTheFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "new thulu")
	dst := filepath.Join(dir, "thulu")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := ManualCommand(src, dst)
	if !strings.HasPrefix(cmd, "sudo sh -c ") {
		t.Fatalf("command = %s", cmd)
	}
	if out, err := exec.Command("sh", "-c", strings.TrimPrefix(cmd, "sudo ")).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("dst = %q", b)
	}
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Fatal("the command left dst.new behind")
	}
}

func TestIsThuluReadsTheBuildInfo(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !IsThulu(self) {
		t.Fatal("the test binary of this module is a Thulu binary")
	}
	script := filepath.Join(t.TempDir(), "octopus")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsThulu(script) {
		t.Fatal("a script called octopus is not Thulu")
	}
}

func TestVersionReadsTheFirstLineAndProbes(t *testing.T) {
	script := filepath.Join(t.TempDir(), "thulu")
	body := "#!/bin/sh\n[ \"$1\" = version ] || exit 2\necho \"1.1.0\"\necho \"probe=$" + ProbeEnv + "\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if v := Version(t.Context(), script); v != "1.1.0" {
		t.Fatalf("version = %q", v)
	}
	if v := Version(t.Context(), filepath.Join(t.TempDir(), "missing")); v != "" {
		t.Fatalf("a copy that does not run has no version: %q", v)
	}
}
