package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/deploy"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

type InitFlags struct {
	Root           string
	DryRun         bool
	NonInteractive bool
	Plain          bool
	Stack          string
}

func Init(f InitFlags) error {
	root, dryRun, nonInteractive, plain := f.Root, f.DryRun, f.NonInteractive, f.Plain
	resolved, err := prepareRoot(root, dryRun, os.Stdout)
	if err != nil {
		return err
	}
	prompter := initwizard.NewPrompter(plain)
	prompter.NonInteractive = nonInteractive

	opts := initwizard.Options{
		Root:           resolved,
		DryRun:         dryRun,
		NonInteractive: nonInteractive,
		NoSource:       !hasSource(resolved),
		BinaryVersion:  buildinfo.Version,
		Stack:          f.Stack,
		Prompter:       prompter,
		Runner:         initwizard.ExecRunner{Dir: resolved, Out: os.Stdout},
		Out:            os.Stdout,
	}
	if prompter.Interactive() && !dryRun {
		path := filepath.Join(resolved, "deploy", "init.log")
		// Keep the previous log one generation back so a rerun canceled at the first question keeps
		// why the last one failed.
		if err := os.Rename(path, path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return apperr.Wrap(err, "cli.errors.open_log", "path", path)
		}
		//nolint:gosec // G304: the install's own log, under the root it resolved
		logFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return apperr.Wrap(err, "cli.errors.open_log", "path", path)
		}
		defer logFile.Close()
		opts.Runner = initwizard.ExecRunner{Dir: resolved, Out: logFile}
		opts.LogPath = path
	}
	return initwizard.Run(context.Background(), opts)
}

func prepareRoot(start string, dryRun bool, out io.Writer) (string, error) {
	var dir string
	if start != "" {
		abs, err := filepath.Abs(start)
		if err != nil {
			return "", apperr.Wrap(err, "cli.errors.resolve_path", "path", start)
		}
		dir = abs
	} else {
		if found, err := resolveRoot(""); err == nil {
			return found, nil
		}
		home, err := defaultHome()
		if err != nil {
			return "", err
		}
		fmt.Fprintln(out, i18n.Text("cli.cli.installing_into_home", "dir", home))
		dir = home
	}
	if dryRun {
		return dir, nil
	}
	return dir, materialize(dir, out)
}

func materialize(root string, out io.Writer) error {
	written, err := deploy.Materialize(root)
	if err != nil {
		return apperr.Wrap(err, "cli.errors.write_deployment_files", "dir", root)
	}
	if len(written) > 0 {
		fmt.Fprintln(out, i18n.Text("cli.cli.wrote_deployment_files", "count", len(written), "dir", root))
	}
	return nil
}

func hasSource(root string) bool {
	for _, name := range []string{"go.mod", "main.go"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			return false
		}
	}
	return true
}

func defaultHome() (string, error) {
	if dir := os.Getenv("OCTOPUS_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", apperr.Wrap(err, "cli.errors.locate_home")
	}
	return filepath.Join(home, ".octopus"), nil
}

func resolveRoot(start string) (string, error) {
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", apperr.Wrap(err, "cli.errors.resolve_working_dir")
		}
		start = cwd
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", apperr.Wrap(err, "cli.errors.resolve_path", "path", start)
	}
	for {
		if holdsDeployment(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if home, homeErr := defaultHome(); homeErr == nil && holdsDeployment(home) {
		return home, nil
	}
	return "", apperr.NotFound("cli.errors.no_deployment", "marker", deploy.Marker, "dir", start)
}

func holdsDeployment(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "deploy", deploy.Marker))
	return err == nil && !info.IsDir()
}
