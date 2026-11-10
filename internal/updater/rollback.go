package updater

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/kept"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

// Migrations only run forward: only the release's rollback_to (proven by the compat gate to read
// the new schema) may go back with images alone.
// Anything older requires -restore of a snapshot.
func (u *updateRun) to(ctx context.Context) error {
	u.say("updater.cli.plan_install", "install", u.in.String())
	u.say("updater.cli.plan_running", "running", describeRunning(u.in))

	m, err := u.manifest(ctx, u.opts.To)
	if err != nil {
		return err
	}
	state, err := LoadState(u.in.Root)
	if err != nil {
		return err
	}
	current := u.installedVersion(state)
	switch {
	case current == m.Version:
		u.say("updater.cli.already_on", "version", m.Version)
		return nil
	case !isVersion(current):
		return apperr.Unavailable("updater.errors.running_version_unknown_short")
	case semver.Newer(m.Version, current):
		return apperr.Invalid("updater.errors.target_newer", "version", m.Version, "current", current)
	}
	imagesOnly := u.rollbackTo(ctx, state, current) == m.Version
	if !imagesOnly && u.opts.Restore == "" {
		return u.restoreRequired(state, current, m.Version)
	}
	if done, derr := u.delegate(ctx, m, u.toArgs(m.Version)); done || derr != nil {
		return derr
	}

	unlock, err := acquireLock(u.in.Root, u.opts.Operator, u.deps.now())
	if err != nil {
		return err
	}
	defer unlock()

	st := u.newState(state, current, m)
	var res Result
	if u.in.Mode == initwizard.ModeKubernetes {
		res, err = u.toKube(ctx, m, current, imagesOnly)
	} else {
		res, err = u.toDocker(ctx, m, current, imagesOnly, st)
	}
	if res == "" {
		return err
	}
	u.finish(ctx, st, res, err)
	return err
}

func (u *updateRun) rollbackTo(ctx context.Context, state *State, current string) string {
	if m, err := u.manifest(ctx, current); err == nil {
		return m.RollbackTo
	}
	if state != nil && state.To.Version == current {
		return state.RollbackTo
	}
	return ""
}

func (u *updateRun) restoreRequired(state *State, current, target string) error {
	if u.externalDatabase() || u.in.Mode == initwizard.ModeKubernetes {
		return apperr.Precondition("updater.errors.restore_needed_external", "current", current, "target", target,
			"snapshot", kept.RestoreExternal)
	}
	snap := i18n.Text("updater.labels.snapshot_placeholder")
	if state != nil {
		for _, k := range state.Kept {
			if k.Version == target && k.Snapshot != "" {
				snap = k.Snapshot
			}
		}
	}
	return apperr.Precondition("updater.errors.restore_needed", "current", current, "target", target, "snapshot", snap)
}

func (u *updateRun) toArgs(version string) []string {
	args := u.delegatedArgs("update", "-to", version)
	if u.opts.Restore != "" {
		args = append(args, "-restore", u.opts.Restore)
	}
	return args
}

func (u *updateRun) toDocker(ctx context.Context, m *release.Manifest, current string, imagesOnly bool, st *State) (Result, error) {
	if u.in.DevStack() {
		return ResultFailed, apperr.Precondition("updater.errors.dev_stack_rollback")
	}
	snapshot, method, err := u.restoreSource(imagesOnly)
	if err != nil {
		return ResultFailed, err
	}
	ran := keptImages(st.Kept, m.Version)
	plan, err := u.planDocker(ctx, m, current, ran)
	if err != nil {
		return ResultFailed, err
	}
	u.printTo(m, current, imagesOnly, snapshot, method)
	if ok, cerr := u.confirm("updater.cli.go_back_now"); cerr != nil || !ok {
		return "", cerr
	}

	if !allIn(plan.images.Ours, ran) {
		if serr := pullAndTag(ctx, u.deps.Images, plan.images, initwizard.ImageTag(m.Version)); serr != nil {
			return ResultFailed, serr
		}
	}
	if snapshot != "" {
		if serr := u.stopAndRestore(ctx, snapshot, method, current); serr != nil {
			return ResultFailed, serr
		}
	}
	sw, err := u.switchFiles(m, current, st, plan)
	if err != nil {
		if sw != nil {
			err = errors.Join(err, sw.undoFiles())
		}
		return ResultFailed, err
	}
	if err := u.composeUp(ctx); err != nil {
		return ResultFailed, err
	}
	if err := u.waitFor(ctx, m.Version); err != nil {
		return ResultFailed, apperr.Wrap(err, "updater.errors.did_not_come_up", "version", m.Version)
	}
	u.settle(ctx, st, sw)
	u.say("updater.cli.now_running", "version", m.Version)
	return ResultOK, nil
}

func (u *updateRun) restoreSource(imagesOnly bool) (string, BackupMethod, error) {
	if u.opts.Restore == kept.RestoreExternal {
		if !u.externalDatabase() {
			return "", "", apperr.Invalid("updater.errors.restore_external_not_external")
		}
		return "", "", nil
	}
	if imagesOnly && u.opts.Restore == "" {
		return "", "", nil
	}
	// An explicit -restore is honored even for rollback_to: the operator asked for the data as it
	// was.
	p, err := u.snapshotPath(u.opts.Restore)
	if err != nil {
		return "", "", err
	}
	return p, snapshotMethod(p), nil
}

func (u *updateRun) printTo(m *release.Manifest, current string, imagesOnly bool, snapshot string, method BackupMethod) {
	u.say("updater.cli.plan_back_to", "version", m.Version)
	switch {
	case imagesOnly && u.opts.Restore == "":
		u.say("updater.cli.plan_database_kept", "version", m.Version, "current", current)
	case snapshot != "":
		u.say("updater.cli.plan_database_replaced", "snapshot", filepath.Base(snapshot), "method", methodLabel(method))
		u.say("updater.cli.restore_warning")
	default:
		u.say("updater.cli.plan_database_external")
	}
}

func (u *updateRun) stopAndRestore(ctx context.Context, snapshot string, method BackupMethod, current string) error {
	u.say("updater.cli.stopping_for_restore", "service", composeService)
	if err := u.opts.Runner.Run(ctx, "docker", u.composeArgs("stop", composeService)...); err != nil {
		return err
	}
	if err := u.restoreSnapshot(ctx, snapshot, method); err != nil {
		return apperr.Wrap(err, "updater.errors.stopped_after_restore", "service", composeService, "version", current)
	}
	return nil
}

func (u *updateRun) toKube(ctx context.Context, m *release.Manifest, current string, imagesOnly bool) (Result, error) {
	if !imagesOnly && u.opts.Restore != kept.RestoreExternal {
		return ResultFailed, apperr.Precondition("updater.errors.cluster_restore_yours", "version", current)
	}
	imgs, err := kubeImages(m, u.opts.Registry)
	if err != nil {
		return ResultFailed, err
	}
	if err := verifyAll(ctx, u.deps.imageVerifier(m), imgs); err != nil {
		return ResultFailed, err
	}
	u.say("updater.cli.plan_back_to_chart", "version", m.Version, "ref", m.Chart.Ref, "digest", m.Chart.Digest)
	if ok, err := u.confirm("updater.cli.go_back_now"); err != nil || !ok {
		return "", err
	}
	if ran, err := u.upgradeHelm(ctx, m); err != nil {
		if !ran {
			return ResultFailed, apperr.Wrap(err, "updater.errors.nothing_changed")
		}
		return ResultRolledBack, apperr.Wrap(err, "updater.errors.helm_kept", "version", m.Version, "current", current)
	}
	if err := u.waitFor(ctx, m.Version); err != nil {
		return ResultFailed, err
	}
	u.say("updater.cli.now_running", "version", m.Version)
	return ResultOK, nil
}

func keptImages(list []KeptVersion, version string) map[string]bool {
	out := map[string]bool{}
	for _, k := range list {
		if k.Version == version {
			for _, img := range k.Images {
				out[img] = true
			}
		}
	}
	return out
}

func allIn(refs map[string]string, set map[string]bool) bool {
	if len(set) == 0 {
		return false
	}
	for _, r := range refs {
		if !set[r] {
			return false
		}
	}
	return true
}

func snapshotMethod(path string) BackupMethod {
	if strings.HasSuffix(path, ".tar.gz") {
		return BackupArchive
	}
	return BackupContainerDump
}

func (u *updateRun) restoreSnapshot(ctx context.Context, path string, method BackupMethod) error {
	if _, err := os.Stat(path); err != nil {
		return apperr.WrapKind(apperr.KindNotFound, err, "updater.errors.snapshot_gone", "path", path)
	}
	u.say("updater.cli.restoring", "path", path)
	if method == BackupArchive {
		return u.restoreArchive(ctx, path)
	}
	return u.restoreDump(ctx, path)
}

func (u *updateRun) restoreArchive(ctx context.Context, path string) error {
	dsn := hostDSN(u.in)
	if dsn == "" {
		return apperr.Unavailable("updater.errors.database_unreachable")
	}
	cmd := exec.CommandContext(ctx, u.in.BackupScript(), path) //nolint:gosec // the bundled script, at a path derived from the install root
	cmd.Dir = u.in.Root
	cmd.Env = append(os.Environ(), "RESTORE=1", "OCTOPUS_DATABASE_URL="+dsn)
	// backup.sh prompts for confirmation; the operator already confirmed, and an unseen prompt
	// would hang the restore.
	cmd.Stdin = strings.NewReader("y\n")
	cmd.Stdout = u.out
	cmd.Stderr = u.out
	if err := cmd.Run(); err != nil {
		return apperr.Wrap(err, "updater.errors.restore_archive")
	}
	return nil
}

func (u *updateRun) restoreDump(ctx context.Context, path string) error {
	file, err := os.Open(path) //nolint:gosec // a snapshot the operator named, under the install's backups/
	if err != nil {
		return apperr.Wrap(err, "updater.errors.open_snapshot")
	}
	defer func() { _ = file.Close() }()
	args := u.composeArgs("exec", "-T", "pg", "psql", "-v", "ON_ERROR_STOP=1", "-U", "octopus", "-d", "octopus")
	if r, ok := u.opts.Runner.(InputRunner); ok {
		err = r.RunInput(ctx, file, "docker", args...)
	} else {
		cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // fixed binary; args are compose flags derived from the install

		cmd.Dir = u.in.Root
		cmd.Stdin, cmd.Stdout, cmd.Stderr = file, u.out, u.out
		err = cmd.Run()
	}
	if err != nil {
		return apperr.Wrap(err, "updater.errors.restore_dump")
	}
	return nil
}

type InputRunner interface {
	RunInput(ctx context.Context, stdin io.Reader, name string, args ...string) error
}
