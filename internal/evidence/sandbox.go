package evidence

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

type SandboxStarter interface {
	// Must fail with *SandboxFailure, or with ctx's error when canceled or timed out;
	// sandboxFailure relies on it.
	Start(ctx context.Context, req SandboxRequest) (*SandboxApp, error)
	// Best effort and must be idempotent: several exit paths call it.
	Stop(ctx context.Context, owner string)
	// Must be known before Start creates anything, so every path can tear the sandbox down.
	Owner(batchID string) string
	Leftovers(ctx context.Context) []string
}

type SandboxRequest struct {
	WorkspaceID string
	ProjectID   string
	Branch      string
	BatchID     string
	Progress    func(phase string)
}

type SandboxApp struct {
	Owner  string
	URL    string
	Commit string
	Port   int
}

type SandboxFailure struct {
	Reason Reason
	Log    string
}

func (f *SandboxFailure) Error() string { return f.Reason.String() }

const sandboxStartMax = 75 * time.Minute

const sandboxTeardownMax = 2 * time.Minute

const unreachableMax = 500

func (s *Service) launchSandbox(ctx context.Context, b *Batch) error {
	if s.Sandbox == nil {
		s.failSandbox(context.WithoutCancel(ctx), b, SandboxReason(SandboxUnavailable), "", "")
		return nil
	}
	logx.Warn(s.Store.MarkSandboxStarting(ctx, b.ID), "evidence.log.sandbox_mark_failed", "batch", b.ID)
	startCtx, cancel := context.WithTimeout(ctx, sandboxStartMax)
	s.trackSandboxStart(b.ID, cancel)
	batch := *b
	go func() {
		defer cancel()
		defer s.untrackSandboxStart(batch.ID)
		s.startSandbox(startCtx, &batch)
	}()
	return nil
}

// Every exit path must tear the sandbox down and release the slot.
func (s *Service) startSandbox(ctx context.Context, b *Batch) {
	bg := context.WithoutCancel(ctx)
	owner := s.Sandbox.Owner(b.ID)
	abandon := func() {
		s.stopSandbox(owner, b.ID)
		// No runner closes a batch canceled before it got one.
		logx.Warn(s.Store.finishCanceled(bg, b.ID), "evidence.log.close_failed", "batch", b.ID)
		s.release(b.ID)
		s.Kick()
	}
	if cur, err := s.Store.batchByID(bg, b.ID); err == nil && cur.Status == BatchCanceled {
		abandon()
		return
	}
	env, err := s.Store.envByID(bg, b.EnvironmentID)
	if err != nil {
		s.failSandbox(bg, b, SandboxReason(SandboxUnavailable), "", owner)
		return
	}
	app, err := s.Sandbox.Start(ctx, SandboxRequest{WorkspaceID: b.WorkspaceID, ProjectID: env.ProjectID,
		Branch: b.SourceBranch, BatchID: b.ID, Progress: func(phase string) {
			logx.Warn(s.Store.MarkSandboxPhase(bg, b.ID, phase), "evidence.log.sandbox_mark_failed", "batch", b.ID)
		}})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			// The cancel already closed the batch, or the daemon is stopping and the next boot
			// closes it; only the sandbox is left.
			abandon()
			return
		}
		reason, tail := sandboxFailure(ctx, err)
		s.failSandbox(bg, b, reason, tail, owner)
		return
	}
	if app.Owner != "" {
		owner = app.Owner
	}
	ready, err := s.Store.MarkSandboxReady(bg, b.ID, app.URL, app.Commit)
	if err != nil {
		s.failSandbox(bg, b, SandboxReason(SandboxError, "error", apperr.Localize(err, i18n.SourceLocale)), "", owner)
		return
	}
	if !ready {
		abandon()
		return
	}
	b.BaseURL, b.SourceCommit, b.SandboxStatus = app.URL, app.Commit, SandboxReady
	if err = s.runRunner(bg, b, owner, func() { s.stopSandbox(owner, b.ID) }); err != nil {
		logx.Warn(err, "evidence.log.launch_failed", "batch", b.ID)
		logx.Warn(s.Store.CloseBatch(bg, b.ID, apperr.Localize(err, i18n.SourceLocale)), "evidence.log.close_failed", "batch", b.ID)
		abandon()
	}
}

func sandboxFailure(ctx context.Context, err error) (Reason, string) {
	var f *SandboxFailure
	switch {
	case errors.As(err, &f):
		return f.Reason, f.Log
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return SandboxReason(SandboxReadyTimeout), ""
	}
	return SandboxReason(SandboxError, "error", apperr.Localize(err, i18n.SourceLocale)), ""
}

func (s *Service) failSandbox(ctx context.Context, b *Batch, reason Reason, tail, owner string) {
	logx.Warn(s.Store.FailSandboxBatch(ctx, b.ID, reason, tail), "evidence.log.close_failed", "batch", b.ID)
	if owner != "" {
		s.stopSandbox(owner, b.ID)
	}
	s.release(b.ID)
	s.Kick()
}

func (s *Service) stopSandbox(owner, batchID string) {
	ctx, cancel := context.WithTimeout(context.Background(), sandboxTeardownMax)
	defer cancel()
	if s.Sandbox != nil {
		s.Sandbox.Stop(ctx, owner)
	}
	logx.Warn(s.Store.MarkSandboxReleased(ctx, batchID), "evidence.log.sandbox_mark_failed", "batch", batchID)
}

func (s *Service) FailUnreachable(ctx context.Context, ws, batchID, msg string) error {
	b, err := s.Store.BatchHeader(ctx, ws, batchID)
	if err != nil {
		return err
	}
	if b.Status != BatchRunning {
		return apperr.Conflict("evidence.errors.batch_closed")
	}
	if len(msg) > unreachableMax {
		msg = msg[:unreachableMax]
	}
	sandboxed := b.SandboxStatus == SandboxReady
	if failErr := s.Store.FailSandboxBatch(ctx, b.ID, SandboxReason(SandboxUnreachable, "error", msg), ""); failErr != nil {
		return failErr
	}
	if sandboxed && s.Sandbox != nil {
		s.stopSandbox(s.Sandbox.Owner(b.ID), b.ID)
	}
	return nil
}

// Must run before Start closes the batches left running: a still-starting sandbox fails its batch
// as an environment failure so no case moves.
func (s *Service) reapSandboxes(ctx context.Context, running []Batch) {
	if s.Sandbox == nil {
		return
	}
	reason := SandboxReason(SandboxRestarted)
	owners := map[string]string{}
	for i := range running {
		b := &running[i]
		switch b.SandboxStatus {
		case SandboxPending, SandboxStarting:
			logx.Warn(s.Store.FailSandboxBatch(ctx, b.ID, reason, ""), "evidence.log.close_failed", "batch", b.ID)
			owners[s.Sandbox.Owner(b.ID)] = b.ID
		case SandboxReady:
			owners[s.Sandbox.Owner(b.ID)] = b.ID
		}
	}
	for _, owner := range s.Sandbox.Leftovers(ctx) {
		if _, ok := owners[owner]; !ok {
			owners[owner] = ""
		}
	}
	for owner, batchID := range owners {
		log.Printf("[evidence] %s", i18n.Log("evidence.log.sandbox_reaped", "owner", owner))
		if batchID != "" {
			s.stopSandbox(owner, batchID)
			continue
		}
		stopCtx, cancel := context.WithTimeout(ctx, sandboxTeardownMax)
		s.Sandbox.Stop(stopCtx, owner)
		cancel()
	}
}
