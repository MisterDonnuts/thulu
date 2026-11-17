package hostrun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A result push that fails (the daemon restarting, the tunnel down, a revoked key) leaves the work in
// the mirror; the task's next job hands it to the daemon before it fetches. The agent's own push goes
// unmarked and the result push marked, so the daemon publishes only the first.
func TestWorkThatMissesTheDaemonWaitsInTheMirrorForTheNextJob(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	j := d.job(t, "task-1", false)
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := gitEnv(ctx, j.env(), j.project(), "push", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if n := d.finalPushes.Load(); n != 0 {
		t.Errorf("the agent's own push was marked final %d times", n)
	}
	writeFile(t, filepath.Join(j.project(), "out.txt"), "work")
	d.failPushes.Store(true)

	pushed, err := j.afterRun(ctx, 0)
	ref := unsentRef("octopus-result-1", "j1")
	if err == nil || len(pushed) != 0 || !strings.Contains(err.Error(), ref) {
		t.Fatalf("afterRun = %+v, %v; want the failed push reported with the ref that keeps the work", pushed, err)
	}
	mirror := d.r.cfg.mirrorDir("p1")
	if got := gitIn(t, mirror, "show", ref+":out.txt"); got != "work" {
		t.Errorf("the mirror keeps %q under %s", got, ref)
	}
	j.cleanup(ctx)
	if _, err := os.Stat(j.root); !os.IsNotExist(err) {
		t.Errorf("the job's folder stayed although its work is in the mirror: %v", err)
	}

	d.failPushes.Store(false)
	next := d.job(t, "task-1", false)
	next.spec.ID = "j2"
	if err := next.linkProject(ctx, next.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, d.repo, "show", "octopus-result-1:out.txt"); got != "work" {
		t.Errorf("the daemon's result branch holds %q, want the earlier run's work", got)
	}
	if left := gitIn(t, mirror, "for-each-ref", unsentRefs); left != "" {
		t.Errorf("the mirror still keeps %q after the daemon got it", left)
	}
	if d.finalPushes.Load() == 0 {
		t.Error("the result pushes carried no final mark")
	}
}

// A clone whose work could not be committed holds it alone: the job's folder stays.
func TestAJobKeepsItsFolderWhenItsWorkIsNowhereElse(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	j := d.job(t, "task-1", false)
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(j.project(), "out.txt"), "work")
	writeFile(t, filepath.Join(j.project(), ".git", "index.lock"), "")

	if _, err := j.afterRun(ctx, 0); err == nil || !strings.Contains(err.Error(), j.project()) {
		t.Fatalf("afterRun = %v, want the failure naming the folder that keeps the work", err)
	}
	j.cleanup(ctx)
	if _, err := os.Stat(filepath.Join(j.project(), "out.txt")); err != nil {
		t.Errorf("the only copy of the work went with the job: %v", err)
	}
}

// A run canceled from the daemon still pushes its work, and a push that fails is reported.
func TestACanceledRunReportsWorkThatMissedTheDaemon(t *testing.T) {
	d := newDaemonFake(t)
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "claude"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	started := filepath.Join(t.TempDir(), "started")
	entry := filepath.Join(t.TempDir(), "agent.sh")
	writeFile(t, entry, "echo work > \"$OCTOPUS_ROOT/projects/app/out.txt\"\ntouch "+started+"\nsleep 30\n")
	d.r.cfg.Entrypoint = entry
	d.failPushes.Store(true)

	j := d.job(t, "task-1", false)
	j.every = 20 * time.Millisecond
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				d.cancel.Store(true)
				return
			}
		}
	}()
	j.execute(context.Background())
	res := <-d.finished
	if res.ExitCode != runner.ExitCanceled || !strings.Contains(res.Error, unsentRefs) {
		t.Fatalf("finish = %+v, want a canceled run that says where its work waits", res)
	}
}

// After a crash the next start saves what a Thulu project's clone holds, committed or not, before it
// removes the run's folder; a folder it cannot save stays.
func TestSweepSavesTheWorkACrashLeftInAClone(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	runs := filepath.Join(d.r.cfg.WorkDir, "runs")
	j := d.job(t, "task-1", false)
	j.root = filepath.Join(runs, "j1")
	stuck := d.job(t, "task-1", false)
	stuck.root = filepath.Join(runs, "j2")
	for _, dir := range []string{j.root, stuck.root} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(j.project(), "out.txt"), "work")
	if err := stuck.linkProject(ctx, stuck.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stuck.project(), "out.txt"), "other")
	writeFile(t, filepath.Join(stuck.project(), ".git", "objects", "info", "alternates"), t.TempDir()+"/objects\n")

	sweepLeftovers(ctx, runs)

	if got := gitIn(t, d.r.cfg.mirrorDir("p1"), "show", unsentRef("octopus-result-1", "j1")+":out.txt"); got != "work" {
		t.Errorf("the mirror keeps %q for the crashed run", got)
	}
	if _, err := os.Stat(j.root); !os.IsNotExist(err) {
		t.Errorf("the saved run's folder stayed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stuck.project(), "out.txt")); err != nil {
		t.Errorf("a run whose work could not be saved lost its folder: %v", err)
	}
}

// Thulu's clone commits even on a machine with no git identity and a signer that cannot sign.
func TestAThuluCloneCommitsWithoutTheMachinesIdentityOrSigner(t *testing.T) {
	d := newDaemonFake(t)
	ctx := context.Background()
	global := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, global, "[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = false\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	j := d.job(t, "task-1", false)
	if err := j.linkProject(ctx, j.spec.Projects[0]); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(j.project(), "out.txt"), "work")
	if pushed, err := j.afterRun(ctx, 0); err != nil || len(pushed) != 1 {
		t.Fatalf("afterRun = %+v, %v; want the work committed and pushed", pushed, err)
	}
	if got := gitIn(t, d.repo, "log", "-1", "--format=%ae", "octopus-result-1"); got != "noreply@thulu.dev" {
		t.Errorf("the commit is by %q, want Thulu's identity", got)
	}
}

// A signer nobody answers fails the commit instead of holding the run.
func TestACommitDoesNotWaitForeverOnTheSigner(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	signer := filepath.Join(tmp, "signer.sh")
	writeFile(t, signer, "#!/bin/sh\nsleep 30\n")
	if err := os.Chmod(signer, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(tmp, "gitconfig")
	writeFile(t, global, "[user]\n\tname = t\n\temail = t@t\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = "+signer+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := filepath.Join(tmp, "repo")
	gitIn(t, tmp, "init", "-q", repo)
	writeFile(t, filepath.Join(repo, "a.txt"), "a")
	defer func(d time.Duration) { commitTimeout = d }(commitTimeout)
	commitTimeout = 500 * time.Millisecond

	start := time.Now()
	if err := commitAll(ctx, repo, "msg"); err == nil {
		t.Error("a commit whose signer never answered succeeded")
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("the commit waited %s on the signer", took)
	}
}

// A first fetch of a large project outlasts the minute an incremental fetch gets, and still goes on
// while data keeps coming.
func TestTheFetchOfAThuluProjectHasNoFixedDeadline(t *testing.T) {
	d := newDaemonFake(t)
	d.slowPacks.Store(int64(2 * time.Second))
	defer func(d time.Duration) { fetchTimeout = d }(fetchTimeout)
	fetchTimeout = time.Second
	j := d.job(t, "task-1", false)
	if err := j.linkProject(context.Background(), j.spec.Projects[0]); err != nil {
		t.Fatalf("a fetch slower than fetchTimeout: %v", err)
	}
}

// A runner that crashed mid-turn left the engine running in the chat's folder; the chat's next turn
// stops it before it resumes the same session.
func TestAChatTurnStopsTheEngineACrashLeftRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are unix")
	}
	r := New(&Config{WorkDir: t.TempDir()}, "test")
	j := &job{r: r, root: r.cfg.chatRoot("chat-1"), spec: &runner.JobSpec{ID: "job2", InstanceID: "job2",
		LaunchKind: runner.LaunchKindChatTurn}}
	if err := os.MkdirAll(j.root, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(j.root, "entry.sh")
	writeFile(t, script, "sleep 60\n")
	cmd := exec.Command("sh", script)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	writeFile(t, filepath.Join(j.root, pidFile), strconv.Itoa(cmd.Process.Pid))

	if err := j.prepareChat(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer j.cleanupChat()

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine of the crashed turn is still running")
	}
}

// A chat turn's answer travels in its outputs. When they never reach the daemon, the turn ends lost
// with the reason instead of as a clean turn with no answer; a run's outputs are not its answer.
func TestATurnWhoseAnswerNeverArrivesEndsLost(t *testing.T) {
	defer func(d time.Duration) { reportBackoff = d }(reportBackoff)
	reportBackoff = time.Millisecond
	finished := make(chan runner.FinishRequest, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /runner/v1/jobs/{id}/outputs", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "tunnel down", http.StatusBadGateway)
	})
	mux.HandleFunc("POST /runner/v1/jobs/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		var res runner.FinishRequest
		_ = json.NewDecoder(r.Body).Decode(&res)
		finished <- res
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := &Runner{cfg: &Config{DaemonURL: srv.URL}, client: NewClient(srv.URL, runner.CredentialPrefix+"test", "test")}
	for _, kind := range []string{runner.LaunchKindChatTurn, ""} {
		j := &job{r: r, root: t.TempDir(), spec: &runner.JobSpec{ID: "j1", InstanceID: "i1", LaunchKind: kind}}
		if err := os.MkdirAll(filepath.Join(j.root, "runs", "i1"), 0o750); err != nil {
			t.Fatal(err)
		}
		j.report(context.Background(), runner.FinishRequest{ExitCode: 0})
		res := <-finished
		switch {
		case kind != "" && (res.ExitCode != runner.ExitLost || res.Error == ""):
			t.Errorf("a turn whose answer never arrived finished as %+v, want lost with the reason", res)
		case kind == "" && res.ExitCode != 0:
			t.Errorf("a run whose outputs never arrived finished as %+v, want its own exit code", res)
		}
	}
}
