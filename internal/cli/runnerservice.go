package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
	"github.com/MisterDonnuts/thulu/internal/runner/service"
)

var (
	queryRunnerService     = service.Query
	uninstallRunnerService = service.Uninstall
)

func RunnerService(ctx context.Context, out io.Writer, action, config string) error {
	switch action {
	case "install":
		cfg, err := hostrun.LoadConfig(config)
		if err != nil {
			return err
		}
		if perr := hostrun.Preflight(cfg); perr != nil {
			return perr
		}
		spec, err := service.SpecFor(config)
		if err != nil {
			return err
		}
		st, err := service.Install(ctx, spec)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, i18n.Text("cli.runner_service.installed", "file", st.File, "log", spec.Log))
		if !st.Running {
			fmt.Fprintln(out, i18n.Text("cli.runner_service.not_running", "log", spec.Log))
		}
		printLinger(out, st)
		return nil
	case "uninstall":
		return uninstallRunner(ctx, out, config)
	case "status", "":
		st, err := queryRunnerService(ctx)
		if err != nil {
			return err
		}
		printRunnerStatus(out, st, service.LogPath(config))
		return nil
	}
	return apperr.Invalid("main.errors.usage_runner")
}

func uninstallRunner(ctx context.Context, out io.Writer, config string) error {
	removed, err := uninstallRunnerService(ctx)
	if err != nil {
		return err
	}
	for _, p := range hostrun.LogFiles(service.LogPath(config)) {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return apperr.Wrap(err, "cli.errors.remove_path", "path", p)
		}
	}
	if !removed {
		fmt.Fprintln(out, i18n.Text("cli.runner_service.status_none"))
		return nil
	}
	fmt.Fprintln(out, i18n.Text("cli.runner_service.uninstalled"))
	return nil
}

func printRunnerStatus(out io.Writer, st service.Status, log string) {
	switch {
	case !st.Installed && !st.Loaded:
		fmt.Fprintln(out, i18n.Text("cli.runner_service.status_none"))
		return
	case !st.Installed:
		// launchd keeps an agent whose plist was deleted until logout.
		fmt.Fprintln(out, i18n.Text("cli.runner_service.status_orphan", "file", st.File))
		return
	case st.Running:
		fmt.Fprintln(out, i18n.Text("cli.runner_service.status_running", "file", st.File))
	case st.LastExit != "":
		fmt.Fprintln(out, i18n.Text("cli.runner_service.status_exited", "file", st.File, "code", st.LastExit))
	default:
		fmt.Fprintln(out, i18n.Text("cli.runner_service.status_stopped", "file", st.File))
	}
	fmt.Fprintln(out, i18n.Text("cli.runner_service.log", "log", log))
	printLinger(out, st)
}

func printLinger(out io.Writer, st service.Status) {
	if st.NoLinger {
		fmt.Fprintln(out, i18n.Text("service.cli.no_linger"))
	}
}
