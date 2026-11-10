package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

type ImportOptions struct {
	Workspace string
	// Off by default: the bundle's settings are install-wide and would apply to every workspace
	// here.
	WithSettings bool
	JSON         bool
}

func ExportInDaemon(ctx context.Context, root, outPath, workspace string) error {
	args := []string{"export"}
	if workspace != "" {
		args = append(args, "-workspace", workspace)
	}
	if outPath == "" {
		return runInDaemon(ctx, root, nil, os.Stdout, args)
	}
	tmp, err := os.CreateTemp(filepath.Dir(outPath), filepath.Base(outPath)+".*.part")
	if err != nil {
		return apperr.Wrap(err, "cli.errors.create_file", "path", outPath)
	}
	err = runInDaemon(ctx, root, nil, tmp, args)
	if closeErr := tmp.Close(); err == nil && closeErr != nil {
		err = apperr.Wrap(closeErr, "cli.errors.create_file", "path", outPath)
	}
	if err == nil {
		if err = os.Rename(tmp.Name(), outPath); err == nil {
			return nil
		}
		err = apperr.Wrap(err, "cli.errors.create_file", "path", outPath)
	}
	if rmErr := os.Remove(tmp.Name()); rmErr != nil {
		return errors.Join(err, apperr.Wrap(rmErr, "cli.errors.remove_path", "path", tmp.Name()))
	}
	return err
}

func ImportInDaemon(ctx context.Context, root, inPath string, opts ImportOptions) error {
	if inPath == "" {
		return apperr.Invalid("cli.errors.usage_import")
	}
	f, err := os.Open(filepath.Clean(inPath))
	if err != nil {
		return apperr.Wrap(err, "cli.errors.open_file", "path", inPath)
	}
	defer f.Close()
	return runInDaemon(ctx, root, f, os.Stdout, importArgs(opts))
}

func importArgs(opts ImportOptions) []string {
	args := []string{"import"}
	if opts.Workspace != "" {
		args = append(args, "-workspace", opts.Workspace)
	}
	if opts.WithSettings {
		args = append(args, "-with-settings")
	}
	if opts.JSON {
		args = append(args, "-json")
	}
	return append(args, "-")
}

func runInDaemon(ctx context.Context, root string, in io.Reader, out io.Writer, args []string) error {
	resolved, err := resolveRoot(root)
	if err != nil {
		return err
	}
	install, err := updater.Detect(ctx, resolved)
	if err != nil {
		return err
	}
	name, argv := daemonExec(install, args)
	runner := initwizard.ExecRunner{Dir: install.Root, Out: out, In: in, ErrOut: os.Stderr}
	return runner.Run(ctx, name, argv...)
}

// The binary is at /app/octopus, off PATH: compose exec runs in WORKDIR /app, kubectl exec has no
// cwd to rely on.
// -T and -i, never a TTY: the stream carries a tar.gz.
func daemonExec(in *updater.Install, args []string) (string, []string) {
	if in.Mode == initwizard.ModeKubernetes {
		deployment := (&initwizard.Config{Release: in.Release}).DaemonDeployment()
		//i18n:ignore kubectl arguments
		return "kubectl", slices.Concat([]string{"exec", "-i", "-n", in.Namespace, "deploy/" + deployment, "--", "/app/octopus"}, args)
	}
	//i18n:ignore docker CLI arguments
	return "docker", slices.Concat([]string{"compose", "-f", in.ComposeFile, "exec", "-T", "octopus", "./octopus"}, args)
}
