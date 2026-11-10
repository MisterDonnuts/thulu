package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func writeSnapshots(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackupRetentionKeepsTheNewestOfBothKinds(t *testing.T) {
	dir := t.TempDir()
	writeSnapshots(t, dir,
		"octopus_20260101_000000.tar.gz", "octopus_20260201_000000.sql", "octopus_20260301_000000.tar.gz",
		"octopus_20260401_000000.sql", "octopus_20260501_000000.sql", "octopus_20260601_000000.tar.gz",
		"octopus_20260701_000000.sql", "notes.txt", "other_20260101.sql")
	pinned := filepath.Join(dir, "octopus_20260101_000000.tar.gz")

	old, err := BackupsBeyondRetention(dir, 5, pinned)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || filepath.Base(old[0].Path) != "octopus_20260201_000000.sql" {
		t.Fatalf("beyond retention = %+v, want only the second oldest (the oldest is pinned)", old)
	}

	removed, err := PruneBackups(dir, 5, pinned)
	if err != nil || len(removed) != 1 {
		t.Fatalf("prune = %v, %v", removed, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	for _, want := range []string{"octopus_20260101_000000.tar.gz", "octopus_20260701_000000.sql", "notes.txt", "other_20260101.sql"} {
		if !strings.Contains(strings.Join(left, ","), want) {
			t.Errorf("%s was pruned; left %v", want, left)
		}
	}

	if old, err := BackupsBeyondRetention(filepath.Join(dir, "missing"), 5, ""); err != nil || old != nil {
		t.Errorf("missing dir = %v, %v", old, err)
	}
}

func TestBackupKeep(t *testing.T) {
	for v, want := range map[string]int{"": 5, "3": 3, "0": 5, "-2": 5, "many": 5} {
		if got := BackupKeep(func(string) string { return v }); got != want {
			t.Errorf("BackupKeep(%q) = %d, want %d", v, got, want)
		}
	}
}

// deploy/backup.sh must prune like PruneBackups: spare the last update's snapshot, kept/pinned
// versions' and those .env lists.
func TestBackupScriptPrunesByCountAndSparesKeptSnapshots(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "deploy", "backup.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	deploy := filepath.Join(root, "deploy")
	bin := filepath.Join(root, "bin")
	backups := filepath.Join(root, "backups")
	for _, d := range []string{deploy, bin, filepath.Join(root, "data")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(deploy, "backup.sh"), string(script), 0o700)
	for _, tool := range []string{"pg_dump", "psql"} {
		write(filepath.Join(bin, tool), "#!/bin/sh\necho '-- dump'\n", 0o700)
	}
	write(filepath.Join(deploy, ".octopus-update.json"), `{
  "backup_path": "backups/octopus_20260101_000000.sql",
  "kept": [
    {"version": "1.2.0"},
    {"version": "1.1.0", "snapshot": "octopus_20260102_000000.sql"},
    {"version": "0.9.0", "snapshot": "octopus_20260103_000000.sql", "pinned": true}
  ]
}
`, 0o600)
	write(filepath.Join(deploy, ".env"), "OCTOPUS_BACKUP_KEEP=1\nOCTOPUS_KEPT_SNAPSHOTS=1.0.0=octopus_20260104_000000.sql\n", 0o600)
	writeSnapshots(t, backups,
		"octopus_20260101_000000.sql", "octopus_20260102_000000.sql", "octopus_20260103_000000.sql",
		"octopus_20260104_000000.sql", "octopus_20260105_000000.sql", "octopus_20260106_000000.tar.gz")

	cmd := exec.Command("bash", filepath.Join(deploy, "backup.sh"), backups)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus",
		"OCTOPUS_DATA_DIR="+filepath.Join(root, "data"),
		"OCTOPUS_BACKUP_KEEP=", "OCTOPUS_KEPT_SNAPSHOTS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(backups)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{"octopus_20260101_000000.sql", "octopus_20260102_000000.sql",
		"octopus_20260103_000000.sql", "octopus_20260104_000000.sql"}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("%s was pruned; left %v", w, got)
		}
	}
	for _, gone := range []string{"octopus_20260105_000000.sql", "octopus_20260106_000000.tar.gz"} {
		if slices.Contains(got, gone) {
			t.Errorf("%s survived; left %v", gone, got)
		}
	}
	if len(got) != len(want)+1 {
		t.Errorf("left %v, want the four spared plus the new archive", got)
	}
}
