//go:build unix

package hostrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

// testTmp points the machine's /tmp at a folder of the test's own, resolved.
func testTmp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := tmpRoot
	tmpRoot = dir
	t.Cleanup(func() { tmpRoot = old })
	return dir
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("%s is gone: %v", path, err)
	}
}

func mustBeGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s is still there: %v", path, err)
	}
}

func TestEnvSetsTmpVars(t *testing.T) {
	t.Setenv("JAVA_TOOL_OPTIONS", "-Xmx2g")
	j := &job{r: &Runner{cfg: &Config{}}, spec: &runner.JobSpec{ID: "j1", Env: map[string]string{}}, root: t.TempDir()}
	tmp := filepath.Join(j.root, "tmp")
	env := j.env()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP", "OCTOPUS_TMP"} {
		if got := envValue(env, k); got != tmp {
			t.Errorf("%s = %q, want %q", k, got, tmp)
		}
	}
	if got, want := envValue(env, "JAVA_TOOL_OPTIONS"), "-Xmx2g -Djava.io.tmpdir="+tmp; got != want {
		t.Errorf("JAVA_TOOL_OPTIONS = %q, want %q (the machine's options kept, the run's folder last)", got, want)
	}

	j.spec.Env["JAVA_TOOL_OPTIONS"] = "-Dfile.encoding=UTF-8"
	if got, want := envValue(j.env(), "JAVA_TOOL_OPTIONS"), "-Dfile.encoding=UTF-8 -Djava.io.tmpdir="+tmp; got != want {
		t.Errorf("JAVA_TOOL_OPTIONS from the job = %q, want %q", got, want)
	}
}

func TestRemoveSessionRemovesClaudeTmp(t *testing.T) {
	tmp := testTmp(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "runs", "job-1")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(tmp, "claude-"+strconv.Itoa(os.Getuid()))
	slug := nonAlnum.ReplaceAllString(resolved, "-")
	output := filepath.Join(own, slug, "session-1", "tasks", "b1.output")
	other := filepath.Join(own, slug+"-other", "tasks", "b2.output")
	for _, f := range []string{output, other} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, f, "server log")
	}

	removeSession(root)

	mustBeGone(t, filepath.Join(own, slug))
	mustExist(t, other)
}

func TestChatCleanupEmptiesTmp(t *testing.T) {
	tmp := testTmp(t)
	r := New(&Config{WorkDir: t.TempDir()}, "test")
	j := &job{r: r, root: r.cfg.chatRoot("chat-1"), spec: &runner.JobSpec{ID: "job1", InstanceID: "job1",
		LaunchKind: runner.LaunchKindChatTurn}}
	if err := j.prepareChat(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"tmp/merged-prompt.md", "tmp/.config/octopus/token", "tmp/job1.message"} {
		p := filepath.Join(j.root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, p, "x")
	}
	resolved, err := filepath.EvalSymlinks(j.root)
	if err != nil {
		t.Fatal(err)
	}
	claudeTmp := filepath.Join(tmp, "claude-"+strconv.Itoa(os.Getuid()), nonAlnum.ReplaceAllString(resolved, "-"))
	if err := os.MkdirAll(claudeTmp, 0o700); err != nil {
		t.Fatal(err)
	}

	j.cleanupChat()

	entries, err := os.ReadDir(filepath.Join(j.root, "tmp"))
	if err != nil {
		t.Fatalf("the chat's tmp folder went too: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the chat's tmp still holds %d entries after the turn", len(entries))
	}
	mustBeGone(t, claudeTmp)
}

// folderJob prepares a kill fixture whose job works on a folder project: a checkout mapped in the
// config, the runner's clone of it, and the task worktree the job creates.
func folderJob(t *testing.T, f *killFixture, script string) (j *job, mirror string) {
	t.Helper()
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@t")
	}
	checkout := filepath.Join(t.TempDir(), "app")
	gitIn(t, filepath.Dir(checkout), "init", "-q", "-b", "main", checkout)
	writeFile(t, filepath.Join(checkout, "README.md"), "app\n")
	gitIn(t, checkout, "add", "-A")
	gitIn(t, checkout, "commit", "-q", "-m", "root")
	f.r.cfg.Projects = map[string]string{"p1": checkout}
	j = f.job(script)
	j.spec.Projects = []runner.JobProject{{ID: "p1", Name: "app", Branch: "task-x", DefaultBranch: "main"}}
	j.spec.Env["THULU_TEST_TMP"] = tmpRoot
	return j, f.r.cfg.mirrorDir("p1")
}

// On a real task the evidence agent copied the worktree to /tmp and ran `ng serve` from the copy. At the
// run's end the copy goes: the server worked in it, and its .git file points at the run's worktree.
// The copy's .git is never used to run git: the live worktree stays registered.
func TestRunRemovesTmpCopyOwnedByRun(t *testing.T) {
	testTmp(t)
	f := newKillFixture(t)
	script := `T="$THULU_TEST_TMP"; M="$THULU_TEST_MARK"
cp -R "$OCTOPUS_ROOT/projects/app/." "$T/x-copy"
mkdir -p "$T/x-copy/src" "$T/y-work" "$T/z-copy"
cp "$T/x-copy/.git" "$T/z-copy/.git"
(cd "$T/x-copy/src" && THULU_TEST_HELPER=sleep exec "$HELPER" "helper-$M-copy" >/dev/null 2>&1 &)
(cd "$T/y-work" && THULU_TEST_HELPER=sleep exec "$HELPER" "helper-$M-work" >/dev/null 2>&1 &)
sleep 1
`
	j, mirror := folderJob(t, f, script)
	res := f.execute(context.Background(), j, nil)
	after := gitIn(t, mirror, "worktree", "list", "--porcelain")
	if !strings.Contains(after, filepath.Join("worktrees", "task-x", "app")) {
		t.Errorf("the live worktree lost its registration:\n%s", after)
	}
	for _, name := range []string{"x-copy", "y-work", "z-copy"} {
		mustBeGone(t, filepath.Join(tmpRoot, name))
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "copy") || strings.Contains(w, "y-work") {
			t.Errorf("a removed entry was also reported: %q", w)
		}
	}
	if _, err := os.Stat(filepath.Join(f.r.cfg.WorkDir, "worktrees", "task-x", "app", "README.md")); err != nil {
		t.Errorf("the task's worktree went with the copy: %v", err)
	}
}

// A file the run left in /tmp that nothing ties to it stays where it is and shows on the run.
func TestRunReportsUnattributedTmpEntries(t *testing.T) {
	tmp := testTmp(t)
	writeFile(t, filepath.Join(tmp, "older.log"), "there before the run")
	f := newKillFixture(t)
	j := f.job(`printf 'baseline\n' > "$THULU_TEST_TMP/rb3043-baseline.log"` + "\n")
	j.spec.Env["THULU_TEST_TMP"] = tmp
	res := f.execute(context.Background(), j, nil)
	left := filepath.Join(tmp, "rb3043-baseline.log")
	mustExist(t, left)
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "older.log") {
			t.Errorf("an entry older than the run was reported: %q", w)
		}
		if strings.Contains(w, left) {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %q do not name %s", res.Warnings, left)
	}
}

// Containers created while the run ran are reported, never removed.
func TestHostContainersCreatedDuringTheRunAreReported(t *testing.T) {
	bin := t.TempDir()
	count := filepath.Join(bin, "calls")
	fake := `#!/bin/sh
n=$(cat "` + count + `" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "` + count + `"
case "$1" in ps) ;; *) echo "unexpected docker $*" >&2; exit 3;; esac
printf 'aaa\tdb-old\trunning\n'
if [ "$n" -gt 1 ]; then printf 'bbb\tshop-db-1\trunning\nccc\tjob-x\texited\n'; fi
`
	writeFile(t, filepath.Join(bin, "docker"), fake)
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := dockerCmd
	dockerCmd = filepath.Join(bin, "docker")
	t.Cleanup(func() { dockerCmd = old })
	testTmp(t)

	w := watchOutside(context.Background(), "job-1", "", nil)
	got := w.finish(context.Background(), nil, nil, false)
	if len(got) != 2 || !strings.Contains(got[0], "shop-db-1") || !strings.Contains(got[0], "running") ||
		!strings.Contains(got[1], "job-x") {
		t.Errorf("warnings = %q, want shop-db-1 still running and job-x left", got)
	}
	if strings.Contains(strings.Join(got, " "), "db-old") {
		t.Errorf("a container from before the run was reported: %q", got)
	}
}

// A container the run created from its own folders (a compose project in its worktree, a bind mount
// of a /tmp copy it made) goes with the run; one nothing ties to the run is only reported.
func TestHostContainersOfTheRunAreRemoved(t *testing.T) {
	bin := t.TempDir()
	count, removed := filepath.Join(bin, "calls"), filepath.Join(bin, "removed")
	root := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	fake := `#!/bin/sh
case "$1" in
ps)
  n=$(cat "` + count + `" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "` + count + `"
  printf 'aaa\tdb-old\trunning\n'
  if [ "$n" -gt 1 ]; then printf 'bbb\tapp-db-1\trunning\nccc\tmine\trunning\nddd\tserve\texited\n'; fi ;;
inspect)
  shift 2; shift
  for id in "$@"; do case "$id" in
  bbb) printf 'bbb\t{"com.docker.compose.project.working_dir":"` + root + `/app"}\tnull\n' ;;
  ccc) printf 'ccc\tnull\t[{"Type":"bind","Source":"` + other + `"}]\n' ;;
  ddd) printf 'ddd\t{}\t[{"Type":"volume","Source":"/var/lib/x"},{"Type":"bind","Source":"` + root + `/app/dist"}]\n' ;;
  esac; done ;;
rm) echo "$4" >> "` + removed + `" ;;
*) echo "unexpected docker $*" >&2; exit 3 ;;
esac
`
	writeFile(t, filepath.Join(bin, "docker"), fake)
	if err := os.Chmod(filepath.Join(bin, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := dockerCmd
	dockerCmd = filepath.Join(bin, "docker")
	t.Cleanup(func() { dockerCmd = old })
	testTmp(t)

	w := watchOutside(context.Background(), "job-1", root, nil)
	got := w.finish(context.Background(), nil, nil, false)
	if len(got) != 1 || !strings.Contains(got[0], "mine") {
		t.Errorf("warnings = %q, want only the container nothing ties to the run", got)
	}
	b, err := os.ReadFile(removed)
	if err != nil {
		t.Fatal(err)
	}
	if ids := strings.Fields(string(b)); !slices.Equal(ids, []string{"bbb", "ddd"}) {
		t.Errorf("removed %v, want the compose project of the run and the container mounting its folder", ids)
	}
}

func TestBuildCachePruned(t *testing.T) {
	wt := t.TempDir()
	big := filepath.Join(wt, ".angular", "cache", "20.1", "app", "x.bin")
	small := filepath.Join(wt, ".next", "cache", "y.bin")
	for path, size := range map[string]int{big: 2 << 20, small: 64 << 10} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	j := &job{r: &Runner{cfg: &Config{BuildCacheMaxMB: 1}}, spec: &runner.JobSpec{ID: "j1"}, wts: map[string]string{"p1": wt}}
	j.pruneBuildCaches()
	mustBeGone(t, filepath.Join(wt, ".angular", "cache"))
	mustExist(t, small)
	mustExist(t, filepath.Join(wt, ".angular"))

	j.r.cfg.BuildCacheMaxMB = -1
	if err := os.MkdirAll(filepath.Dir(big), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	j.pruneBuildCaches()
	mustExist(t, big)
}

// ---- worktree sweep -------------------------------------------------------------------------------

type sweepFixture struct {
	r       *Runner
	remote  string
	mirror  string
	open    map[string][]string
	calls   int
	failing bool
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "t")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "t@t")
	}
	s := &sweepFixture{open: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runner/v1/worktrees", func(w http.ResponseWriter, _ *http.Request) {
		s.calls++
		if s.failing {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(runner.WorktreesResponse{Projects: s.open})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	tmp := t.TempDir()
	s.remote = filepath.Join(tmp, "remote.git")
	checkout := filepath.Join(tmp, "checkout")
	gitIn(t, tmp, "init", "-q", "--bare", "-b", "main", s.remote)
	gitIn(t, tmp, "clone", "-q", s.remote, checkout)
	gitIn(t, checkout, "commit", "-q", "--allow-empty", "-m", "root")
	gitIn(t, checkout, "push", "-q", "origin", "HEAD:main")
	s.r = &Runner{cfg: &Config{DaemonURL: srv.URL, WorkDir: filepath.Join(tmp, "work")},
		client: NewClient(srv.URL, runner.CredentialPrefix+"test", "test")}
	s.mirror = s.r.cfg.mirrorDir("p1")
	if _, err := ensureMirror(context.Background(), checkout, s.mirror); err != nil {
		t.Fatal(err)
	}
	return s
}

// worktree creates the task worktree of branch with one commit of its own.
func (s *sweepFixture) worktree(t *testing.T, branch string) string {
	t.Helper()
	wt := filepath.Join(s.r.cfg.worktreesDir(), branch, "app")
	if _, err := ensureTaskWorktree(context.Background(), s.mirror, wt, branch, "main"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, branch+".txt"), branch)
	gitIn(t, wt, "add", "-A")
	gitIn(t, wt, "commit", "-q", "-m", branch)
	return wt
}

func TestWorktreeSweepRemovesClosedBranch(t *testing.T) {
	s := newSweepFixture(t)
	ctx := context.Background()
	closed := s.worktree(t, "task-a")
	gitIn(t, closed, "push", "-q", "origin", "task-a")
	open := s.worktree(t, "task-b")
	stray := filepath.Join(s.r.cfg.worktreesDir(), "output", "_index.md")
	if err := os.MkdirAll(filepath.Dir(stray), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, stray, "a report written through ../../output")
	gone := filepath.Join(s.r.cfg.WorkDir, "repos", "gone")
	gitIn(t, filepath.Dir(gone), "clone", "-q", "--no-checkout", s.remote, gone)

	// A daemon that fails removes nothing.
	s.failing = true
	s.r.sweepWorktrees(ctx)
	mustExist(t, closed)
	mustExist(t, stray)
	mustExist(t, gone)

	s.failing = false
	s.open["p1"] = []string{"task-b"}
	s.r.sweepWorktrees(ctx)

	mustBeGone(t, filepath.Dir(closed))
	mustExist(t, open)
	mustBeGone(t, filepath.Dir(stray))
	mustBeGone(t, gone)
	mustExist(t, s.mirror)
	if refExists(ctx, s.mirror, "refs/heads/task-a") {
		t.Error("the closed branch is still in the clone")
	}
	if list := gitIn(t, s.mirror, "worktree", "list", "--porcelain"); strings.Contains(list, "task-a") {
		t.Errorf("the closed worktree is still registered:\n%s", list)
	}

	// A branch a running job uses stays even when the daemon calls it closed.
	s.open["p1"] = nil
	s.r.jobs = map[string]*job{"j1": {spec: &runner.JobSpec{ID: "j1", Projects: []runner.JobProject{{ID: "p1", Branch: "task-b"}}}}}
	s.r.sweepWorktrees(ctx)
	mustExist(t, open)
}

func TestWorktreeSweepKeepsUnsavedWork(t *testing.T) {
	s := newSweepFixture(t)
	ctx := context.Background()
	unpushed := s.worktree(t, "task-a")
	writeFile(t, filepath.Join(unpushed, "dirty.txt"), "never committed")
	busy := s.worktree(t, "task-c")
	writeFile(t, filepath.Join(busy, "dirty.txt"), "git cannot commit this now")
	// git holds the worktree's index: the commit fails, so nothing of it can be saved.
	adminDir := worktreeGitdir(busy)
	writeFile(t, filepath.Join(adminDir, "index.lock"), "")

	s.r.sweepWorktrees(ctx)

	mustBeGone(t, filepath.Dir(unpushed))
	refs := gitIn(t, s.mirror, "for-each-ref", "--format=%(refname)", unsentRefs+"task-a/")
	if refs == "" {
		t.Fatal("the unpushed work of task-a was not saved in the clone")
	}
	if got := gitIn(t, s.mirror, "show", strings.Fields(refs)[0]+":dirty.txt"); got != "never committed" {
		t.Errorf("saved dirty.txt = %q", got)
	}
	if got := gitIn(t, s.mirror, "show", strings.Fields(refs)[0]+":task-a.txt"); got != "task-a" {
		t.Errorf("saved task-a.txt = %q", got)
	}
	mustExist(t, filepath.Join(busy, "dirty.txt"))

	// The clone stays though the daemon no longer names its project: a worktree and an unsent ref
	// still need it.
	s.r.sweepWorktrees(ctx)
	mustExist(t, s.mirror)
}
