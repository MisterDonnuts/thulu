package hostrun

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLogRotatesAndKeepsThree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.log")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	l.max = 10
	for _, line := range []string{"one 1234\n", "two 1234\n", "three 12\n", "four 123\n"} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{path: "four 123\n", path + ".1": "three 12\n", path + ".2": "two 1234\n"} {
		if got := readLog(t, file); got != want {
			t.Errorf("%s = %q, want %q", filepath.Base(file), got, want)
		}
	}
	if got := LogFiles(path); !slices.Equal(got, []string{path, path + ".1", path + ".2"}) {
		t.Errorf("files %v, want the log and two copies", got)
	}
}

func TestLogKeepsALineLongerThanTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.log")
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	l.max = 10
	long := strings.Repeat("x", 25) + "\n"
	for _, line := range []string{"short\n", long} {
		if _, err := l.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, path); got != long {
		t.Errorf("log = %q, want the long line whole", got)
	}
	if got := readLog(t, path+".1"); got != "short\n" {
		t.Errorf("log.1 = %q", got)
	}
}

func TestLogReopensAnOlderLogPrivately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	path := filepath.Join(t.TempDir(), "runner.log")
	if err := os.WriteFile(path, []byte("old 12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(path)
	if err != nil {
		t.Fatal(err)
	}
	l.max = 12
	if _, err := l.Write([]byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + ".1"} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want 0600", filepath.Base(file), info.Mode().Perm())
		}
	}
	if got := readLog(t, path+".1"); got != "old 12345\n" {
		t.Errorf("the older log was not moved aside whole: %q", got)
	}
}

func TestLogFilesNamesOnlyTheLogAndItsCopies(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"runner.log", "runner.log.1", "runner.log.12", "runner.json",
		"runner.log.old", "runner.log.1.gz", "runner.log.", "runner.logs", "other.log.1"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "runner.log.2"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runner.log")
	want := []string{path, path + ".1", path + ".12"}
	if got := LogFiles(path); !slices.Equal(got, want) {
		t.Errorf("LogFiles = %v, want %v", got, want)
	}
	if got := LogFiles(filepath.Join(t.TempDir(), "runner.log")); len(got) != 0 {
		t.Errorf("LogFiles of an empty folder = %v", got)
	}
}
