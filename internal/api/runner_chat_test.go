package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chatruntime"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// chatTurnScript stands in for the runtime: it counts the turns the chat's folder has seen (the
// engine's session lives there), lists the task's code and answers in stream-json with the message.
const chatTurnScript = `set -eu
n=$(cat "$OCTOPUS_ROOT/turns" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$OCTOPUS_ROOT/turns"
code=$(cat "$OCTOPUS_ROOT/projects/shop/README.md" 2>/dev/null | head -n 1 || true)
echo "[runner] a banner line the parser skips"
echo "scripts talk on stderr" >&2
printf '{"type":"result","result":"turn %s: %s | %s","is_error":false,"session_id":"sess-%s"}\n' "$n" "$(cat "$CHAT_INPUT_FILE")" "$code" "$n"
`

type chatMachine struct {
	*machineRig
	l   *runnerLauncher
	ws  string
	cfg *hostrun.Config
}

// startChatMachine runs a real hostrun runner against the daemon with chatTurnScript as its runtime,
// and settles the launcher the way its loop would.
func startChatMachine(t *testing.T, script string) *chatMachine {
	t.Helper()
	m := newMachineRig(t)
	daemon := httptest.NewServer(m.h)
	t.Cleanup(daemon.Close)
	tmp := t.TempDir()
	entry := filepath.Join(tmp, "turn.sh")
	if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	tk, err := m.srv.taskStore.Get(t.Context(), m.taskID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &hostrun.Config{DaemonURL: daemon.URL, RunnerID: m.reg.RunnerID, Credential: m.reg.Credential,
		WorkDir: filepath.Join(tmp, "work"), Concurrency: 1, ChatConcurrency: 1, Entrypoint: entry}
	ctx := runHostRunner(t, cfg)
	l := m.srv.runnerRoute.runner
	go func() {
		for ctx.Err() == nil {
			l.settle(context.Background())
			time.Sleep(100 * time.Millisecond)
		}
	}()
	return &chatMachine{machineRig: m, l: l, ws: tk.WorkspaceID, cfg: cfg}
}

func (c *chatMachine) turn(chatID, taskID, text string) chatruntime.MachineTurn {
	return chatruntime.MachineTurn{
		WorkspaceID: c.ws, RunnerID: c.reg.RunnerID, ChatID: chatID, TaskID: taskID, AgentName: "manager",
		AgentType: "manager", Stdin: text,
		Env: map[string]string{"ENGINE": "claude-code", "STAGE": "chat", "TASK_ID": taskID,
			"OCTOPUS_API": "http://host.docker.internal:8585", "OCTOPUS_API_KEY": c.key},
	}
}

func (c *chatMachine) run(t *testing.T, turn chatruntime.MachineTurn) chatruntime.ExecOutput {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	out, err := c.l.RunTurn(ctx, turn)
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", out.ExitCode, out.Stdout, out.Stderr)
	}
	return out
}

func resultLine(t *testing.T, out chatruntime.ExecOutput) string {
	t.Helper()
	for _, l := range strings.Split(string(out.Stdout), "\n") {
		if strings.HasPrefix(l, `{"type":"result"`) {
			return l
		}
	}
	t.Fatalf("no result in stdout:\n%s", out.Stdout)
	return ""
}

// Cases 39, 41 and 57: a chat turn runs on the machine with the message as its input and comes back
// as the turn's stdout; the chat's folder outlives the turn, so the second turn finds the first's
// session; nothing reaches the task's report or the run logs, and no file of the turn stays behind.
func TestChatTurnRunsOnTheMachineAndKeepsItsFolder(t *testing.T) {
	c := startChatMachine(t, chatTurnScript)
	var reported, sunk atomic.Int32
	c.l.report = func(context.Context, string, task.RunnerReport) { reported.Add(1) }
	c.l.logSink = func(context.Context, *agent.Agent, string) { sunk.Add(1) }
	turn := c.turn("chat-mac", "", "hola")

	first := c.run(t, turn)
	if got := resultLine(t, first); !strings.Contains(got, `"turn 1: hola`) {
		t.Errorf("first answer = %s", got)
	}
	if !strings.Contains(string(first.Stderr), "scripts talk on stderr") {
		t.Errorf("stderr = %q, want the scripts' output", first.Stderr)
	}
	turn.Stdin = "otra"
	if got := resultLine(t, c.run(t, turn)); !strings.Contains(got, `"turn 2: otra`) {
		t.Errorf("second answer = %s, want the chat's folder kept between turns", got)
	}

	if reported.Load() != 0 || sunk.Load() != 0 {
		t.Errorf("reports = %d, log sinks = %d; a chat turn is no run of its task", reported.Load(), sunk.Load())
	}
	if exited, err := c.l.ScanExitedTaskAgents(t.Context()); err != nil || len(exited) != 0 {
		t.Errorf("exited agents = %+v, %v; want none", exited, err)
	}
	for _, dir := range []string{c.l.runsDir, c.l.logsDir} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s keeps %d entries after the turns", dir, len(entries))
		}
	}
	chatDir := filepath.Join(c.cfg.WorkDir, "chats", "chat-mac")
	if b, err := os.ReadFile(filepath.Join(chatDir, "turns")); err != nil || strings.TrimSpace(string(b)) != "2" {
		t.Errorf("chat folder turns = %q, %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(chatDir, "runs")); len(entries) != 0 {
		t.Errorf("the machine keeps %d turn outputs", len(entries))
	}
}

// Case 40: a task chat reads the task's code, which a Thulu project brings over /git/ with the chat's
// key; it runs read-only, so nothing comes back as a push.
func TestTaskChatOnAMachineReadsTheTasksCode(t *testing.T) {
	c := startChatMachine(t, chatTurnScript)
	if err := os.WriteFile(filepath.Join(c.wt.Path, "README.md"), []byte("shop readme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, c.wt.Path, "add", "README.md")
	gitT(t, c.wt.Path, "commit", "-q", "-m", "readme")
	if _, err := c.srv.taskStore.Update(t.Context(), c.taskID, task.Patch{WorktreePaths: map[string]string{c.projID: c.wt.Path}}); err != nil {
		t.Fatal(err)
	}

	var reported atomic.Int32
	c.l.report = func(context.Context, string, task.RunnerReport) { reported.Add(1) }
	refs := func() string { return gitT(t, c.proj.Path, "for-each-ref", "--format=%(refname) %(objectname)") }
	before := refs()

	out := c.run(t, c.turn("chat-task", c.taskID, "qué hay"))
	if got := resultLine(t, out); !strings.Contains(got, "| shop readme") {
		t.Errorf("answer = %s, want the task's code read", got)
	}
	if after := refs(); after != before || reported.Load() != 0 {
		t.Errorf("refs %q -> %q, reports %d; want no push", before, after, reported.Load())
	}
}

// A turn whose waiter gives up asks the machine to stop and says which machine did not answer.
func TestChatTurnThatTimesOutIsCanceled(t *testing.T) {
	c := startChatMachine(t, "sleep 30\n")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := c.l.RunTurn(ctx, c.turn("chat-slow", "", "hola")); err == nil ||
		!strings.Contains(err.Error(), "mac") {
		t.Fatalf("err = %v, want the machine named", err)
	}
	var canceled bool
	var specJSON string
	if err := c.srv.secretsStore.DB().QueryRowContext(t.Context(),
		`SELECT cancel, spec FROM runner_jobs WHERE launch_kind = $1`, runner.LaunchKindChatTurn).Scan(&canceled, &specJSON); err != nil {
		t.Fatal(err)
	}
	if !canceled {
		t.Error("the turn still runs with no one waiting")
	}
	// The machine stops the turn on its own too, should the cancel never reach it.
	var spec runner.JobSpec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.DeadlineSeconds < 1 || spec.DeadlineSeconds > 3 {
		t.Errorf("the turn's deadline_seconds = %d, want what was left of the waiter's 3 s", spec.DeadlineSeconds)
	}
}

// Case 46: with the machine's only run slot taken by a stage run, a chat turn still goes through on
// the chat pool.
func TestChatTurnRunsWhileTheRunSlotsAreFull(t *testing.T) {
	c := startChatMachine(t, `if [ "${STAGE:-}" != chat ]; then sleep 30; exit 0; fi
`+chatTurnScript)
	ctx := t.Context()
	tk, err := c.srv.taskStore.Get(ctx, c.taskID)
	if err != nil {
		t.Fatal(err)
	}
	tk.WorktreePaths = map[string]string{c.projID: c.wt.Path}
	env := map[string]string{"INSTANCE_ID": "dev-task-" + c.taskID + "-1", "TASK_ID": c.taskID, "STAGE": "in_progress",
		runner.RunnerIDEnv: c.reg.RunnerID, "OCTOPUS_API": "http://host.docker.internal:8585", "OCTOPUS_API_KEY": c.key}
	cid, _, _, err := c.l.LaunchForStage(ctx, tk, types.StageInProgress, &types.AgentType{Name: "dev", Type: "dev"},
		env, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		j, err := c.l.store.GetJob(ctx, runner.JobIDFromRun(cid))
		if err != nil {
			t.Fatal(err)
		}
		if j.State == runner.StateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stage run was never claimed: %s", j.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := resultLine(t, c.run(t, c.turn("chat-busy", "", "hola"))); !strings.Contains(got, `"turn 1: hola`) {
		t.Errorf("answer = %s", got)
	}
}

// Case 57 on its failure paths: a turn canceled while queued, lost mid-turn or lost across a restart
// loses its spec, and still reaches neither the task's report, the run logs nor the dispatcher.
func TestLostOrCanceledChatTurnsStayOutOfTheTask(t *testing.T) {
	cases := []struct {
		name     string
		waited   bool
		end      func(t *testing.T, f *runnerFixture, id string)
		wantCode int
	}{
		{"canceled while queued", true, func(t *testing.T, f *runnerFixture, id string) {
			if err := f.store.RequestCancel(t.Context(), id); err != nil {
				t.Fatal(err)
			}
		}, runner.ExitCanceled},
		{"lost mid-turn", true, claimAndExpire, runner.ExitLost},
		{"lost across a restart", false, claimAndExpire, runner.ExitLost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunnerFixture(t, "ws1")
			l := f.launcher
			var reported, sunk, reconciled atomic.Int32
			l.report = func(context.Context, string, task.RunnerReport) { reported.Add(1) }
			l.logSink = func(context.Context, *agent.Agent, string) { sunk.Add(1) }
			l.reconcile = func(context.Context) { reconciled.Add(1) }
			job := &runner.Job{RunnerID: f.reg.RunnerID, WorkspaceID: "ws1", TaskID: "task-1", AgentName: "manager",
				Stage: string(types.StageChat), LaunchKind: runner.LaunchKindChatTurn,
				Spec: runner.JobSpec{TaskID: "task-1", Stage: string(types.StageChat),
					LaunchKind: runner.LaunchKindChatTurn, ChatID: "chat1", Stdin: "hola"}}
			if err := f.store.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			got := -1
			if tc.waited {
				l.OnExit(t.Context(), job.RunID(), func(code int, _, _ string) { got = code })
			} else {
				l.started = time.Now().Add(time.Hour)
			}
			tc.end(t, f, job.ID)

			l.settle(t.Context())
			if exited, err := l.ScanExitedTaskAgents(t.Context()); err != nil || len(exited) != 0 {
				t.Errorf("exited agents = %+v, %v; want none", exited, err)
			}
			if tc.waited && got != tc.wantCode {
				t.Errorf("the turn's waiter got %d, want %d", got, tc.wantCode)
			}
			if reported.Load() != 0 || sunk.Load() != 0 || reconciled.Load() != 0 {
				t.Errorf("reports = %d, log sinks = %d, reconciles = %d; want none",
					reported.Load(), sunk.Load(), reconciled.Load())
			}
			if j, err := f.store.GetJob(t.Context(), job.ID); err != nil || !j.Reaped {
				t.Errorf("job = %+v, %v; want it reaped", j, err)
			}
		})
	}
}

func claimAndExpire(t *testing.T, f *runnerFixture, _ string) {
	t.Helper()
	spec, err := f.store.Claim(t.Context(), f.reg.RunnerID, runner.ProtocolVersion, nil, time.Millisecond, runner.ClaimChat)
	if err != nil || spec == nil {
		t.Fatalf("claim = %v, %v", spec, err)
	}
	time.Sleep(20 * time.Millisecond)
}

// Case 40 with the code in a folder of the machine: the task chat reads the task's project there,
// from a worktree of its own; what the turn writes reaches neither the machine's checkout nor its
// remote, and nothing is reported as a push.
func TestTaskChatOnAMachineReadsTheMachinesFolder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	f := newRunnerFixture(t, "ws1")
	ctx := t.Context()
	if _, err := f.store.Authenticate(ctx, f.reg.Credential, "test", runner.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	srv := &Server{runnerStore: f.store, runnerRoute: &routedLauncher{runner: f.launcher}, runsDir: f.runsDir}
	mux := http.NewServeMux()
	srv.registerRunnerRoutes(mux, func(_ auth.Role, h http.HandlerFunc) http.HandlerFunc { return h })
	daemon := httptest.NewServer(mux)
	t.Cleanup(daemon.Close)

	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	checkout := filepath.Join(tmp, "app")
	gitT(t, tmp, "init", "--quiet", "--bare", "-b", "main", remote)
	gitT(t, tmp, "clone", "--quiet", remote, checkout)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("app readme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, checkout, "add", "README.md")
	gitT(t, checkout, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--quiet", "-m", "readme")
	gitT(t, checkout, "push", "--quiet", "origin", "HEAD:main")
	machineEnv(t)
	entry := filepath.Join(tmp, "turn.sh")
	if err := os.WriteFile(entry, []byte(`set -eu
code=$(head -n 1 "$OCTOPUS_ROOT/projects/app/README.md")
echo "chat edit" > "$OCTOPUS_ROOT/projects/app/README.md"
printf '{"type":"result","result":"read: %s","is_error":false}\n' "$code"
`), 0o600); err != nil {
		t.Fatal(err)
	}

	tk := &task.Task{ID: "t1", WorkspaceID: "ws1", ProjectIDs: []string{f.project.ID}}
	f.launcher.task = func(context.Context, string) (*task.Task, error) { return tk, nil }
	var reported atomic.Int32
	f.launcher.report = func(context.Context, string, task.RunnerReport) { reported.Add(1) }
	cfg := &hostrun.Config{DaemonURL: daemon.URL, RunnerID: f.reg.RunnerID, Credential: f.reg.Credential,
		Projects: map[string]string{f.project.ID: checkout}, WorkDir: filepath.Join(tmp, "work"),
		Concurrency: 1, ChatConcurrency: 1, Entrypoint: entry}
	runCtx := runHostRunner(t, cfg)
	go func() {
		for runCtx.Err() == nil {
			f.launcher.settle(context.Background())
			time.Sleep(100 * time.Millisecond)
		}
	}()

	turnCtx, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	out, err := f.launcher.RunTurn(turnCtx, chatruntime.MachineTurn{
		WorkspaceID: "ws1", RunnerID: f.reg.RunnerID, ChatID: "chat-folder", TaskID: tk.ID, AgentName: "manager",
		AgentType: "manager", Stdin: "qué hay",
		Env: map[string]string{"ENGINE": "claude-code", "STAGE": "chat", "TASK_ID": tk.ID,
			"OCTOPUS_API": "http://host.docker.internal:8585"},
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "read: app readme") {
		t.Fatalf("exit %d, stdout %q, stderr %q; want the folder's code read", out.ExitCode, out.Stdout, out.Stderr)
	}
	if st := gitT(t, checkout, "status", "--porcelain"); st != "" {
		t.Errorf("the turn changed the machine's checkout: %q", st)
	}
	if refs := gitT(t, tmp, "--git-dir", remote, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" {
		t.Errorf("remote refs = %q, want only main", refs)
	}
	if reported.Load() != 0 {
		t.Errorf("the turn reached the task's report %d times", reported.Load())
	}
	if wts := gitT(t, checkout, "worktree", "list", "--porcelain"); strings.Count(wts, "worktree ") != 1 {
		t.Errorf("the turn's worktree stayed registered:\n%s", wts)
	}
}
