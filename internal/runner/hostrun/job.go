package hostrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MisterDonnuts/thulu/images"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

type job struct {
	r         *Runner
	spec      *runner.JobSpec
	every     time.Duration
	root      string
	wts       map[string]string
	readOnly  []string
	checkouts []checkoutState
	// The job's env for git calls that reach the daemon; built on first use.
	gitEnv []string
	// Lets go of the chat a turn holds.
	unlock func()
	// A clone holds work found nowhere else, so the job's folder stays for the next start to save.
	keep bool
	// How the run reaches the machine's own tooling (local_tools, policy).
	tools runTools
	// The heads a lock_tests run started from, by project (testlock.go).
	starts map[string]string
	// The processes the agent's script started (runtrack.go); nil until it starts.
	tracker *runTracker
	// What was still running after killRun gave up. The run's folder and record stay while it is not
	// empty, for the next sweep.
	survivors []procInfo
	// What the run left outside its folder that the runner kept: new /tmp entries it could not tie
	// to the run, containers created meanwhile (outside.go).
	leftWarnings []string
	// How long the daemon keeps the job without a heartbeat (ClaimResponse.LeaseSeconds).
	lease time.Duration
	// When the run must be over by the wall clock (runLimit). The heartbeat checks it too: a timer
	// does not count the time the machine slept.
	deadline time.Time
	// The run's tracker, for the janitor, which runs on another goroutine.
	trackerRef atomic.Pointer[runTracker]
	// The run's memory cap in MB, zero or less for none (memory.go).
	memLimit int64
	// The run's memory watcher while the script runs, for the floor check on another goroutine.
	memRef atomic.Pointer[memWatch]
	// Why the memory watcher stopped the run; nil when it did not.
	memStop *memStop
}

// The runner stops a run this long after the daemon's limit: the daemon's own stop, which says why on
// the task, normally comes first.
var deadlineMargin = 2 * time.Minute

var (
	errDeadline   = errors.New("run went over its time limit")        //i18n:ignore context cause, never shown
	errDaemonLost = errors.New("daemon did not answer the heartbeat") //i18n:ignore context cause, never shown
)

// stopReason is why the runner stopped the run on its own, from ctx's cause.
func stopReason(ctx context.Context) string {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errDeadline):
		return runner.StopDeadline
	case errors.Is(cause, errDaemonLost):
		return runner.StopDaemonLost
	}
	return ""
}

func (j *job) execute(ctx context.Context) {
	spec := j.spec
	log.Printf("[runner] %s", i18n.Log("hostrun.log.job_started", "job", spec.ID, "task", spec.TaskID,
		"stage", spec.Stage, "agent", spec.AgentName))
	// The report must outlive a Ctrl-C: the daemon has to hear how the run ended.
	reportCtx := context.WithoutCancel(ctx)
	runnerCtx := ctx
	// The lease covers the whole job, from the claim to after Finish: fetching, pushing and uploading
	// can outlast it too. A cancel from the daemon stops whatever the job is doing.
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	ctx, endTimer := j.withDeadline(ctx)
	defer endTimer()
	leaseCtx, endLease := context.WithCancel(reportCtx)
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		j.heartbeat(leaseCtx, stop)
	}()
	res := runner.FinishRequest{ExitCode: runner.ExitLost}
	ship := newLogShipper(reportCtx, j.r.client, spec.ID)
	defer func() {
		j.finish(ctx, runnerCtx.Err() != nil, ship, &res)
		endLease()
		// The heartbeat ends with the job: none keeps calling the daemon for a job already reported.
		<-beating
		j.cleanup(reportCtx)
	}()
	// Started before prepare: a message sent while the code is fetched waits in the queue.
	steerCtx, endSteer := context.WithCancel(ctx)
	defer endSteer()
	if spec.Env[runner.SteerEnabledEnv] == "1" {
		go j.steerLoop(steerCtx)
	}
	if err := j.prepare(ctx); err != nil {
		if ctx.Err() != nil {
			res.ExitCode = runner.ExitCanceled
			return
		}
		res.Error = apperr.Text(err)
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", res.Error)
		return
	}
	for _, line := range append(slices.Clone(j.tools.notes), j.tools.warnings...) {
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", line)
	}
	code, err := j.runJob(ctx, ship)
	endSteer()
	res.ExitCode = code
	if j.memStop != nil {
		res.StopReason = j.memStop.reason
	}
	if err != nil {
		res.Error = apperr.Text(err)
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", res.Error)
	}
	// A chat turn reads the code and has nothing to save.
	if spec.ChatTurn() {
		return
	}
	pushed, perr := j.afterRun(reportCtx, code)
	res.Pushed = pushed
	if perr != nil {
		res.Error = joinErr(res.Error, apperr.Text(perr))
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", apperr.Text(perr))
	}
	// After the commit: the daemon compares what the run left on the branch.
	res.TestFiles = j.testFiles(reportCtx)
	changed := j.checkoutChanges(reportCtx)
	res.Warnings = append(slices.Clone(j.tools.warnings), changed...)
	for _, w := range changed {
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", w)
	}
}

// The longest a run whose job carries no limit may take: a daemon from before chat turns carried
// one, or a workspace without a limit. The run's key lives 24 hours (settings.MaxRunMinutesCeiling),
// so nothing a run does can succeed after it.
var fallbackDeadline = 24 * time.Hour

// runLimit is how long the run may take on this machine: deadlineMargin after the daemon's limit, or
// fallbackDeadline when the job carries none.
func (j *job) runLimit() time.Duration {
	if j.spec.DeadlineSeconds <= 0 {
		return fallbackDeadline
	}
	return time.Duration(j.spec.DeadlineSeconds)*time.Second + deadlineMargin
}

// withDeadline ends ctx once the run's limit passes. Every run has one.
func (j *job) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	limit := j.runLimit()
	// Round(0): the wall clock, which keeps counting while the machine sleeps.
	j.deadline = time.Now().Round(0).Add(limit)
	return context.WithTimeoutCause(ctx, limit, errDeadline)
}

// finish reports how the run ended, after run killed what it started. ctx is the job's own,
// stopping says the runner is on its way out.
func (j *job) finish(ctx context.Context, stopping bool, ship *logShipper, res *runner.FinishRequest) {
	if res.ExitCode == runner.ExitCanceled {
		res.StopReason = stopReason(ctx)
	}
	switch res.StopReason {
	case runner.StopDeadline:
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", i18n.Log("hostrun.log.deadline", "job", j.spec.ID, "limit", j.runLimit()))
	case runner.StopDaemonLost:
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", i18n.Log("hostrun.log.daemon_lost", "job", j.spec.ID, "lease", j.lease))
	}
	if len(j.survivors) > 0 {
		res.Warnings = append(res.Warnings, i18n.Text("hostrun.warnings.run_survivors",
			"count", len(j.survivors), "pids", survivorList(j.survivors)))
	}
	res.Warnings = append(res.Warnings, j.leftWarnings...)
	// A runner on its way out (launchd and systemd give it 60 s) or a daemon that stopped answering
	// get one short try; what does not reach the daemon waits in the mirror.
	quick := stopping || res.StopReason == runner.StopDaemonLost
	ship.close(quick)
	j.report(context.WithoutCancel(ctx), *res, quick)
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

var branchDir = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (j *job) prepare(ctx context.Context) error {
	if err := checkJobEngine(j.spec.Env["ENGINE"]); err != nil {
		return err
	}
	if j.spec.ChatTurn() {
		if err := j.prepareChat(ctx); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(j.root, "tmp"), 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if err := createSteerQueue(j.root); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if err := writeRuntime(j.root); err != nil {
		return err
	}
	if err := j.writeGatesCLI(); err != nil {
		return err
	}
	tools, err := j.planTools(ctx)
	if err != nil {
		return err
	}
	j.tools = tools
	if err := os.MkdirAll(filepath.Join(j.root, "runs", j.spec.InstanceID), 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if j.spec.ChatTurn() {
		if err := j.writeChatInput(); err != nil {
			return err
		}
	}
	j.wts = map[string]string{}
	for i := range j.spec.Projects {
		p := &j.spec.Projects[i]
		if err := j.linkProject(ctx, *p); err != nil {
			return err
		}
	}
	j.recordStarts(ctx)
	if err := os.MkdirAll(j.r.cfg.gatesCacheDir(), 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.r.cfg.gatesCacheDir())
	}
	return j.writeGatesProfile()
}

// The name the agent finds the project under, and the one /git/ serves it by.
func mountName(p runner.JobProject) string { return filepath.Base(filepath.Clean("/" + p.Name)) }

func (j *job) linkProject(ctx context.Context, p runner.JobProject) error {
	if p.FromDaemon() {
		return j.linkDaemonProject(ctx, p, filepath.Join(j.root, "projects", mountName(p)))
	}
	checkout, err := j.r.cfg.checkoutFor(ctx, p)
	if err != nil {
		return err
	}
	unlock := j.r.lockRepo(p.ID)
	defer unlock()
	repo := j.r.cfg.mirrorDir(p.ID)
	if merr := prepareMirror(ctx, p, checkout, repo); merr != nil {
		return merr
	}
	j.checkouts = append(j.checkouts, snapshotCheckout(ctx, checkout))
	name := mountName(p)
	wt, err := j.projectWorktree(ctx, p, repo, checkout, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(j.root, "projects"), 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if err := os.Symlink(wt, filepath.Join(j.root, "projects", name)); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	return nil
}

func prepareMirror(ctx context.Context, p runner.JobProject, checkout, repo string) error {
	origin, err := ensureMirror(ctx, checkout, repo)
	if err != nil {
		return err
	}
	return checkProject(ctx, p, checkout, repo, origin)
}

func checkProject(ctx context.Context, p runner.JobProject, checkout, repo, origin string) error {
	if err := checkRepo(p, checkout, origin); err != nil {
		return err
	}
	if err := validRef(ctx, repo, p.Branch); err != nil {
		return err
	}
	return validRef(ctx, repo, p.DefaultBranch)
}

func (j *job) projectWorktree(ctx context.Context, p runner.JobProject, repo, checkout, name string) (string, error) {
	if j.spec.ReadOnly {
		// Per job: a chat's folder outlives its turns, and a worktree a crash left registered at a
		// path blocks that path.
		wt := filepath.Join(j.root, "ro", j.spec.ID, name)
		if err := readOnlyWorktree(ctx, repo, wt, p.Branch, p.DefaultBranch); err != nil {
			return "", err
		}
		j.readOnly = append(j.readOnly, repo+"\x00"+wt)
		return wt, nil
	}
	files := j.r.cfg.ProjectFiles[p.ID]
	if err := excludeProjectFiles(repo, files); err != nil {
		return "", err
	}
	wt := filepath.Join(j.r.cfg.WorkDir, "worktrees", branchDir.ReplaceAllString(p.Branch, "_"), name)
	created, err := ensureTaskWorktree(ctx, repo, wt, p.Branch, p.DefaultBranch)
	if err != nil {
		return "", err
	}
	if created {
		if err := copyProjectFiles(checkout, wt, files); err != nil {
			return "", err
		}
	}
	j.wts[p.ID] = wt
	return wt, nil
}

func (j *job) checkoutChanges(ctx context.Context) []string {
	var out []string
	for _, before := range j.checkouts {
		after := snapshotCheckout(ctx, before.path)
		if after.head != before.head || after.status != before.status {
			out = append(out, i18n.Text("hostrun.warnings.checkout_changed", "path", before.path))
		}
	}
	return out
}

// Lays the runtime out the way the agent images bake it; keep the renames in step with the images.
func writeRuntime(root string) error {
	err := fs.WalkDir(images.Runtime, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".test.js") {
			return err
		}
		dst := p
		switch p {
		case "entrypoint-base.sh":
			dst = "entrypoint.sh"
		case "kube-agent-entrypoint.sh":
			dst = "kube-entrypoint.sh"
		}
		b, err := images.Runtime.ReadFile(p)
		if err != nil {
			return apperr.Wrap(err, "hostrun.errors.runtime", "file", p)
		}
		full := filepath.Join(root, filepath.FromSlash(dst))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return apperr.Wrap(err, "hostrun.errors.runtime", "file", p)
		}
		if err := os.WriteFile(full, b, 0o755); err != nil {
			return apperr.Wrap(err, "hostrun.errors.runtime", "file", path.Base(p))
		}
		return nil
	})
	return apperr.Wrap(err, "hostrun.errors.runtime_copy", "path", root)
}

// The job's env names the daemon by the URL containers use; rewrite it to the address this runner
// reaches it by.
func (j *job) env() []string {
	m := loginEnv(j.spec.Env["ENGINE"])
	delete(m, engine.LLMSourceEnv)
	from := strings.TrimRight(j.spec.Env["OCTOPUS_API"], "/")
	to := strings.TrimRight(j.r.cfg.DaemonURL, "/")
	for k, v := range j.spec.Env {
		if from != "" && from != to {
			v = strings.ReplaceAll(v, from, to)
		}
		m[k] = v
	}
	tmp := filepath.Join(j.root, "tmp")
	m["OCTOPUS_ROOT"] = j.root
	// A chat turn's own job id, not the chat's: each turn's processes end with the turn.
	m[runner.RunIDEnv] = j.spec.ID
	m["OCTOPUS_TMP"] = tmp
	// TMPDIR for most tools, TMP and TEMP for those that read only them, java.io.tmpdir for the JVM
	// (Gradle, Android builds), which reads none of them. Claude Code keeps its own folder in /tmp
	// whatever these say; removeSession clears it.
	m["TMPDIR"], m["TMP"], m["TEMP"] = tmp, tmp, tmp
	m["JAVA_TOOL_OPTIONS"] = withJavaTmp(m["JAVA_TOOL_OPTIONS"], tmp)
	m[runner.HostRunEnv] = "1"
	if m[engine.LLMSourceEnv] == "" {
		m[engine.LLMSourceEnv] = runner.SourceRunnerLogin
	}
	j.tools.env(m)
	// From the spec or the machine's own environment; a value that caps the heap already stays.
	if opts := withHeapCap(m["NODE_OPTIONS"], j.memLimit); opts != "" {
		m["NODE_OPTIONS"] = opts
	}
	j.gatesEnv(m)
	delete(m, "OCTOPUS_USER_SETTINGS")
	if j.r.cfg.UserSettings {
		m["OCTOPUS_USER_SETTINGS"] = "1"
	}
	delete(m, chatInputEnv)
	if j.spec.ChatTurn() {
		m[chatInputEnv] = j.chatInputPath()
	}
	return envList(m)
}

// withJavaTmp adds the run's temp folder to JAVA_TOOL_OPTIONS after what it already holds: the JVM
// takes the last -D of a name, so the run's folder wins over one the machine sets.
func withJavaTmp(opts, tmp string) string {
	flag := "-Djava.io.tmpdir=" + tmp
	if strings.ContainsAny(tmp, " \t") {
		flag = `"` + flag + `"`
	}
	return strings.TrimSpace(opts + " " + flag)
}

// How long Wait waits, after a cancel or after bash exits, for a child that still holds the run's
// output pipe; then it closes the pipe and returns.
var killGrace = 10 * time.Second

func (j *job) runJob(ctx context.Context, ship io.Writer) (int, error) {
	if j.spec.ChatTurn() {
		return j.runTurn(ctx, ship)
	}
	return j.run(ctx, ship, ship)
}

func (j *job) run(ctx context.Context, stdout, stderr io.Writer) (int, error) {
	bash := j.r.cfg.Bash
	if bash == "" {
		bash = "bash"
	}
	entry := filepath.Join(j.root, "kube-entrypoint.sh")
	if j.r.cfg.Entrypoint != "" {
		entry = j.r.cfg.Entrypoint
	}
	name, args := bash, []string{entry}
	// With a systemd scope the kernel holds the run's memory cap; otherwise the watcher does.
	var scope *runScope
	if j.memLimit > 0 && j.r.scope.ok {
		scope = newRunScope(j.spec.ID)
		name, args = scope.command(bash, entry, j.memLimit)
	}
	// G204: the runner's own runtime, or the script its operator named.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // see above
	cmd.Dir = j.root
	cmd.Env = j.env()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	setProcessGroup(cmd)
	var canceled atomic.Bool
	// The default Cancel kills bash alone; the engine and the tools it started are in bash's
	// process group. TERM lets them stop; killRun below kills whatever did not.
	cmd.Cancel = func() error {
		canceled.Store(true)
		terminate(cmd)
		return nil
	}
	// A child that keeps the output pipe would otherwise hold Wait, and the job, forever.
	cmd.WaitDelay = killGrace
	outside := watchOutside(ctx, j.spec.ID, j.root, slices.Collect(maps.Values(j.wts)))
	if err := cmd.Start(); err != nil {
		return runner.ExitLost, apperr.Wrap(err, "hostrun.errors.start", "bash", bash)
	}
	logx.Warn(os.WriteFile(filepath.Join(j.root, pidFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600),
		"hostrun.log.pid_file_failed", "path", j.root)
	j.tracker = j.track(cmd.Process.Pid)
	if scope != nil {
		scope.started(cmd.Process.Pid)
	}
	mem := j.watchMemory(ctx, scope, stderr)
	err := cmd.Wait()
	// A kill the watcher started finishes before the runner looks for what is left.
	mem.stop()
	// Before scope.kill, which drops the failed unit that says the kernel stopped it.
	mem.noteKernelKill(context.WithoutCancel(ctx))
	j.memRef.Store(nil)
	// Every way out passes here, before the report: what the run started ends with it, whether it
	// stayed in bash's group or not.
	j.tracker.halt()
	cwds, commands := j.memberCwds(context.WithoutCancel(ctx))
	j.survivors = killRun(context.WithoutCancel(ctx), j.tracker, stderr)
	if scope != nil {
		// What the tracker missed is still in the scope's cgroup.
		scope.kill(context.WithoutCancel(ctx))
		j.survivors = stillRunning(j.r.procOS(), j.survivors)
	}
	j.leftWarnings = outside.finish(context.WithoutCancel(ctx), cwds, commands, len(j.survivors) > 0)
	for _, w := range j.leftWarnings {
		_, _ = fmt.Fprintf(stderr, "[runner] %s\n", w)
	}
	if s := mem.result(); s != nil {
		j.memStop = s
		return memoryExitCode, s.err
	}
	if canceled.Load() {
		return runner.ExitCanceled, nil
	}
	// bash exited 0 but a child kept the pipe past WaitDelay.
	if errors.Is(err, exec.ErrWaitDelay) {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code, nil
		}
		return 137, nil
	}
	if err != nil {
		return runner.ExitLost, apperr.Wrap(err, "hostrun.errors.wait")
	}
	return 0, nil
}

// memberCwds reads, before killRun stops them, where the run's processes work, and their names: a
// folder in /tmp that one of them works in is the run's.
func (j *job) memberCwds(ctx context.Context) (cwds, commands map[int]string) {
	procs := j.r.procOS()
	if procs.cwds == nil {
		return nil, nil
	}
	j.tracker.refresh(true)
	members := j.tracker.Members()
	pids := make([]int, 0, len(members))
	commands = map[int]string{}
	for _, m := range members {
		pids = append(pids, m.PID)
		commands[m.PID] = m.Comm
	}
	return procs.cwds(ctx, pids), commands
}

// track starts following the processes of the script whose pid is leader.
func (j *job) track(leader int) *runTracker {
	kind := "run"
	if j.spec.ChatTurn() {
		kind = "chat"
	}
	file := ""
	if j.r.cfg.WorkDir != "" {
		file = filepath.Join(j.r.cfg.WorkDir, activeDir, unsafeName.ReplaceAllString(j.spec.ID, "_")+".json")
	}
	t := newRunTracker(j.r.procOS(), j.spec.ID, kind, j.root, file, leader)
	j.trackerRef.Store(t)
	t.start()
	return t
}

// heartbeat keeps the job's lease. A cancel or a job the daemon no longer holds stops the job; so
// does a daemon that answers nothing for longer than the lease plus daemonRestartGrace, by the wall
// clock, so a daemon that never comes back does not leave the run going until its deadline.
func (j *job) heartbeat(ctx context.Context, stop context.CancelCauseFunc) {
	t := time.NewTicker(j.every)
	defer t.Stop()
	l := leaseState{lease: j.lease, lastOK: time.Now().Round(0)}
	if l.lease <= 0 {
		l.lease = runner.DefaultLease
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if end, cause := j.beat(ctx, &l); end {
			stop(cause)
			return
		}
	}
}

// daemonRestartGrace is how long past the lease the runner keeps a run while the daemon answers
// nothing. A daemon that starts gives every running job a fresh lease (runner.Store.AdoptRunning), so
// a restart slower than the lease (an upgrade, an image pull, a pod rescheduled) must not cost the
// run. A daemon that stayed up and called the job lost answers job_not_held to the first heartbeat
// that reaches it, which stops the run at once.
var daemonRestartGrace = 5 * time.Minute

type leaseState struct {
	lease    time.Duration
	lastOK   time.Time
	failures int
}

// beat sends one heartbeat and tells whether the job must stop, and why (nil: the daemon said so).
func (j *job) beat(ctx context.Context, l *leaseState) (bool, error) {
	if !j.deadline.IsZero() && time.Now().Round(0).After(j.deadline) {
		return true, errDeadline
	}
	// A call that hangs counts as a failed one once the lease is gone.
	callCtx, cancel := context.WithTimeout(ctx, l.lease)
	hb, err := j.r.client.Heartbeat(callCtx, j.spec.ID)
	cancel()
	switch {
	case (err == nil && hb.Cancel) || isNotHeld(err):
		return true, nil
	case err == nil:
		l.lastOK, l.failures = time.Now().Round(0), 0
		if hb.LeaseSeconds > 0 {
			l.lease = time.Duration(hb.LeaseSeconds) * time.Second
		}
		return false, nil
	case ctx.Err() != nil:
		return false, nil
	}
	l.failures++
	gone := time.Now().Round(0).Sub(l.lastOK)
	if gone <= l.lease+daemonRestartGrace {
		return false, nil
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.daemon_lost_stop", "job", j.spec.ID,
		"failures", l.failures, "gone", gone.Round(time.Second), "err", err))
	return true, errDaemonLost
}

// Commit even after a failed run so the work stays on the branch; push a folder's branch only after a
// clean run. A project from the daemon goes back after every run, or the machine keeps its work until
// it can (sendResult).
func (j *job) afterRun(ctx context.Context, code int) ([]runner.PushedBranch, error) {
	if j.spec.ReadOnly {
		return nil, nil
	}
	msg := fmt.Sprintf("%s: %s (task %s)", j.spec.Stage, j.spec.AgentName, j.spec.TaskID) //i18n:ignore git commit subject, same as the daemon's
	push := code == 0 && j.spec.Env["OCTOPUS_STAGE_PUSH"] == "1"
	var pushed []runner.PushedBranch
	var errs []string
	for i := range j.spec.Projects {
		p := &j.spec.Projects[i]
		wt := j.wts[p.ID]
		if wt == "" {
			continue
		}
		pb, err := j.saveProject(ctx, *p, wt, msg, push || (code == 0 && p.Push))
		if err != nil {
			errs = append(errs, apperr.Text(err))
		}
		if pb != nil {
			pushed = append(pushed, *pb)
		}
	}
	if len(errs) > 0 {
		return pushed, apperr.Precondition("hostrun.errors.after_run", "err", strings.Join(errs, "; "))
	}
	return pushed, nil
}

// saveProject commits what the agent left in wt and pushes it when it has to go somewhere.
func (j *job) saveProject(ctx context.Context, p runner.JobProject, wt, msg string, push bool) (*runner.PushedBranch, error) {
	// A Thulu project's clone goes with the job, so its HEAD is saved wherever the agent left it.
	if ok, head := onTaskBranch(ctx, wt, p.Branch); !ok && !p.FromDaemon() {
		return nil, apperr.Precondition("hostrun.errors.left_branch", "project", p.Name, "branch", p.Branch, "head", head)
	}
	err := commitAll(ctx, wt, msg)
	if p.FromDaemon() {
		return j.sendResult(ctx, p, wt, err)
	}
	if err != nil {
		return nil, err
	}
	if !push {
		return nil, nil
	}
	unlock := j.r.lockRepo(p.ID)
	defer unlock()
	commit, remote, err := pushBranch(ctx, wt, p.Branch)
	if err != nil {
		return nil, err
	}
	return &runner.PushedBranch{ProjectID: p.ID, Branch: p.Branch, Commit: commit, Remote: remote}, nil
}

// sendResult pushes a Thulu project's work to the daemon. Work that does not get there waits in the
// mirror for the task's next job; a clone holding work that is in neither place keeps the job's folder.
func (j *job) sendResult(ctx context.Context, p runner.JobProject, wt string, commitErr error) (*runner.PushedBranch, error) {
	err := commitErr
	if err == nil {
		pb, perr := j.pushResult(ctx, p, wt)
		if perr == nil {
			return &pb, nil
		}
		err = perr
	}
	ref := unsentRef(j.spec.ResultRef, j.spec.ID)
	unlock := j.r.lockRepo(p.ID)
	serr := saveUnsent(ctx, wt, j.r.cfg.mirrorDir(p.ID), ref)
	unlock()
	if serr != nil || commitErr != nil {
		j.keep = true
		return nil, apperr.Wrap(err, "hostrun.errors.result_kept", "project", p.Name, "path", wt)
	}
	return nil, apperr.Wrap(err, "hostrun.errors.result_unsent", "project", p.Name, "ref", ref)
}

const reportAttempts = 6

// The wait after the first failed report; it doubles after each one.
var reportBackoff = time.Second

// How long each call of a quick report may take: one try, no retry.
var quickReportTimeout = 10 * time.Second

// report sends the run's outputs and its end. quick makes each call one try of quickReportTimeout.
func (j *job) report(ctx context.Context, res runner.FinishRequest, quick bool) {
	attempts := reportAttempts
	call := func(f func(context.Context) error) func() error {
		return func() error { return f(ctx) }
	}
	if quick {
		attempts = 1
		call = func(f func(context.Context) error) func() error {
			return func() error {
				cctx, cancel := context.WithTimeout(ctx, quickReportTimeout)
				defer cancel()
				return f(cctx)
			}
		}
	}
	outDir := filepath.Join(j.root, "runs", j.spec.InstanceID)
	if _, err := os.Stat(outDir); err == nil {
		var skipped []runner.SkippedOutput
		err := retry(ctx, attempts, call(func(cctx context.Context) error {
			var oerr error
			skipped, oerr = j.r.client.Outputs(cctx, j.spec.ID, outDir)
			return oerr
		}), "outputs", j.spec.ID)
		logx.Warn(err, "hostrun.log.report_gave_up", "what", "outputs", "job", j.spec.ID)
		// The daemon reads the run's verdict from its outputs: it must know when they did not all
		// arrive, or it blames the agent for a verdict the upload lost.
		res.OutputsSkipped = skipped
		if err != nil {
			res.OutputsError = apperr.Text(err)
		}
		for _, s := range skipped {
			log.Printf("[runner] %s", i18n.Log("hostrun.log.output_skipped", "job", j.spec.ID, "path", s.Path, "size", s.Size))
		}
		// A turn's answer travels in its outputs: without them the turn has none, whatever the engine
		// exited with.
		if err != nil && j.spec.ChatTurn() {
			res.ExitCode = runner.ExitLost
			res.Error = joinErr(res.Error, apperr.Text(apperr.Wrap(err, "hostrun.errors.turn_outputs")))
		}
	}
	err := retry(ctx, attempts, call(func(cctx context.Context) error { return j.r.client.Finish(cctx, j.spec.ID, res) }),
		"finish", j.spec.ID)
	logx.Warn(err, "hostrun.log.report_gave_up", "what", "finish", "job", j.spec.ID)
	log.Printf("[runner] %s", i18n.Log("hostrun.log.job_finished", "job", j.spec.ID, "code", res.ExitCode))
}

// retry returns the last error once the attempts run out. A job the daemon no longer holds is not
// an error: nobody waits for its report.
func retry(ctx context.Context, attempts int, f func() error, what, jobID string) error {
	wait := reportBackoff
	var err error
	for i := 0; i < attempts; i++ {
		if err = f(); err == nil || isNotHeld(err) {
			return nil
		}
		log.Printf("[runner] %s", i18n.Log("hostrun.log.report_failed", "what", what, "job", jobID, "err", err))
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait *= 2
	}
	return err
}

func (j *job) cleanup(ctx context.Context) {
	for _, entry := range j.readOnly {
		repo, wt, _ := strings.Cut(entry, "\x00")
		removeWorktree(ctx, repo, wt)
	}
	if j.spec.ChatTurn() {
		j.cleanupChat()
		j.dropRecord()
		return
	}
	// Something of the run still runs: its folder, pid file and record stay for the next sweep.
	if len(j.survivors) > 0 {
		log.Printf("[runner] %s", i18n.Log("hostrun.log.run_kept_alive", "job", j.spec.ID, "path", j.root, "count", len(j.survivors)))
		return
	}
	removeSession(j.root)
	j.pruneBuildCaches()
	switch {
	case j.keep:
		log.Printf("[runner] %s", i18n.Log("hostrun.log.run_kept", "path", j.root))
	case !j.r.cfg.KeepRuns:
		logx.Warn(os.RemoveAll(j.root), "hostrun.log.remove_failed", "path", j.root)
		j.removedAgain(ctx)
	}
	j.dropRecord()
}

// After a removal, wait this long and look again: a process of the run that outlived the kill (or one
// it missed) writes in its TMPDIR and brings the folder back.
var recheckAfter = time.Second

func (j *job) removedAgain(ctx context.Context) {
	if j.tracker == nil {
		return
	}
	time.Sleep(recheckAfter)
	if _, err := os.Stat(j.root); err != nil {
		return
	}
	log.Printf("[runner] %s", i18n.Log("hostrun.log.root_recreated", "job", j.spec.ID, "path", j.root))
	j.survivors = killRun(ctx, j.tracker, nil)
	if len(j.survivors) > 0 {
		return
	}
	logx.Warn(os.RemoveAll(j.root), "hostrun.log.remove_failed", "path", j.root)
}

// dropRecord removes the job's record and pid file once nothing of the run is left running.
func (j *job) dropRecord() {
	if j.tracker == nil || len(j.survivors) > 0 {
		return
	}
	j.tracker.removeRecord()
	if err := os.Remove(filepath.Join(j.root, pidFile)); err != nil && !os.IsNotExist(err) {
		logx.Warn(err, "hostrun.log.remove_failed", "path", filepath.Join(j.root, pidFile))
	}
}

// Claude Code replaces these characters with "-" in its project directory names.
var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// Must match claude_projects_dir in images/snippets/engines/claude-code.sh.
func claudeProjectsDir(dir string) string {
	conf := os.Getenv("CLAUDE_CONFIG_DIR")
	if conf == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		conf = filepath.Join(home, ".claude")
	}
	return filepath.Join(conf, "projects", nonAlnum.ReplaceAllString(dir, "-"))
}
