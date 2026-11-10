package db

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type Pinger interface {
	PingContext(ctx context.Context) error
}

type WaitPolicy struct {
	Initial     time.Duration
	Max         time.Duration
	Budget      time.Duration
	PingTimeout time.Duration
}

var DefaultWait = WaitPolicy{
	Initial:     500 * time.Millisecond,
	Max:         5 * time.Second,
	Budget:      60 * time.Second,
	PingTimeout: 5 * time.Second,
}

// In some clusters the pod's NetworkPolicy lands after the container starts, so the first connects are
// refused.
func WaitReady(ctx context.Context, p Pinger, pol WaitPolicy) error {
	deadline := time.Now().Add(pol.Budget)
	delay := pol.Initial
	for attempt := 1; ; attempt++ {
		last := ping(ctx, p, pol.PingTimeout)
		if last == nil {
			if attempt > 1 {
				log.Printf("[db] %s", i18n.Log("db.log.ready_after_retries", "attempts", attempt))
			}
			return nil
		}
		if ctx.Err() != nil {
			return apperr.Wrap(ctx.Err(), "db.errors.wait_canceled", "last", last.Error())
		}
		if !retryable(last) {
			return apperr.Wrap(last, "db.errors.ping")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return apperr.WrapKind(apperr.KindUnavailable, last, "db.errors.not_ready",
				"attempts", attempt, "budget", pol.Budget.String())
		}
		pause := min(delay, remaining)
		log.Printf("[db] %s", i18n.Log("db.log.waiting_for_database",
			"attempt", attempt, "retry_in", pause.String(), "err", last))
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return apperr.Wrap(ctx.Err(), "db.errors.wait_canceled", "last", last.Error())
		case <-timer.C:
		}
		delay = min(delay*2, pol.Max)
	}
}

func ping(ctx context.Context, p Pinger, timeout time.Duration) error {
	if timeout <= 0 {
		return p.PingContext(ctx)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return p.PingContext(attemptCtx)
}

// Class 57 (starting/shutting down) and 53300 (too many connections) clear on their own; auth or
// missing-database errors do not.
func retryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return true
	}
	return strings.HasPrefix(pgErr.Code, "57") || pgErr.Code == "53300"
}
