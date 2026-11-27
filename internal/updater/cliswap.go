package updater

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/clicopy"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

// InvokedCLIEnv carries, to the CLI an update hands over to, the binary the person ran: it may sit
// outside the PATH (./thulu) and the child cannot see it otherwise.
const InvokedCLIEnv = "OCTOPUS_UPDATE_INVOKED_CLI"

// CLIOps is how an update finds and replaces the copies of the CLI. Tests replace it whole; the
// default reads the real PATH and service.
type CLIOps struct {
	Executable    func() (string, error)
	PathList      func() string
	IsThulu       func(path string) bool
	Version       func(ctx context.Context, path string) string
	Replace       func(src, dst string) error
	ServiceBinary func(ctx context.Context) (string, bool)
}

func defaultCLIOps() *CLIOps {
	return &CLIOps{
		Executable: os.Executable,
		PathList:   func() string { return os.Getenv("PATH") },
		IsThulu:    clicopy.IsThulu,
		Version:    clicopy.Version,
		Replace:    clicopy.Replace,
		ServiceBinary: func(ctx context.Context) (string, bool) {
			def, ok, err := service.Installed(ctx)
			if err != nil || !ok || def.Binary == "" {
				return "", false
			}
			return def.Binary, true
		},
	}
}

func (u *updateRun) cliOps() *CLIOps {
	if u.deps.CLI != nil {
		return u.deps.CLI
	}
	return defaultCLIOps()
}

// syncAfterDelegate brings the CLI copies to m after a child of another version did the update. A
// newer child does it itself; an older one (a rollback) predates this, so the parent does.
func (u *updateRun) syncAfterDelegate(ctx context.Context, m *release.Manifest) {
	if semver.Newer(u.selfVersion(), m.Version) {
		u.syncCLI(ctx, m, u.delegatedBin)
	}
}

// syncCLI replaces every copy of the CLI that is not on m.Version with src (the binary of m.Version;
// when empty, this process's own or the one fetched for m), then checks the runner service.
func (u *updateRun) syncCLI(ctx context.Context, m *release.Manifest, src string) {
	if !u.opts.SyncCLI {
		return
	}
	ops := u.cliOps()
	if src == "" {
		var err error
		if src, err = u.cliSource(ctx, ops, m); err != nil {
			u.say("updater.cli.cli_fetch_failed", "version", m.Version, "err", apperr.Text(err))
			return
		}
	}
	srcResolved := clicopy.Resolve(src)
	fetched := filepath.Join(u.in.Root, binDir) + string(filepath.Separator)
	replaced := map[string]bool{}
	for _, c := range u.cliCandidates(ops) {
		if c.Resolved == srcResolved || strings.HasPrefix(c.Resolved, fetched) {
			continue
		}
		v := ops.Version(ctx, c.Resolved)
		switch v {
		case m.Version:
			continue
		case "dev":
			u.say("updater.cli.cli_dev_left", "path", c.Path)
			continue
		}
		if err := ops.Replace(src, c.Resolved); err != nil {
			if clicopy.NoPermission(err) {
				u.say("updater.cli.cli_blocked", "path", c.Path, "from", orUnknown(v), "to", m.Version,
					"dir", filepath.Dir(c.Resolved), "command", clicopy.ManualCommand(src, c.Resolved))
			} else {
				u.say("updater.cli.cli_replace_failed", "path", c.Path, "err", apperr.Text(err),
					"command", clicopy.ManualCommand(src, c.Resolved))
			}
			continue
		}
		replaced[c.Resolved] = true
		u.say("updater.cli.cli_replaced", "path", c.Path, "from", orUnknown(v), "to", m.Version)
	}
	u.checkRunnerService(ctx, ops, m, replaced)
}

func (u *updateRun) cliSource(ctx context.Context, ops *CLIOps, m *release.Manifest) (string, error) {
	if u.selfVersion() == m.Version {
		return ops.Executable()
	}
	platform := runtime.GOOS + "_" + runtime.GOARCH
	art, ok := m.Binaries[platform]
	if !ok {
		return "", apperr.NotFound("updater.errors.no_cli_for_platform", "version", m.Version, "platform", platform)
	}
	src, err := u.source()
	if err != nil {
		return "", err
	}
	return fetchBinary(ctx, src, u.in.Root, m.Version, art)
}

// cliCandidates are the Thulu binaries on the PATH, plus the one the person ran when it is not.
func (u *updateRun) cliCandidates(ops *CLIOps) []clicopy.Copy {
	var out []clicopy.Copy
	seen := map[string]bool{}
	add := func(path string) {
		r := clicopy.Resolve(path)
		if path == "" || seen[r] || !ops.IsThulu(r) {
			return
		}
		seen[r] = true
		out = append(out, clicopy.Copy{Path: path, Resolved: r})
	}
	invoked := strings.TrimSpace(os.Getenv(InvokedCLIEnv))
	if invoked == "" && !u.opts.Delegated {
		if exe, err := ops.Executable(); err == nil {
			invoked = exe
		}
	}
	add(invoked)
	for _, dir := range filepath.SplitList(ops.PathList()) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		for _, name := range clicopy.Names {
			p := filepath.Join(dir, name)
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				add(p)
			}
		}
	}
	return out
}

// checkRunnerService says what the runner service needs after the CLI changed: a restart when it
// runs a file just replaced, a reinstall when it points at a copy still on another version.
func (u *updateRun) checkRunnerService(ctx context.Context, ops *CLIOps, m *release.Manifest, replaced map[string]bool) {
	bin, ok := ops.ServiceBinary(ctx)
	if !ok {
		return
	}
	r := clicopy.Resolve(bin)
	if replaced[r] {
		u.say("updater.cli.runner_restart", "path", bin, "version", m.Version, "command", "thulu runner service restart") //i18n:ignore a command
		return
	}
	if !ops.IsThulu(r) {
		return
	}
	if v := ops.Version(ctx, r); v != m.Version && v != "dev" {
		u.say("updater.cli.runner_outdated", "path", bin, "version", orUnknown(v), "target", m.Version,
			"command", "thulu runner service install") //i18n:ignore a command
	}
}
