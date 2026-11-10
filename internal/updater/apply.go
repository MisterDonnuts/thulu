package updater

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/deployfiles"
	"github.com/MisterDonnuts/thulu/internal/fsx"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/kept"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

const composeService = "octopus"

const minFreeBytes = 1 << 30

const maxBundle = 64 << 20

func (u *updateRun) apply(ctx context.Context, latest bool) error {
	u.say("updater.cli.plan_install", "install", u.in.String())
	u.say("updater.cli.plan_running", "running", describeRunning(u.in))

	version, err := u.targetVersion(ctx, latest)
	if err != nil {
		return err
	}
	m, err := u.manifest(ctx, version)
	if err != nil {
		return err
	}
	u.say("updater.cli.plan_target", "version", m.Version, "channel", m.Channel)

	prev, err := LoadState(u.in.Root)
	if err != nil {
		return err
	}
	current := u.installedVersion(prev)
	if latest && current == m.Version && u.opts.Version == "" {
		u.say("updater.cli.already_on", "version", m.Version)
		return nil
	}
	if done, derr := u.delegate(ctx, m, u.delegatedArgs("apply", "-version", m.Version)); done || derr != nil {
		return derr
	}

	unlock, err := acquireLock(u.in.Root, u.opts.Operator, u.deps.now())
	if err != nil {
		return err
	}
	defer unlock()

	st := u.newState(prev, current, m)
	var res Result
	var runErr error
	if u.in.Mode == initwizard.ModeKubernetes {
		res, runErr = u.applyKube(ctx, m, current, st)
	} else {
		res, runErr = u.applyDocker(ctx, m, current, st, prev)
	}
	if res == "" { // empty Result: declined at the prompt, nothing ran, nothing to record
		return runErr
	}
	u.finish(ctx, st, res, runErr)
	return runErr
}

func (u *updateRun) newState(prev *State, current string, m *release.Manifest) *State {
	st := &State{
		StartedAt:   u.deps.now(),
		From:        Ref{Version: current},
		To:          Ref{Version: m.Version, Image: m.Images["octopus"]},
		ComposeFile: u.in.ComposeFile,
		Mode:        string(u.in.Mode),
		Operator:    u.opts.Operator,
		RollbackTo:  m.RollbackTo,
	}
	if st.Mode == "" {
		st.Mode = string(initwizard.ModeDocker)
	}
	if prev != nil {
		st.Kept = prev.Kept
	}
	return st
}

func (u *updateRun) finish(ctx context.Context, st *State, res Result, runErr error) {
	st.Result = res
	st.FinishedAt = u.deps.now()
	st.UpdatedAt = st.FinishedAt
	if runErr != nil && st.Detail == "" {
		st.Detail = firstLine(apperr.Text(runErr))
	}
	if err := SaveState(u.in.Root, st); err != nil {
		u.say("updater.cli.write_failed", "path", StateFile, "err", apperr.Text(err))
	}
	u.reportEvent(ctx, Event{
		From: st.From.Version, To: st.To.Version, Result: res, Mode: st.Mode, Operator: st.Operator,
		StartedAt: st.StartedAt, FinishedAt: st.FinishedAt, Detail: st.Detail,
	})
}

func checkUpgradePath(m *release.Manifest, current string) error {
	switch {
	case current == m.Version:
		return nil
	case current == "":
		return apperr.Unavailable("updater.errors.running_version_unknown")
	case !isVersion(current):
		return apperr.Precondition("updater.errors.unstamped_build", "current", current, "version", m.Version)
	case semver.Newer(current, m.Version):
		return apperr.Invalid("updater.errors.target_older", "version", m.Version, "current", current)
	case !m.CanUpgradeFrom(current):
		return apperr.Precondition("updater.errors.upgrade_path", "version", m.Version, "from", m.UpgradeFrom, "current", current)
	}
	return nil
}

func isVersion(v string) bool {
	_, ok := semver.Parse(v)
	return ok
}

type dockerPlan struct {
	images *dockerImages
	tree   deployfiles.Tree
	files  *deployfiles.Plan
}

func (u *updateRun) applyDocker(ctx context.Context, m *release.Manifest, current string, st, prev *State) (Result, error) {
	if u.in.DevStack() {
		return ResultFailed, apperr.Precondition("updater.errors.dev_stack")
	}
	if err := u.preflightDocker(m, current); err != nil {
		return ResultFailed, err
	}
	plan, err := u.planDocker(ctx, m, current, nil)
	if err != nil {
		return ResultFailed, err
	}
	u.printPlanDocker(m, current, plan)
	if ok, cerr := u.confirm("updater.cli.update_now"); cerr != nil || !ok {
		return "", cerr
	}
	if serr := u.backupBefore(ctx, m, current, st); serr != nil {
		return ResultFailed, serr
	}

	u.say("updater.cli.pulling_images", "count", len(plan.images.Pull))
	if err = pullAndTag(ctx, u.deps.Images, plan.images, initwizard.ImageTag(m.Version)); err != nil {
		return ResultFailed, apperr.Wrap(err, "updater.errors.nothing_was_changed")
	}

	sw, err := u.switchFiles(m, current, st, plan)
	switch {
	case sw == nil && err != nil:
		return ResultFailed, err
	case err != nil:
		return u.backOut(ctx, m, current, st, sw, err)
	}
	if err := u.composeUp(ctx); err != nil {
		return u.backOut(ctx, m, current, st, sw, err)
	}
	if err := u.waitFor(ctx, m.Version); err != nil {
		return u.backOut(ctx, m, current, st, sw, err)
	}

	u.settle(ctx, st, sw)
	u.pruneBackups(append(keptSnapshots(u.in.Root, sw.list), st.BackupPath)...)
	u.say("updater.cli.now_running", "version", m.Version)
	if prev != nil && prev.Result == ResultFailed {
		st.Detail = i18n.Text("updater.labels.converged_after_failure")
	}
	return ResultOK, nil
}

func (u *updateRun) backupBefore(ctx context.Context, m *release.Manifest, current string, st *State) error {
	if current == m.Version || u.externalDatabase() || u.opts.BackupConfirmed {
		return nil
	}
	backup := u.deps.Backup
	if backup == nil {
		backup = Backup
	}
	path, method, err := backup(ctx, u.in, u.out)
	if err != nil {
		return apperr.WrapKind(apperr.KindUnavailable, err, "updater.errors.backup_failed")
	}
	st.BackupPath, st.BackupMethod = path, method
	u.say("updater.cli.snapshot_taken", "path", path, "method", methodLabel(method))
	return nil
}

type switched struct {
	envBefore     []byte
	undoFiles     deployfiles.Undo
	list, dropped []KeptVersion
}

// nil with an error: nothing changed. Non-nil with an error: files changed and the caller must put
// them back.
func (u *updateRun) switchFiles(m *release.Manifest, current string, st *State, plan *dockerPlan) (*switched, error) {
	envPath := u.in.EnvPath()
	envBefore, err := os.ReadFile(envPath) //nolint:gosec // this install's own deploy/.env
	if err != nil && !os.IsNotExist(err) {
		return nil, apperr.Wrap(err, "updater.errors.read_path", "path", "deploy/.env")
	}
	st.From.Image = readBlock(string(envBefore))[envDaemonImage]
	if st.From.Image == "" {
		st.From.Image = u.getenv(envDaemonImage)
	}
	sw := &switched{envBefore: envBefore}
	sw.list, sw.dropped = nextKept(st.Kept, KeptVersion{
		Version: current, Images: blockImages(string(envBefore), u.getenv),
		Snapshot: snapshotName(st.BackupPath), SnapshotMethod: st.BackupMethod,
	}, KeptVersion{Version: m.Version, Images: sortedValues(plan.images.Env)}, kept.Keep(u.getenv))

	if sw.undoFiles, err = u.writeFiles(plan); err != nil {
		return nil, err
	}
	values := keptEnv(sw.list)
	for k, v := range plan.images.Env {
		values[k] = v
	}
	disabled, err := updateEnvFile(envPath, m.Version, values)
	if err != nil {
		return sw, err
	}
	if len(disabled) > 0 {
		u.say("updater.cli.commented_out", "keys", strings.Join(disabled, ", "))
	}
	return sw, nil
}

func (u *updateRun) settle(ctx context.Context, st *State, sw *switched) {
	st.Kept = sw.list
	u.dropKept(sw.dropped)
	u.pruneImages(ctx, i18n.Text("updater.labels.images_no_longer_kept"))
}

func (u *updateRun) preflightDocker(m *release.Manifest, current string) error {
	if err := checkUpgradePath(m, current); err != nil {
		return err
	}
	var missing []string
	for _, k := range m.Requires {
		if strings.TrimSpace(u.getenv(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return apperr.Precondition("updater.errors.requires_env", "version", m.Version, "names", strings.Join(missing, ", "))
	}
	if err := u.checkManualSteps(m); err != nil {
		return err
	}
	if current != m.Version && u.externalDatabase() && !u.opts.BackupConfirmed {
		return apperr.Precondition("updater.errors.external_backup_required")
	}
	if free := freeBytes(u.in.Root); free >= 0 && free < minFreeBytes {
		return apperr.Precondition("updater.errors.disk_space", "path", u.in.Root, "free", free>>20, "need", minFreeBytes>>20)
	}
	return nil
}

func (u *updateRun) planDocker(ctx context.Context, m *release.Manifest, current string, ran map[string]bool) (*dockerPlan, error) {
	env, err := initwizard.LoadEnvFile(u.in.EnvPath())
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.read_path", "path", "deploy/.env")
	}
	// Unreadable init answers pull every engine's image on purpose: that costs disk, never a run.
	var engines []string
	if answers, aerr := initwizard.LoadAnswers(u.in.Root); aerr == nil {
		engines = answers.Engines
	}
	imgs, err := planDockerImages(m, u.opts.Registry, env, engines, u.in.ComposeFile, u.externalDatabase())
	if err != nil {
		return nil, err
	}
	if !allIn(imgs.Ours, ran) {
		if verr := verifyAll(ctx, u.deps.imageVerifier(m), imgs.Ours); verr != nil {
			return nil, verr
		}
	}
	plan := &dockerPlan{images: imgs}

	if u.in.HasSource() {
		u.say("updater.cli.source_checkout")
		return plan, nil
	}
	tree, err := u.bundle(ctx, m)
	if err != nil {
		return nil, err
	}
	rec, err := deployfiles.LoadRecord(u.in.Root)
	if err != nil {
		return nil, err
	}
	var baseline map[string]string
	if rec == nil && isVersion(current) && current != m.Version {
		baseline = u.baselineHashes(ctx, current)
	}
	files, err := deployfiles.NewPlan(u.in.Root, rec, baseline, tree, m.Version)
	if err != nil {
		return nil, err
	}
	if c := files.Conflicts(); len(c) > 0 {
		return nil, editedFilesError(c)
	}
	plan.tree, plan.files = tree, files
	return plan, nil
}

func (u *updateRun) checkManualSteps(m *release.Manifest) error {
	steps := m.ManualSteps
	if u.in.Mode == initwizard.ModeKubernetes {
		// A cluster's variables live in a Secret this CLI cannot read, so the operator confirms
		// them as manual steps.
		for _, k := range m.Requires {
			steps = append(steps, i18n.Text("updater.labels.manual_step_set_var", "name", k))
		}
	}
	if len(steps) == 0 || u.opts.AckManualSteps {
		return nil
	}
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "\n  %d. %s", i+1, s)
	}
	return apperr.Precondition("updater.errors.manual_steps", "version", m.Version, "steps", b.String())
}

func editedFilesError(conflicts []deployfiles.Change) error {
	var b strings.Builder
	for _, c := range conflicts {
		fmt.Fprintf(&b, "\n%s", c.Diff)
	}
	return apperr.Conflict("updater.errors.edited_files", "count", len(conflicts), "diffs", b.String(), "file", deployfiles.OverrideFile)
}

func (u *updateRun) bundle(ctx context.Context, m *release.Manifest) (deployfiles.Tree, error) {
	if m.DeployBundle.File == "" {
		return nil, apperr.NotFound("updater.errors.no_deploy_bundle", "version", m.Version)
	}
	src, err := u.source()
	if err != nil {
		return nil, err
	}
	rc, err := src.Open(ctx, m.Version, m.DeployBundle.File)
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.download", "file", m.DeployBundle.File)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(io.LimitReader(rc, maxBundle+1))
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.download", "file", m.DeployBundle.File)
	}
	if len(body) > maxBundle {
		return nil, apperr.TooLarge("updater.errors.file_too_large", "file", m.DeployBundle.File, "limit", maxBundle)
	}
	if got := deployfiles.Hash(body); got != m.DeployBundle.SHA256 {
		return nil, apperr.Unprocessable("updater.errors.sha_mismatch", "file", m.DeployBundle.File, "got", got, "want", m.DeployBundle.SHA256)
	}
	return deployfiles.ReadBundle(bytes.NewReader(body))
}

func (u *updateRun) baselineHashes(ctx context.Context, current string) map[string]string {
	m, err := u.manifest(ctx, current)
	if err != nil {
		return nil
	}
	tree, err := u.bundle(ctx, m)
	if err != nil {
		return nil
	}
	return tree.Hashes()
}

func (u *updateRun) writeFiles(plan *dockerPlan) (deployfiles.Undo, error) {
	if plan.files == nil {
		return func() error { return nil }, nil
	}
	// The record must be restored with the files, or restored files read as hand-edited and block
	// the retry.
	prev, err := deployfiles.LoadRecord(u.in.Root)
	if err != nil {
		return nil, err
	}
	rec, undoFiles, err := deployfiles.Apply(u.in.Root, plan.files, plan.tree)
	if err != nil {
		return nil, err
	}
	undo := func() error { return errors.Join(undoFiles(), u.restoreRecord(prev)) }
	if err := deployfiles.SaveRecord(u.in.Root, rec); err != nil {
		return nil, errors.Join(err, undo())
	}
	if n := len(plan.files.Writes()); n > 0 {
		u.say("updater.cli.wrote_files", "count", n)
	}
	return undo, nil
}

func (u *updateRun) restoreRecord(prev *deployfiles.Record) error {
	if prev != nil {
		return deployfiles.SaveRecord(u.in.Root, prev)
	}
	err := os.Remove(filepath.Join(u.in.Root, deployfiles.RecordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (u *updateRun) composeArgs(rest ...string) []string {
	args := []string{"compose", "-f", u.in.ComposeFile} //i18n:ignore docker CLI arguments
	if _, err := os.Stat(filepath.Join(u.in.Root, deployfiles.OverrideFile)); err == nil {
		args = append(args, "-f", deployfiles.OverrideFile)
	}
	return append(args, rest...)
}

func (u *updateRun) composeUp(ctx context.Context) error {
	return u.opts.Runner.Run(ctx, "docker", u.composeArgs("up", "-d", "--no-build", "--remove-orphans")...)
}

// Without the version check, a container restarting into the old image would count as success.
func (u *updateRun) waitFor(ctx context.Context, version string) error {
	if u.in.DaemonURL == "" {
		u.say("updater.cli.no_daemon_url")
		return nil
	}
	timeout := u.deps.readyTimeout()
	url := strings.TrimRight(u.in.DaemonURL, "/") + "/health/ready"
	fmt.Fprint(u.out, i18n.Text("updater.cli.waiting_for", "url", url))
	err := initwizard.WaitForHTTP(ctx, url, 200, timeout, func(_, _ int, _ error) { fmt.Fprint(u.out, ".") })
	fmt.Fprintln(u.out)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		got := u.in.Running(ctx)
		if got == version {
			u.say("updater.cli.running_version", "version", got)
			return nil
		}
		if time.Now().After(deadline) {
			return apperr.Unavailable("updater.errors.daemon_version_mismatch", "got", got, "want", version)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (u *updateRun) backOut(ctx context.Context, m *release.Manifest, current string, st *State, sw *switched, cause error) (Result, error) {
	// Going back must run even after Ctrl-C: the interrupt is what left the install half-changed.
	ctx = context.WithoutCancel(ctx)
	fmt.Fprintf(u.out, "\n   %s\n", apperr.Text(cause))
	u.say("updater.cli.going_back", "version", current)
	st.Detail = firstLine(apperr.Text(cause))
	var errs []error
	if err := fsx.WriteFile(u.in.EnvPath(), sw.envBefore, 0o600); err != nil {
		errs = append(errs, apperr.Wrap(err, "updater.errors.restore_env"))
	}
	if err := sw.undoFiles(); err != nil {
		errs = append(errs, apperr.Wrap(err, "updater.errors.restore_files"))
	}
	if current != m.Version && !m.RollbackWithoutRestore(current) {
		switch {
		case st.BackupPath != "":
			if err := u.opts.Runner.Run(ctx, "docker", u.composeArgs("stop", composeService)...); err != nil {
				errs = append(errs, err)
			} else if err := u.restoreSnapshot(ctx, st.BackupPath, st.BackupMethod); err != nil {
				errs = append(errs, err)
			}
		default:
			errs = append(errs, apperr.Precondition("updater.errors.restore_required",
				"version", m.Version, "current", current, "hint", restoreHint(u)))
		}
	}
	if len(errs) == 0 {
		if err := u.composeUp(ctx); err != nil {
			errs = append(errs, err)
		} else if err := u.waitFor(ctx, current); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return ResultFailed, apperr.Wrap(cause, "updater.errors.back_out_failed",
			"version", m.Version, "current", current, "errs", apperr.List{Errs: errs, Sep: "\n"})
	}
	return ResultRolledBack, apperr.Wrap(cause, "updater.errors.rolled_back", "version", m.Version, "current", current)
}

func restoreHint(u *updateRun) string {
	if u.externalDatabase() {
		return kept.RestoreExternal
	}
	return i18n.Text("updater.labels.snapshot_placeholder")
}

func (u *updateRun) printPlanDocker(m *release.Manifest, current string, plan *dockerPlan) {
	u.say("updater.cli.plan_from", "version", orUnknown(current))
	u.say("updater.cli.plan_images_pull", "signed", len(plan.images.Ours), "pull", len(plan.images.Pull))
	if plan.files != nil {
		writes := plan.files.Writes()
		names := make([]string, 0, len(writes))
		for _, c := range writes {
			names = append(names, c.Path)
		}
		if len(names) == 0 {
			u.say("updater.cli.plan_files_match", "version", m.Version)
		} else {
			u.say("updater.cli.plan_files", "files", strings.Join(names, ", "))
		}
	}
	switch {
	case current == m.Version:
		u.say("updater.cli.plan_backup_none")
	case u.externalDatabase() || u.opts.BackupConfirmed:
		u.say("updater.cli.plan_backup_yours")
	default:
		u.say("updater.cli.plan_backup_dump")
	}
	if m.RollbackWithoutRestore(current) {
		u.say("updater.cli.plan_back_images", "version", current)
	} else if current != m.Version {
		u.say("updater.cli.plan_back_snapshot", "version", current)
	}
}

func (u *updateRun) applyKube(ctx context.Context, m *release.Manifest, current string, st *State) (Result, error) {
	if err := checkUpgradePath(m, current); err != nil {
		return ResultFailed, err
	}
	if err := u.checkManualSteps(m); err != nil {
		return ResultFailed, err
	}
	if current != m.Version && !u.opts.BackupConfirmed {
		return ResultFailed, apperr.Precondition("updater.errors.external_backup_required")
	}
	if _, err := u.helmValues(); err != nil {
		return ResultFailed, err
	}
	imgs, err := kubeImages(m, u.opts.Registry)
	if err != nil {
		return ResultFailed, err
	}
	if err := verifyAll(ctx, u.deps.imageVerifier(m), imgs); err != nil {
		return ResultFailed, err
	}
	u.say("updater.cli.plan_from", "version", orUnknown(current))
	u.say("updater.cli.plan_images", "count", len(imgs))
	u.say("updater.cli.plan_chart", "ref", m.Chart.Ref, "digest", m.Chart.Digest)
	if ok, err := u.confirm("updater.cli.update_now"); err != nil || !ok {
		return "", err
	}
	if ran, err := u.upgradeHelm(ctx, m); err != nil {
		if !ran {
			return ResultFailed, apperr.Wrap(err, "updater.errors.nothing_changed")
		}
		// --atomic: helm already put the previous revision back.
		return ResultRolledBack, apperr.Wrap(err, "updater.errors.helm_rolled_back", "version", m.Version)
	}
	if err := u.waitFor(ctx, m.Version); err != nil {
		st.Detail = firstLine(apperr.Text(err))
		if rbErr := u.opts.Runner.Run(ctx, "helm", "rollback", u.in.Release, "--namespace", u.in.Namespace, "--wait"); rbErr != nil {
			return ResultFailed, apperr.Wrap(err, "updater.errors.helm_rollback_failed", "version", m.Version, "rollback_err", rbErr)
		}
		return ResultRolledBack, apperr.Wrap(err, "updater.errors.previous_revision_back", "version", m.Version)
	}
	u.say("updater.cli.now_running", "version", m.Version)
	return ResultOK, nil
}

// Falls back to OCTOPUS_IMAGE for .env files written by an older updater without the managed block.
func blockImages(raw string, getenv func(string) string) []string {
	block := readBlock(raw)
	var out []string
	for k, v := range block {
		if strings.HasSuffix(k, "_IMAGE") && v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		if img := strings.TrimSpace(getenv(envDaemonImage)); img != "" {
			out = append(out, img)
		}
	}
	sort.Strings(out)
	return out
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func orUnknown(v string) string {
	if v == "" {
		return i18n.Text("updater.labels.unknown")
	}
	return v
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
