package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/MisterDonnuts/thulu/internal/buildinfo"
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
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
