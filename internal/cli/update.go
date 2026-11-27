package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/clicopy"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
	"github.com/MisterDonnuts/thulu/internal/updater"
)

func Update(root string, opts updater.Options) error {
	if err := prepare(root, &opts); err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return updater.Run(ctx, opts)
}

func Apply(root string, opts updater.Options) error {
	if err := prepare(root, &opts); err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return updater.Apply(ctx, opts)
}

func Mirror(opts updater.MirrorOptions) error {
	opts.Out = os.Stdout
	ctx, stop := signalContext()
	defer stop()
	return updater.Mirror(ctx, opts)
}

func prepare(root string, opts *updater.Options) error {
	resolved, err := resolveRoot(root)
	if err != nil {
		return err
	}
	opts.Root = resolved
	opts.Out = os.Stdout
	opts.Prompter = initwizard.NewPrompter(false)
	opts.Runner = initwizard.ExecRunner{Dir: resolved, Out: os.Stdout}
	opts.Deps.SelfVersion = buildinfo.Version
	opts.SyncCLI = true
	return nil
}

// VersionDrift warns on f, when f is a terminal, about the copies of Thulu on this machine that run
// another version than this CLI. Scripts read `thulu version` from stdout and never see it; a copy
// asked for its version by another one (clicopy.ProbeEnv) skips it.
func VersionDrift(f *os.File) {
	if os.Getenv(clicopy.ProbeEnv) != "" || !term.IsTerminal(int(f.Fd())) {
		return
	}
	writeDrift(f, buildinfo.Version, func(ctx context.Context, self, root string) updater.Versions {
		return updater.ReadVersions(ctx, self, root, nil)
	})
}

func writeDrift(out io.Writer, self string, read func(ctx context.Context, self, root string) updater.Versions) {
	root, err := resolveRoot("")
	if err != nil {
		root = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, line := range read(ctx, self, root).Drift() {
		fmt.Fprintln(out, line)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
