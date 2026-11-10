package deploy

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

type backupRig struct {
	t       *testing.T
	root    string
	bin     string
	backups string
	data    string
	remote  string
	env     []string
}

func newBackupRig(t *testing.T) *backupRig {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	script, err := os.ReadFile("backup.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := &backupRig{
		t: t, root: root,
		bin:     filepath.Join(root, "bin"),
		backups: filepath.Join(root, "backups"),
		data:    filepath.Join(root, "data"),
		remote:  filepath.Join(root, "remote"),
	}
	for _, d := range []string{filepath.Join(root, "deploy"), r.bin, r.remote} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	r.write(filepath.Join(root, "deploy", "backup.sh"), string(script), 0o700)
	r.stub("pg_dump", "echo '-- dump'")
	r.stub("psql", "cat >/dev/null")
	r.env = []string{
		"PATH=" + r.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + root,
		"OCTOPUS_LOCALE=en",
	}
	return r
}

func (r *backupRig) write(path, body string, mode os.FileMode) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		r.t.Fatal(err)
	}
}

func (r *backupRig) stub(name, body string) {
	r.write(filepath.Join(r.bin, name), "#!/usr/bin/env bash\nset -eu\n"+body+"\n", 0o700)
}

func (r *backupRig) seedData() {
	r.write(filepath.Join(r.data, "projects", "p1", "README.md"), "worktree", 0o640)
	r.write(filepath.Join(r.data, "task-attachments", "t1", "shot.png"), "png", 0o640)
	r.write(filepath.Join(r.data, "usage.json"), "{}", 0o640)
	r.write(filepath.Join(r.data, "logs", "daemon.log"), "log", 0o640)
	r.write(filepath.Join(r.data, "runs", "r1", "out.txt"), "run", 0o640)
}

func (r *backupRig) run(env ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command("bash", filepath.Join(r.root, "deploy", "backup.sh"), r.backups)
	cmd.Dir = r.root
	cmd.Env = append(append([]string{}, r.env...), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *backupRig) archives() []string {
	r.t.Helper()
	entries, err := os.ReadDir(r.backups)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			out = append(out, filepath.Join(r.backups, e.Name()))
		}
	}
	return out
}

func archiveMembers(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	members := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		members[strings.TrimPrefix(h.Name, "./")] = string(body)
	}
	return members
}

func TestBackupArchiveHoldsDataAndIsOwnerOnly(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus", "OCTOPUS_DATA_DIR="+r.data)
	if err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	got := r.archives()
	if len(got) != 1 {
		t.Fatalf("want one archive, got %v\n%s", got, out)
	}
	info, err := os.Stat(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("archive mode %v, want 0600", perm)
	}
	if dir, _ := os.Stat(r.backups); dir.Mode().Perm()&0o077 != 0 {
		t.Errorf("backup directory mode %v lets others in", dir.Mode().Perm())
	}
	m := archiveMembers(t, got[0])
	for name, want := range map[string]string{
		"postgres.sql":                      "-- dump\n",
		"data/projects/p1/README.md":        "worktree",
		"data/task-attachments/t1/shot.png": "png",
		"data/usage.json":                   "{}",
	} {
		if m[name] != want {
			t.Errorf("%s = %q, want %q", name, m[name], want)
		}
	}
	for name := range m {
		if strings.HasPrefix(name, "data/logs") || strings.HasPrefix(name, "data/runs") {
			t.Errorf("%s is run output and should stay out of the archive", name)
		}
	}
	if !strings.Contains(m["backup_meta.json"], `"source":"host"`) {
		t.Errorf("backup_meta.json = %q", m["backup_meta.json"])
	}
	if !strings.Contains(out, "OCTOPUS_SECRETBOX_KEY") {
		t.Errorf("the run should remind that the key is not in the archive:\n%s", out)
	}
}

func TestBackupStopsOnMissingDataDir(t *testing.T) {
	r := newBackupRig(t)
	out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus",
		"OCTOPUS_DATA_DIR="+filepath.Join(r.root, "nope"))
	if err == nil {
		t.Fatalf("backup.sh succeeded without its data directory:\n%s", out)
	}
	if !strings.Contains(out, "does not exist") {
		t.Errorf("output should name the missing directory:\n%s", out)
	}
	if a := r.archives(); len(a) != 0 {
		t.Errorf("an archive was written anyway: %v", a)
	}
}

func TestBackupStopsWithoutADataSource(t *testing.T) {
	r := newBackupRig(t)
	out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus")
	if err == nil || !strings.Contains(out, "OCTOPUS_DATA_DIR") {
		t.Fatalf("want a refusal naming OCTOPUS_DATA_DIR, got %v:\n%s", err, out)
	}
}

func TestBackupStopsOnEmptyDump(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	r.stub("pg_dump", "true")
	out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus", "OCTOPUS_DATA_DIR="+r.data)
	if err == nil {
		t.Fatalf("backup.sh kept an archive with an empty dump:\n%s", out)
	}
	if a := r.archives(); len(a) != 0 {
		t.Errorf("an archive was written anyway: %v", a)
	}
}

func (r *backupRig) remoteStubs() {
	r.stub("ssh", `while [ "${1:-}" != "" ] && [ "${1#-}" != "$1" ]; do
  case "$1" in -p|-o) shift 2 ;; *) shift ;; esac
done
shift # host
cmd=$1; shift
case "$cmd" in
mkdir) mkdir "`+r.remote+`/$1" 2>/dev/null || exit 1 ;;
rm) rm "`+r.remote+`/$1" ;;
*) echo "unexpected remote command $cmd" >&2; exit 2 ;;
esac`)
	r.stub("rsync", `list=0 files=()
while [ $# -gt 0 ]; do
  case "$1" in
  -e) shift 2; continue ;;
  --list-only) list=1 ;;
  --chmod=*) ;;
  *) files+=("$1") ;;
  esac
  shift
done
dest=${files[${#files[@]}-1]}
dir="`+r.remote+`/${dest#*:}"
if [ "$list" = 1 ]; then
  for f in "$dir"/*; do [ -e "$f" ] && echo "-rw------- 10 2026/01/01 00:00:00 $(basename "$f")"; done
  exit 0
fi
cp "${files[0]}" "$dir"`)
}

func (r *backupRig) remoteFiles(dir string) []string {
	r.t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.remote, dir))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestBackupPushesOffsiteAndRotates(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	r.remoteStubs()
	old := []string{"octopus_20250101_000000.tar.gz", "octopus_20250102_000000.tar.gz", "octopus_20250103_000000.tar.gz"}
	for _, name := range append(old, "notes.txt") {
		r.write(filepath.Join(r.remote, "octopus", name), "old", 0o600)
	}
	out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus", "OCTOPUS_DATA_DIR="+r.data,
		"OCTOPUS_BACKUP_REMOTE=backup@backup.example.com:octopus", "OCTOPUS_BACKUP_REMOTE_KEEP=2")
	if err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	local := r.archives()
	if len(local) != 1 {
		t.Fatalf("local archives: %v", local)
	}
	got := r.remoteFiles("octopus")
	want := []string{"notes.txt", "octopus_20250103_000000.tar.gz", filepath.Base(local[0])}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("remote holds %v, want %v (the new archive, the newest old one, and the file that is not a backup)", got, want)
	}
}

func TestBackupEncryptsTheOffsiteCopy(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	r.remoteStubs()
	r.stub("age", `out='' in=''
while [ $# -gt 0 ]; do
  case "$1" in -r) [ "$2" = age1test ] || exit 3; shift 2 ;; -o) out=$2; shift 2 ;; *) in=$1; shift ;; esac
done
{ printf 'AGE:'; cat "$in"; } >"$out"`)
	out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus", "OCTOPUS_DATA_DIR="+r.data,
		"OCTOPUS_BACKUP_REMOTE=u1@host:box", "OCTOPUS_BACKUP_AGE_RECIPIENT=age1test")
	if err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	got := r.remoteFiles("box")
	if len(got) != 1 || !strings.HasSuffix(got[0], ".tar.gz.age") {
		t.Fatalf("remote holds %v, want one .tar.gz.age", got)
	}
	body, err := os.ReadFile(filepath.Join(r.remote, "box", got[0]))
	if err != nil || !strings.HasPrefix(string(body), "AGE:") {
		t.Fatalf("remote copy was not encrypted: %v", err)
	}
	if m := archiveMembers(t, r.archives()[0]); m["postgres.sql"] == "" {
		t.Error("local archive lost its dump")
	}
}

func TestBackupFailsWhenTheOffsiteCopyFails(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	r.remoteStubs()
	r.stub("rsync", "exit 12")
	out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus", "OCTOPUS_DATA_DIR="+r.data,
		"OCTOPUS_BACKUP_REMOTE=u1@host:box")
	if err == nil {
		t.Fatalf("a failed offsite copy must fail the run (cron reports it):\n%s", out)
	}
	if len(r.archives()) != 1 {
		t.Error("the local archive should still be there")
	}
}

func TestBackupReadsTheComposeVolume(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	r.write(filepath.Join(r.root, "deploy", "docker-compose.yml"), "services: {}\n", 0o600)
	r.stub("docker", `[ "$1" = compose ] || exit 2
shift 3 # compose -f FILE
case "$1" in
exec) echo '-- dump from the pg service' ;;
run)
  while [ "$1" != -c ]; do shift; done
  script=${2//\/data/`+r.data+`}
  sh -c "$script" ;;
*) exit 2 ;;
esac`)
	out, err := r.run("OCTOPUS_COMPOSE_FILE=deploy/docker-compose.yml")
	if err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	a := r.archives()
	if len(a) != 1 {
		t.Fatalf("archives: %v\n%s", a, out)
	}
	m := archiveMembers(t, a[0])
	if m["postgres.sql"] != "-- dump from the pg service\n" {
		t.Errorf("postgres.sql = %q", m["postgres.sql"])
	}
	if m["data/projects/p1/README.md"] != "worktree" || m["data/task-attachments/t1/shot.png"] != "png" {
		t.Errorf("data missing from the archive: %v", keys(m))
	}
	for name := range m {
		if strings.HasPrefix(name, "data/logs") || strings.HasPrefix(name, "data/runs") {
			t.Errorf("%s should stay out of the archive", name)
		}
	}
	if st := r.status(); st.Source != "compose" || len(st.RestoreEnv) != 1 ||
		!strings.HasPrefix(st.RestoreEnv[0], "OCTOPUS_COMPOSE_FILE=/") || st.DatabaseURL {
		t.Errorf("status = %+v", st)
	}

	if out, err := r.run("OCTOPUS_COMPOSE_FILE=deploy/docker-compose.yml", "OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus"); err != nil {
		t.Fatalf("backup.sh with a DSN: %v\n%s", err, out)
	}
	if st := r.status(); st.Source != "compose" || !st.DatabaseURL {
		t.Errorf("status with a DSN = %+v, want database_url", st)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestBackupReadsTheKubernetesVolume(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	r.stub("kubectl", `case "$*" in
"-n octopus get pvc octopus-octopus -o jsonpath={.spec.volumeName}") printf pv-data ;;
"get pv pv-data -o jsonpath={.spec.hostPath.path}{.spec.local.path}") printf '%s' "`+r.data+`" ;;
"-n octopus exec -i statefulset/octopus-postgres -- pg_dump "*" -U octopus octopus")
  # The dump lands in the staging directory, which must sit next to the
  # archives rather than in $TMPDIR.
  ls -d "`+r.backups+`"/.octopus_backup.* >/dev/null 2>&1 || { echo "staging is not next to the archive" >&2; exit 3; }
  echo '-- dump from the pod' ;;
*) echo "unexpected kubectl $*" >&2; exit 2 ;;
esac`)
	out, err := r.run("OCTOPUS_BACKUP_SOURCE=kubernetes")
	if err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	a := r.archives()
	if len(a) != 1 {
		t.Fatalf("archives: %v\n%s", a, out)
	}
	m := archiveMembers(t, a[0])
	if m["postgres.sql"] != "-- dump from the pod\n" {
		t.Errorf("postgres.sql = %q", m["postgres.sql"])
	}
	if m["data/projects/p1/README.md"] != "worktree" || m["data/task-attachments/t1/shot.png"] != "png" {
		t.Errorf("data missing from the archive: %v", keys(m))
	}
	if !strings.Contains(m["backup_meta.json"], `"source":"kubernetes"`) {
		t.Errorf("backup_meta.json = %q", m["backup_meta.json"])
	}
	entries, _ := os.ReadDir(r.backups)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("staging directory %s left behind", e.Name())
		}
	}
}

func TestBackupStopsWhenThePVCHasNoPath(t *testing.T) {
	r := newBackupRig(t)
	r.stub("kubectl", `exit 1`)
	out, err := r.run("OCTOPUS_BACKUP_SOURCE=kubernetes")
	if err == nil || !strings.Contains(out, "octopus-octopus") {
		t.Fatalf("want a refusal naming the PVC, got %v:\n%s", err, out)
	}
	if a := r.archives(); len(a) != 0 {
		t.Errorf("an archive was written anyway: %v", a)
	}
}

// Regression: under umask 077 a non-root tar applied the umask on extraction, and copying the
// staged "data/."
// stamped the staging dir's owner and 0700 onto the data dir, which the daemon (uid 10001) could
// not open.
func TestRestoreKeepsModesAndTheDataDirectory(t *testing.T) {
	r := newBackupRig(t)
	r.write(filepath.Join(r.data, "projects", "p1", "README.md"), "worktree", 0o644)
	r.write(filepath.Join(r.data, "projects", "p1", "run.sh"), "#!/bin/sh", 0o755)
	for _, d := range []string{filepath.Join(r.data, "projects"), filepath.Join(r.data, "projects", "p1")} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(r.data, 0o750); err != nil {
		t.Fatal(err)
	}
	dsn := "OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus"
	out, err := r.run(dsn, "OCTOPUS_DATA_DIR="+r.data)
	if err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	a := r.archives()
	if len(a) != 1 {
		t.Fatalf("archives: %v\n%s", a, out)
	}
	if err := os.RemoveAll(filepath.Join(r.data, "projects")); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join(r.root, "deploy", "backup.sh"), a[0])
	cmd.Dir = r.root
	cmd.Env = append(append([]string{}, r.env...), "RESTORE=1", dsn, "OCTOPUS_DATA_DIR="+r.data)
	cmd.Stdin = strings.NewReader("y\n")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, b)
	}

	for path, want := range map[string]os.FileMode{
		r.data:                                  0o750,
		filepath.Join(r.data, "projects"):       0o755,
		filepath.Join(r.data, "projects", "p1"): 0o755,
		filepath.Join(r.data, "projects", "p1", "README.md"): 0o644,
		filepath.Join(r.data, "projects", "p1", "run.sh"):    0o755,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s restored with mode %v, want %v", path, got, want)
		}
	}
	entries, _ := os.ReadDir(r.backups)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("restore staging directory %s left behind", e.Name())
		}
	}
}

type backupStatus struct {
	Archive     string   `json:"archive"`
	BackupDir   string   `json:"backup_dir"`
	SizeBytes   int64    `json:"size_bytes"`
	FinishedAt  string   `json:"finished_at"`
	Kept        int      `json:"kept"`
	Keep        int      `json:"keep"`
	Source      string   `json:"source"`
	Script      string   `json:"script"`
	RestoreEnv  []string `json:"restore_env"`
	DatabaseURL bool     `json:"database_url"`
	Remote      string   `json:"remote"`
	RemoteState string   `json:"remote_state"`
}

func (r *backupRig) status() backupStatus {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(r.data, "backup-status.json"))
	if err != nil {
		r.t.Fatalf("status file: %v", err)
	}
	var st backupStatus
	if err := json.Unmarshal(body, &st); err != nil {
		r.t.Fatalf("status file is not JSON: %v\n%s", err, body)
	}
	return st
}

func TestBackupWritesItsStatusForTheDaemon(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	for _, name := range []string{"octopus_20250101_000000.tar.gz", "octopus_20250102_000000.sql"} {
		r.write(filepath.Join(r.backups, name), "old", 0o600)
	}
	env := []string{"OCTOPUS_DATABASE_URL=postgres://u:secret@localhost/octopus", "OCTOPUS_DATA_DIR=" + r.data, "OCTOPUS_BACKUP_KEEP=2"}
	if out, err := r.run(env...); err != nil {
		t.Fatalf("backup.sh: %v\n%s", err, out)
	}
	st := r.status()
	a := r.archives()
	if len(a) != 1 || st.Archive != filepath.Base(a[0]) {
		t.Fatalf("status names %q, archives are %v", st.Archive, a)
	}
	info, _ := os.Stat(a[0])
	if st.SizeBytes != info.Size() || st.Kept != 2 || st.Keep != 2 || st.Source != "host" || st.RemoteState != "none" || !st.DatabaseURL {
		t.Errorf("status = %+v", st)
	}
	if !filepath.IsAbs(st.BackupDir) || !filepath.IsAbs(st.Script) || !strings.HasSuffix(st.Script, "/deploy/backup.sh") {
		t.Errorf("paths must be absolute for a command run from anywhere: %+v", st)
	}
	if len(st.RestoreEnv) != 1 || !strings.HasPrefix(st.RestoreEnv[0], "OCTOPUS_DATA_DIR=/") {
		t.Errorf("restore_env = %v", st.RestoreEnv)
	}
	raw, _ := os.ReadFile(filepath.Join(r.data, "backup-status.json"))
	if strings.Contains(string(raw), "secret") {
		t.Error("the status file holds the database password")
	}
	if fi, _ := os.Stat(filepath.Join(r.data, "backup-status.json")); fi.Mode().Perm() != 0o644 {
		t.Errorf("status mode %v, want 0644 so the daemon reads it whoever ran the backup", fi.Mode().Perm())
	}

	if out, err := r.run(env...); err != nil {
		t.Fatalf("second backup.sh: %v\n%s", err, out)
	}
	newest := r.status().Archive
	if _, ok := archiveMembers(t, filepath.Join(r.backups, newest))["data/backup-status.json"]; ok {
		t.Error("the status file was archived; a restore would bring back a stale one")
	}
}

func TestBackupStatusRecordsAFailedOffsiteCopy(t *testing.T) {
	r := newBackupRig(t)
	r.seedData()
	r.remoteStubs()
	r.stub("rsync", "exit 12")
	if out, err := r.run("OCTOPUS_DATABASE_URL=postgres://test@localhost/octopus", "OCTOPUS_DATA_DIR="+r.data,
		"OCTOPUS_BACKUP_REMOTE=u1@host:box"); err == nil {
		t.Fatalf("want the run to fail:\n%s", out)
	}
	if st := r.status(); st.RemoteState != "failed" || st.Remote != "u1@host:box" || st.Archive == "" {
		t.Errorf("status = %+v, want the failed copy recorded", st)
	}
}
