package api

import (
	"context"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/runner/hostrun"
)

// runHostRunner runs a real hostrun runner until the test ends and returns the context it runs on.
// The cleanup cancels it and waits for Run to return: a runner stopped mid-run still kills that
// run, saves its leftovers into cfg.WorkDir and reports the finish to the daemon, so it must be
// done before the test's temp dirs are removed and its daemon closed (both registered earlier).
func runHostRunner(t *testing.T, cfg *hostrun.Config) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = hostrun.New(cfg, "test").Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the runner did not stop within 30 s of its context ending")
		}
	})
	return ctx
}
