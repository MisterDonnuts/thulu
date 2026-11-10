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
}

func (j *job) execute(ctx context.Context) {
	spec := j.spec
	log.Printf("[runner] %s", i18n.Log("hostrun.log.job_started", "job", spec.ID, "task", spec.TaskID,
		"stage", spec.Stage, "agent", spec.AgentName))
	// The report must outlive a Ctrl-C: the daemon has to hear how the run ended.
	reportCtx := context.WithoutCancel(ctx)
	res := runner.FinishRequest{ExitCode: runner.ExitLost}
	ship := newLogShipper(reportCtx, j.r.client, spec.ID)
	defer func() {
		ship.close()
		j.report(reportCtx, res)
		j.cleanup(reportCtx)
	}()
	if err := j.prepare(ctx); err != nil {
		res.Error = apperr.Text(err)
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", res.Error)
		return
	}
	code, err := j.run(ctx, ship)
	res.ExitCode = code
	if err != nil {
		res.Error = apperr.Text(err)
		_, _ = fmt.Fprintf(ship, "[runner] %s\n", res.Error)
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
	if code == runner.ExitCanceled {
		res.Error = ""
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
	if err := os.MkdirAll(filepath.Join(j.root, "tmp"), 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	if err := writeRuntime(j.root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(j.root, "runs", j.spec.InstanceID), 0o750); err != nil {
		return apperr.Wrap(err, "hostrun.errors.run_dir", "path", j.root)
	}
	j.wts = map[string]string{}
	for _, p := range j.spec.Projects {
		if err := j.linkProject(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (j *job) linkProject(ctx context.Context, p runner.JobProject) error {
	checkout := j.r.cfg.Projects[p.ID]
	if checkout == "" {
		return apperr.Precondition("hostrun.errors.project_unmapped", "project", p.Name, "id", p.ID)
	}
	unlock := j.r.lockRepo(p.ID)
	defer unlock()
	repo := j.r.cfg.mirrorDir(p.ID)
	if err := prepareMirror(ctx, p, checkout, repo); err != nil {
		return err
	}
	j.checkouts = append(j.checkouts, snapshotCheckout(ctx, checkout))
	name := filepath.Base(filepath.Clean("/" + p.Name))
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
		wt := filepath.Join(j.root, "ro", name)
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
	m := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
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
	m["OCTOPUS_LLM_SOURCE"] = runner.SourceRunnerLogin
	delete(m, "OCTOPUS_LOCAL_MCP_CONFIG")
	if c := j.r.cfg.LocalMCPConfig; c != "" {
		m["OCTOPUS_LOCAL_MCP_CONFIG"] = c
	}
	delete(m, "OCTOPUS_USER_SETTINGS")
	if j.r.cfg.UserSettings {
		m["OCTOPUS_USER_SETTINGS"] = "1"
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

const killGrace = 10 * time.Second

func (j *job) run(ctx context.Context, out io.Writer) (int, error) {
	bash := j.r.cfg.Bash
	if bash == "" {
		bash = "bash"
	}
	entry := filepath.Join(j.root, "kube-entrypoint.sh")
	if j.r.cfg.Entrypoint != "" {
		entry = j.r.cfg.Entrypoint
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	// G204: the runner's own runtime, or the script its operator named.
	cmd := exec.CommandContext(runCtx, bash, entry) //nolint:gosec // see above
	cmd.Dir = j.root
	cmd.Env = j.env()
	cmd.Stdout, cmd.Stderr = out, out
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
	go j.heartbeat(runCtx, stop)
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

// Commit even after a failed run so the work stays on the branch; push only after a clean one.
func (j *job) afterRun(ctx context.Context, code int) ([]runner.PushedBranch, error) {
	if j.spec.ReadOnly {
		return nil, nil
	}
	msg := fmt.Sprintf("%s: %s (task %s)", j.spec.Stage, j.spec.AgentName, j.spec.TaskID) //i18n:ignore git commit subject, same as the daemon's
	stagePush := j.spec.Env["OCTOPUS_STAGE_PUSH"] == "1"
	var pushed []runner.PushedBranch
	var errs []string
	for _, p := range j.spec.Projects {
		wt := j.wts[p.ID]
		if wt == "" {
			continue
		}
		if ok, head := onTaskBranch(ctx, wt, p.Branch); !ok {
			errs = append(errs, apperr.Text(apperr.Precondition("hostrun.errors.left_branch",
				"project", p.Name, "branch", p.Branch, "head", head)))
			continue
		}
		if _, err := commitAll(ctx, wt, msg); err != nil {
			errs = append(errs, apperr.Text(err))
			continue
		}
		if code != 0 || (!stagePush && !p.Push) {
			continue
		}
		unlock := j.r.lockRepo(p.ID)
		commit, remote, err := pushBranch(ctx, wt, p.Branch)
		unlock()
		if err != nil {
			errs = append(errs, apperr.Text(err))
			continue
		}
		pushed = append(pushed, runner.PushedBranch{ProjectID: p.ID, Branch: p.Branch, Commit: commit, Remote: remote})
	}
	if len(errs) > 0 {
		return pushed, apperr.Precondition("hostrun.errors.after_run", "err", strings.Join(errs, "; "))
	}
	return pushed, nil
}

const reportAttempts = 6

func (j *job) report(ctx context.Context, res runner.FinishRequest) {
	outDir := filepath.Join(j.root, "runs", j.spec.InstanceID)
	if _, err := os.Stat(outDir); err == nil {
		retry(ctx, func() error { return j.r.client.Outputs(ctx, j.spec.ID, outDir) }, "outputs", j.spec.ID)
	}
	retry(ctx, func() error { return j.r.client.Finish(ctx, j.spec.ID, res) }, "finish", j.spec.ID)
	log.Printf("[runner] %s", i18n.Log("hostrun.log.job_finished", "job", j.spec.ID, "code", res.ExitCode))
}

func retry(ctx context.Context, f func() error, what, jobID string) {
	wait := time.Second
	for i := 0; i < reportAttempts; i++ {
		err := f()
		if err == nil || isNotHeld(err) {
			return
		}
		log.Printf("[runner] %s", i18n.Log("hostrun.log.report_failed", "what", what, "job", jobID, "err", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func (j *job) cleanup(ctx context.Context) {
	for _, entry := range j.readOnly {
		repo, wt, _ := strings.Cut(entry, "\x00")
		removeWorktree(ctx, repo, wt)
	}
	removeSession(j.root)
	if !j.r.cfg.KeepRuns {
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
