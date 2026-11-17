package hostrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
}

func (j *job) execute(ctx context.Context) {
	spec := j.spec
	log.Printf("[runner] %s", i18n.Log("hostrun.log.job_started", "job", spec.ID, "task", spec.TaskID,
		"stage", spec.Stage, "agent", spec.AgentName))
	// The report must outlive a Ctrl-C: the daemon has to hear how the run ended.
	reportCtx := context.WithoutCancel(ctx)
	// The lease covers the whole job, from the claim to after Finish: fetching, pushing and uploading
	// can outlast it too. A cancel from the daemon stops whatever the job is doing.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	leaseCtx, endLease := context.WithCancel(reportCtx)
	go j.heartbeat(leaseCtx, stop)
	res := runner.FinishRequest{ExitCode: runner.ExitLost}
	ship := newLogShipper(reportCtx, j.r.client, spec.ID)
	defer func() {
		ship.close()
		j.report(reportCtx, res)
		endLease()
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
	code, err := j.runJob(ctx, ship)
	endSteer()
	res.ExitCode = code
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
	res.Warnings = j.checkoutChanges(reportCtx)
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", w)
	}
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
	if err := os.MkdirAll(filepath.Join(j.root, "runs", j.spec.InstanceID), 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if j.spec.ChatTurn() {
		if err := j.writeChatInput(); err != nil {
			return err
		}
	}
	j.wts = map[string]string{}
	for _, p := range j.spec.Projects {
		if err := j.linkProject(ctx, p); err != nil {
			return err
		}
	}
	return nil
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
	m["OCTOPUS_TMP"] = tmp
	m["TMPDIR"] = tmp
	m[runner.HostRunEnv] = "1"
	if m[engine.LLMSourceEnv] == "" {
		m[engine.LLMSourceEnv] = runner.SourceRunnerLogin
	}
	delete(m, "OCTOPUS_LOCAL_MCP_CONFIG")
	if c := j.r.cfg.LocalMCPConfig; c != "" {
		m["OCTOPUS_LOCAL_MCP_CONFIG"] = c
	}
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

const killGrace = 10 * time.Second

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
	// G204: the runner's own runtime, or the script its operator named.
	cmd := exec.CommandContext(ctx, bash, entry) //nolint:gosec // see above
	cmd.Dir = j.root
	cmd.Env = j.env()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	setProcessGroup(cmd)
	done := make(chan struct{})
	var canceled atomic.Bool
	// The default Cancel kills bash alone; the engine and the tools it started are in bash's
	// process group.
	cmd.Cancel = func() error {
		canceled.Store(true)
		terminate(cmd)
		go func() {
			select {
			case <-done:
			case <-time.After(killGrace):
				kill(cmd)
			}
		}()
		return nil
	}
	if err := cmd.Start(); err != nil {
		return runner.ExitLost, apperr.Wrap(err, "hostrun.errors.start", "bash", bash)
	}
	logx.Warn(os.WriteFile(filepath.Join(j.root, pidFile), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600),
		"hostrun.log.pid_file_failed", "path", j.root)
	err := cmd.Wait()
	close(done)
	if canceled.Load() {
		return runner.ExitCanceled, nil
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

func (j *job) heartbeat(ctx context.Context, stop context.CancelFunc) {
	t := time.NewTicker(j.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hb, err := j.r.client.Heartbeat(ctx, j.spec.ID)
			if (err == nil && hb.Cancel) || isNotHeld(err) {
				stop()
				return
			}
		}
	}
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
	for _, p := range j.spec.Projects {
		wt := j.wts[p.ID]
		if wt == "" {
			continue
		}
		pb, err := j.saveProject(ctx, p, wt, msg, push || (code == 0 && p.Push))
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

func (j *job) report(ctx context.Context, res runner.FinishRequest) {
	outDir := filepath.Join(j.root, "runs", j.spec.InstanceID)
	if _, err := os.Stat(outDir); err == nil {
		err := retry(ctx, func() error { return j.r.client.Outputs(ctx, j.spec.ID, outDir) }, "outputs", j.spec.ID)
		logx.Warn(err, "hostrun.log.report_gave_up", "what", "outputs", "job", j.spec.ID)
		// A turn's answer travels in its outputs: without them the turn has none, whatever the engine
		// exited with.
		if err != nil && j.spec.ChatTurn() {
			res.ExitCode = runner.ExitLost
			res.Error = joinErr(res.Error, apperr.Text(apperr.Wrap(err, "hostrun.errors.turn_outputs")))
		}
	}
	err := retry(ctx, func() error { return j.r.client.Finish(ctx, j.spec.ID, res) }, "finish", j.spec.ID)
	logx.Warn(err, "hostrun.log.report_gave_up", "what", "finish", "job", j.spec.ID)
	log.Printf("[runner] %s", i18n.Log("hostrun.log.job_finished", "job", j.spec.ID, "code", res.ExitCode))
}

// retry returns the last error once the attempts run out. A job the daemon no longer holds is not
// an error: nobody waits for its report.
func retry(ctx context.Context, f func() error, what, jobID string) error {
	wait := reportBackoff
	var err error
	for i := 0; i < reportAttempts; i++ {
		if err = f(); err == nil || isNotHeld(err) {
			return nil
		}
		log.Printf("[runner] %s", i18n.Log("hostrun.log.report_failed", "what", what, "job", jobID, "err", err))
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
		return
	}
	removeSession(j.root)
	switch {
	case j.keep:
		log.Printf("[runner] %s", i18n.Log("hostrun.log.run_kept", "path", j.root))
	case !j.r.cfg.KeepRuns:
		logx.Warn(os.RemoveAll(j.root), "hostrun.log.remove_failed", "path", j.root)
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
