package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/release"
)

const (
	binDir    = "deploy/.octopus-bin"
	maxBinary = 512 << 20
)

func (u *updateRun) selfVersion() string {
	if u.deps.SelfVersion != "" {
		return u.deps.SelfVersion
	}
	return "dev"
}

func (u *updateRun) delegate(ctx context.Context, m *release.Manifest, args []string) (bool, error) {
	self := u.selfVersion()
	if self == m.Version {
		return false, nil
	}
	if u.opts.Delegated {
		// The fetched binary disagrees with the manifest it was verified against: stop instead of
		// fetching again in a loop.
		return false, apperr.Conflict("updater.errors.delegated_version_mismatch", "self", self, "target", m.Version)
	}
	if u.opts.NoFetchCLI {
		return false, apperr.Precondition("updater.errors.no_fetch_cli_mismatch", "self", self, "target", m.Version)
	}
	platform := runtime.GOOS + "_" + runtime.GOARCH
	art, ok := m.Binaries[platform]
	if !ok {
		return false, apperr.NotFound("updater.errors.no_cli_for_platform", "version", m.Version, "platform", platform)
	}
	src, err := u.source()
	if err != nil {
		return false, err
	}
	bin, err := fetchBinary(ctx, src, u.in.Root, m.Version, art)
	if err != nil {
		return false, err
	}
	u.say("updater.cli.running_cli", "version", m.Version, "path", filepath.Join(binDir, art.File))
	run := u.deps.Exec
	if run == nil {
		run = u.execBinary
	}
	return true, run(ctx, bin, args)
}

// The API key goes in the environment, never argv, where other users could read it with ps.
// Cancel sends an interrupt, not SIGKILL: the child backs out its changes, records the result and
// releases the lock.
func (u *updateRun) execBinary(ctx context.Context, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // a binary whose sha256 matched the verified manifest
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 0 // never kill: wait for the child to finish backing out
	cmd.Dir = u.in.Root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if u.opts.APIKey != "" {
		cmd.Env = append(cmd.Env, "OCTOPUS_API_KEY="+u.opts.APIKey)
	}
	if err := cmd.Run(); err != nil {
		return apperr.Wrap(err, "updater.errors.cli_failed", "name", filepath.Base(bin))
	}
	return nil
}

func (u *updateRun) delegatedArgs(cmd ...string) []string {
	args := append(append([]string{}, cmd...), "-root", u.in.Root, "-delegated")
	flag := func(on bool, name string) {
		if on {
			args = append(args, name)
		}
	}
	value := func(name, v string) {
		if v != "" {
			args = append(args, name, v)
		}
	}
	flag(u.opts.Yes, "-yes")
	flag(u.opts.AckManualSteps, "-ack-manual-steps")
	flag(u.opts.BackupConfirmed, "-backup-confirmed")
	flag(u.opts.ReuseValues, "-reuse-values")
	value("-registry", u.opts.Registry)
	value("-chart-registry", u.opts.ChartRegistry)
	value("-operator", u.opts.Operator)
	if u.opts.Values != "" {
		// The child runs in the install root, so a relative path must be resolved here.
		values, err := filepath.Abs(u.opts.Values)
		if err != nil {
			values = u.opts.Values
		}
		value("-values", values)
	}
	return args
}

func fetchBinary(ctx context.Context, src release.Source, root, version string, art release.Artifact) (string, error) {
	dir := filepath.Join(root, binDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", apperr.Wrap(err, "updater.errors.create_path", "path", binDir)
	}
	final := filepath.Join(dir, filepath.Base(art.File))
	if sum, err := fileSHA256(final); err == nil && sum == art.SHA256 {
		return final, nil
	}
	rc, err := src.Open(ctx, version, art.File)
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.download", "file", art.File)
	}
	defer func() { _ = rc.Close() }()

	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer warnRemove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(rc, maxBinary+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", apperr.Wrap(err, "updater.errors.download", "file", art.File)
	}
	if n > maxBinary {
		return "", apperr.TooLarge("updater.errors.file_too_large", "file", art.File, "limit", maxBinary)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != art.SHA256 {
		return "", apperr.Unprocessable("updater.errors.sha_mismatch", "file", art.File, "got", got, "want", art.SHA256)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // an executable the operator runs
		return "", err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return "", err
	}
	return final, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a path under the install root
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
