package hostrun

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/gitremote"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

const runKey = "oct_run_key_that_must_never_be_stored"

// daemonFake serves a project the way the daemon does: /git/<task>/<name> answers from the daemon's
// clone, whose task branch is checked out in a worktree of its own, and only to the run's key.
type daemonFake struct {
	repo, wt string
	r        *Runner
	// Holds every /git/ call until a heartbeat arrives, so a call made without the lease shows up
	// in gitWithoutBeat instead of depending on how fast the machine is.
	awaitBeat bool
	// Fails every fetch while listing the refs still works.
	failPacks atomic.Bool
	// Fails every push.
	failPushes atomic.Bool
	// Holds every fetch this long (a time.Duration) before serving it.
	slowPacks atomic.Int64
	// Tells the job to stop at its next heartbeat.
	cancel atomic.Bool

	finalPushes atomic.Int32

	gitBusy        atomic.Int32
	gitCalls       atomic.Int32
	gitWithoutBeat atomic.Int32
	heartbeats     atomic.Int32
	beatsDuringGit atomic.Int32
	finished       chan runner.FinishRequest
}

func newDaemonFake(t *testing.T) *daemonFake {
	t.Helper()
	tmp := t.TempDir()
	d := &daemonFake{repo: filepath.Join(tmp, "projects", "app"), wt: filepath.Join(tmp, "worktree"),
		finished: make(chan runner.FinishRequest, 1)}
	h, err := gitremote.New("", func(r *http.Request, repo string) bool {
		_, pass, ok := r.BasicAuth()
		return ok && pass == runKey && strings.HasPrefix(repo, "t1/")
	})
	if err != nil {
		t.Skipf("git-http-backend unavailable: %v", err)
	}
	h.WithResolver(func(_ context.Context, repo string) (string, bool) { return d.repo, repo == "t1/app" })
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	gitIn(t, tmp, "init", "-q", "-b", "main", d.repo)
	gitIn(t, d.repo, "commit", "-q", "--allow-empty", "-m", "root")
	gitIn(t, d.repo, "config", "http.receivepack", "true")
	gitIn(t, d.repo, "worktree", "add", "-q", "-b", "task-1", d.wt)
	gitIn(t, d.wt, "commit", "-q", "--allow-empty", "-m", "task work")

	mux := http.NewServeMux()
	mux.Handle("/git/", http.StripPrefix("/git", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.gitBusy.Add(1)
		defer d.gitBusy.Add(-1)
		d.gitCalls.Add(1)
		if d.awaitBeat && !d.beatArrives() {
			d.gitWithoutBeat.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			if d.failPushes.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			if r.Header.Get(gitremote.FinalPushHeader) != "" {
				d.finalPushes.Add(1)
			}
		}
		if (d.failPacks.Load() || d.slowPacks.Load() > 0) && wantsObjects(t, r) {
			if d.failPacks.Load() {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			time.Sleep(time.Duration(d.slowPacks.Load()))
		}
		h.ServeHTTP(w, r)
	})))
	mux.HandleFunc("POST /runner/v1/jobs/{id}/heartbeat", func(w http.ResponseWriter, _ *http.Request) {
		d.heartbeats.Add(1)
		if d.gitBusy.Load() > 0 {
			d.beatsDuringGit.Add(1)
		}
		_ = json.NewEncoder(w).Encode(runner.HeartbeatResponse{LeaseSeconds: 60, Cancel: d.cancel.Load()})
	})
	mux.HandleFunc("POST /runner/v1/jobs/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /runner/v1/jobs/{id}/outputs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /runner/v1/jobs/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var res runner.FinishRequest
		_ = json.NewDecoder(r.Body).Decode(&res)
		d.finished <- res
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	d.r = &Runner{
		cfg:    &Config{DaemonURL: srv.URL, WorkDir: filepath.Join(tmp, "work")},
		client: NewClient(srv.URL, runner.CredentialPrefix+"test", "test"),
	}
	return d
}

func (d *daemonFake) beatArrives() bool {
	seen := d.beatsDuringGit.Load()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if d.beatsDuringGit.Load() > seen {
			return true
		}
	}
	return false
}

// wantsObjects tells a fetch from a ref listing, which protocol v2 also posts to git-upload-pack.
func wantsObjects(t *testing.T, r *http.Request) bool {
	if !strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Error(err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Error(err)
			return false
		}
		if body, err = io.ReadAll(zr); err != nil {
			t.Error(err)
		}
	}
	return bytes.Contains(body, []byte("want "))
}

func (d *daemonFake) job(t *testing.T, branch string, readOnly bool) *job {
	t.Helper()
	return &job{r: d.r, root: t.TempDir(), wts: map[string]string{}, every: runner.DefaultHeartbeat,
		spec: &runner.JobSpec{ID: "j1", InstanceID: "i1", TaskID: "t1", Stage: "in_progress", AgentName: "dev",
			ReadOnly: readOnly, ResultRef: "octopus-result-1", Env: map[string]string{"OCTOPUS_API_KEY": runKey},
			Projects: []runner.JobProject{{ID: "p1", Name: "app", DefaultBranch: "main", Branch: branch,
				Source: runner.SourceDaemon}}}}
}

func (j *job) project() string { return filepath.Join(j.root, "projects", "app") }

// keyStoredUnder lists the files under dirs that hold the run key.
func keyStoredUnder(t *testing.T, dirs ...string) []string {
	t.Helper()
	var hits []string
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() {
				return err
			}
			if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), runKey) {
				hits = append(hits, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return hits
}

// Case 27: the machine fetches a Thulu project through /git/ with the run's key, works on the task
// branch and pushes the work, even of a failed run, to the result branch; the key stays in the env.
func TestAThuluProjectComesFromTheDaemonAndGoesBackOnTheResultBranch(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	j := d.job(t, "task-1", false)
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	if got, want := gitIn(t, j.project(), "rev-parse", "HEAD"), gitIn(t, d.wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("the job starts at %s, want the daemon's task tip %s", got, want)
	}
	if b := gitIn(t, j.project(), "symbolic-ref", "--short", "HEAD"); b != "task-1" {
		t.Errorf("the agent is on %q, want task-1", b)
	}
	if err := os.WriteFile(filepath.Join(j.project(), "out.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	pushed, err := j.afterRun(ctx, 1)
	if err != nil || len(pushed) != 1 {
		t.Fatalf("afterRun = %+v, %v; want the result branch pushed after a failed run", pushed, err)
	}
	head := gitIn(t, j.project(), "rev-parse", "HEAD")
	if p := pushed[0]; p.ProjectID != "p1" || p.Branch != "octopus-result-1" || p.Commit != head || strings.Contains(p.Remote, runKey) {
		t.Errorf("pushed = %+v, want p1 on octopus-result-1 at %s", p, head)
	}
	if got := gitIn(t, d.repo, "show", "octopus-result-1:out.txt"); got != "work" {
		t.Errorf("the daemon's result branch holds %q", got)
	}
	if got := gitIn(t, d.repo, "log", "-1", "--format=%s", "task-1"); got != "task work" {
		t.Errorf("the daemon's task branch moved to %q; only the daemon integrates", got)
	}
	if origin := gitIn(t, j.project(), "remote", "get-url", "origin"); origin != j.daemonRepoURL(j.spec.Projects[0]) {
		t.Errorf("the clone's origin is %q, want the daemon's /git/ without a key", origin)
	}
	if hits := keyStoredUnder(t, d.r.cfg.WorkDir, j.root); len(hits) > 0 {
		t.Errorf("the run key was written to %v", hits)
	}
}

// Case 28: the next job of the project reuses the mirror and only fetches, so it starts from what the
// daemon has now; two jobs of one task can hold their copies at once.
func TestASecondJobReusesTheMirrorAndStartsFromTheNewTip(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	first := d.job(t, "task-1", false)
	if err := first.linkProject(ctx, first.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	mirror := d.r.cfg.mirrorDir("p1")
	gitIn(t, mirror, "config", "octopus.marker", "kept")
	gitIn(t, d.wt, "commit", "-q", "--allow-empty", "-m", "integrated meanwhile")

	second := d.job(t, "task-1", false)
	if err := second.linkProject(ctx, second.spec.Projects[0]); err != nil {
		t.Fatalf("a second job while the first still holds its copy: %v", err)
	}
	if got, want := gitIn(t, second.project(), "rev-parse", "HEAD"), gitIn(t, d.wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("the second job starts at %s, want the daemon's new tip %s", got, want)
	}
	if got := gitIn(t, mirror, "config", "octopus.marker"); got != "kept" {
		t.Errorf("the mirror was made again: marker %q", got)
	}
}

// Case 29: a read-only run (a mention) reads the task branch, or the default branch when the task has
// none, and pushes nothing.
func TestAReadOnlyRunReadsTheTaskBranchOrTheDefault(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	task, main := gitIn(t, d.wt, "rev-parse", "HEAD"), gitIn(t, d.repo, "rev-parse", "main")
	for _, tc := range []struct{ branch, want string }{
		{"task-1", task},
		{"", main},
		{"task-9", main},
	} {
		j := d.job(t, tc.branch, true)
		if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
			t.Fatalf("branch %q: %v", tc.branch, err)
		}
		if got := gitIn(t, j.project(), "rev-parse", "HEAD"); got != tc.want {
			t.Errorf("branch %q: read %s, want %s", tc.branch, got, tc.want)
		}
		if pushed, err := j.afterRun(ctx, 0); err != nil || len(pushed) != 0 {
			t.Errorf("branch %q: afterRun = %+v, %v; want nothing pushed", tc.branch, pushed, err)
		}
	}
	if out := gitIn(t, d.repo, "branch", "--list", "octopus-result-1"); out != "" {
		t.Errorf("a read-only run left a result branch: %q", out)
	}
}

// A writing run never works on a stale or invented branch: the fetch and the task branch are required.
func TestAWritingRunNeedsTheFetchAndTheTaskBranch(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	cases := []struct {
		name, branch, key, want string
	}{
		{"missing task branch", "task-9", runKey, "task-9"},
		{"key refused", "task-1", "oct_other_run", "app"},
		{"no branch", "", runKey, "app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := d.job(t, tc.branch, false)
			j.spec.Env["OCTOPUS_API_KEY"] = tc.key
			err := j.linkProject(ctx, j.spec.Projects[0])
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("linkProject = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// The agent is told to use plain git: in its copy origin is the daemon's /git/, origin/<default> is
// there to rebase on, and a plain push lands on the result branch with the key from the env.
func TestTheAgentUsesPlainGitOnAThuluProject(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	j := d.job(t, "task-1", false)
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	if got, want := gitIn(t, j.project(), "rev-parse", "origin/main"), gitIn(t, d.repo, "rev-parse", "main"); got != want {
		t.Errorf("origin/main = %s, want the daemon's default %s", got, want)
	}
	// The daemon fetched GitHub meanwhile: its origin/main moved past its local main.
	gitIn(t, d.repo, "update-ref", "refs/remotes/origin/main",
		gitIn(t, d.repo, "commit-tree", "-p", "main", "-m", "upstream", "main^{tree}"))
	agent := func(args ...string) {
		t.Helper()
		if _, err := gitEnv(ctx, j.env(), j.project(), args...); err != nil {
			t.Fatalf("the agent's git %v: %v", args, err)
		}
	}
	agent("fetch", "--quiet", "origin")
	agent("rebase", "--quiet", "origin/main")
	agent("commit", "--quiet", "--allow-empty", "-m", "agent work")
	agent("push", "--quiet")
	if got, want := gitIn(t, d.repo, "rev-parse", "octopus-result-1"), gitIn(t, j.project(), "rev-parse", "HEAD"); got != want {
		t.Errorf("the daemon's result branch is at %s, want the agent's push %s", got, want)
	}
	if got := gitIn(t, d.repo, "log", "-1", "--format=%s", "octopus-result-1~2"); got != "upstream" {
		t.Errorf("the pushed work sits on %q, want the daemon's origin/main", got)
	}
	if hits := keyStoredUnder(t, d.r.cfg.WorkDir, j.root); len(hits) > 0 {
		t.Errorf("the run key was written to %v", hits)
	}
	if !strings.Contains(credentialFill(t, j, d.r.cfg.DaemonURL+"/git/t1/app"), runKey) {
		t.Error("git in the clone gets no key for the daemon's /git/")
	}
	for _, other := range []string{"https://elsewhere.invalid/repo.git", d.r.cfg.DaemonURL + "/api/tasks"} {
		if strings.Contains(credentialFill(t, j, other), runKey) {
			t.Errorf("git in the clone hands the run key to %s", other)
		}
	}
}

// credentialFill asks git in the job's clone what it would send to rawURL. Errors are ignored: a host
// nobody answers for fails, and that is the point.
func credentialFill(t *testing.T, j *job, rawURL string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "credential", "fill")
	cmd.Dir = j.project()
	cmd.Env = append(j.env(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	cmd.Stdin = strings.NewReader("url=" + rawURL + "\n\n")
	out, _ := cmd.Output()
	return string(out)
}

// The agent may leave the task branch. The copy goes with the job, so its HEAD is pushed anyway and
// the daemon decides whether it fits the task branch.
func TestWorkOffTheTaskBranchStillReachesTheDaemon(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	j := d.job(t, "task-1", false)
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	gitIn(t, j.project(), "checkout", "-q", "-b", "elsewhere")
	if err := os.WriteFile(filepath.Join(j.project(), "out.txt"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}
	pushed, err := j.afterRun(ctx, 0)
	if err != nil || len(pushed) != 1 {
		t.Fatalf("afterRun = %+v, %v; want the work pushed from the other branch", pushed, err)
	}
	if got := gitIn(t, d.repo, "show", "octopus-result-1:out.txt"); got != "work" {
		t.Errorf("the daemon's result branch holds %q", got)
	}
}

// The default branch comes from the daemon's origin/<default>, which tracks GitHub; its local default
// is only the fallback for a repo without one.
func TestTheDefaultBranchIsTheDaemonsOrigin(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	upstream := gitIn(t, d.repo, "commit-tree", "-p", "main", "-m", "upstream", "main^{tree}")
	gitIn(t, d.repo, "update-ref", "refs/remotes/origin/main", upstream)
	j := d.job(t, "", true)
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, j.project(), "rev-parse", "HEAD"); got != upstream {
		t.Errorf("a read-only run reads %s, want the daemon's origin/main %s", got, upstream)
	}
}

// A read-only run falls back to the default branch only when the daemon has no task branch, never
// because the fetch failed.
func TestAReadOnlyRunFailsOnAFailedFetch(t *testing.T) {
	d := newDaemonFake(t)
	d.failPacks.Store(true)
	j := d.job(t, "task-1", true)
	err := j.linkProject(context.Background(), j.spec.Projects[0])
	if err == nil || !strings.Contains(err.Error(), "task-1") {
		t.Errorf("linkProject = %v, want the failed fetch of task-1", err)
	}
}

// The mirror keeps a copy of each task branch it fetched; once the daemon deletes the branch (the task
// ended), the next fetch drops it.
func TestTheMirrorDropsBranchesTheDaemonDeleted(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	first := d.job(t, "task-1", false)
	if err := first.linkProject(ctx, first.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	mirror := d.r.cfg.mirrorDir("p1")
	if refs := gitIn(t, mirror, "for-each-ref", "--format=%(refname)", daemonRefs); !strings.Contains(refs, "task-1") {
		t.Fatalf("precondition: the mirror has %q", refs)
	}
	gitIn(t, d.repo, "worktree", "remove", "--force", d.wt)
	gitIn(t, d.repo, "branch", "-q", "-D", "task-1")

	next := d.job(t, "", true)
	if err := next.linkProject(ctx, next.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	if refs := gitIn(t, mirror, "for-each-ref", "--format=%(refname)", daemonRefs); refs != daemonRefs+"main" {
		t.Errorf("the mirror keeps %q, want only the default branch", refs)
	}
}

// Case 53: the lease holds while the job fetches and pushes, not only while the agent runs, and ends
// once the daemon has the result.
func TestTheLeaseCoversFetchAndPush(t *testing.T) {
	d := newDaemonFake(t)
	d.awaitBeat = true
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	entry := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(entry, []byte("echo work > \"$OCTOPUS_ROOT/projects/app/out.txt\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.r.cfg.Entrypoint = entry

	j := d.job(t, "task-1", false)
	j.every = 20 * time.Millisecond
	j.execute(context.Background())
	res := <-d.finished
	if res.ExitCode != 0 || len(res.Pushed) != 1 {
		t.Fatalf("finish = %+v, want a clean run that pushed its result", res)
	}
	if n := d.gitWithoutBeat.Load(); n > 0 {
		t.Errorf("%d of %d calls to /git/ saw no heartbeat, want the lease kept through fetch and push", n, d.gitCalls.Load())
	}
	after := d.heartbeats.Load()
	time.Sleep(5 * j.every)
	if n := d.heartbeats.Load() - after; n > 1 {
		t.Errorf("%d heartbeats after the job ended, want the lease released", n)
	}
}
