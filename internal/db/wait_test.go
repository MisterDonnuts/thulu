package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/db"
)

var errRefused = errors.New("dial tcp 10.96.0.10:5432: connect: connection refused")

type scriptedPinger struct {
	mu    sync.Mutex
	errs  []error
	calls int
}

func (p *scriptedPinger) PingContext(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.calls
	p.calls++
	if len(p.errs) == 0 {
		return nil
	}
	if i >= len(p.errs) {
		i = len(p.errs) - 1
	}
	return p.errs[i]
}

func (p *scriptedPinger) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type hangingPinger struct{}

func (hangingPinger) PingContext(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

var fast = db.WaitPolicy{
	Initial:     time.Millisecond,
	Max:         2 * time.Millisecond,
	Budget:      5 * time.Second,
	PingTimeout: time.Second,
}

func TestWaitReady_FirstPingAnswers(t *testing.T) {
	p := &scriptedPinger{}
	if err := db.WaitReady(t.Context(), p, fast); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if p.count() != 1 {
		t.Errorf("pinged %d times, want 1", p.count())
	}
}

func TestWaitReady_RetriesUntilTheDatabaseAnswers(t *testing.T) {
	p := &scriptedPinger{errs: []error{errRefused, errRefused, errRefused, nil}}
	if err := db.WaitReady(t.Context(), p, fast); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if p.count() != 4 {
		t.Errorf("pinged %d times, want 4", p.count())
	}
}

func TestWaitReady_GivesUpWithTheLastError(t *testing.T) {
	p := &scriptedPinger{errs: []error{errRefused}}
	pol := fast
	pol.Budget = 30 * time.Millisecond
	err := db.WaitReady(t.Context(), p, pol)
	if err == nil {
		t.Fatal("WaitReady succeeded against a database that never answers")
	}
	if !errors.Is(err, errRefused) {
		t.Errorf("error %v does not carry the last ping error", err)
	}
	if k := apperr.KindOf(err); k != apperr.KindUnavailable {
		t.Errorf("kind = %v, want KindUnavailable", k)
	}
	if p.count() < 2 {
		t.Errorf("pinged %d times, want at least 2 within the budget", p.count())
	}
}

func TestWaitReady_DoesNotRetryAnAnswerWaitingCannotFix(t *testing.T) {
	auth := &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}
	p := &scriptedPinger{errs: []error{auth, nil}}
	err := db.WaitReady(t.Context(), p, fast)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "28P01" {
		t.Fatalf("WaitReady = %v, want the authentication error", err)
	}
	if p.count() != 1 {
		t.Errorf("pinged %d times, want 1", p.count())
	}
}

func TestWaitReady_RetriesAServerThatIsStartingUp(t *testing.T) {
	starting := &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}
	p := &scriptedPinger{errs: []error{starting, nil}}
	if err := db.WaitReady(t.Context(), p, fast); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if p.count() != 2 {
		t.Errorf("pinged %d times, want 2", p.count())
	}
}

func TestWaitReady_StopsWhenTheContextEnds(t *testing.T) {
	p := &scriptedPinger{errs: []error{errRefused}}
	pol := db.WaitPolicy{Initial: time.Hour, Max: time.Hour, Budget: time.Hour, PingTimeout: time.Second}
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)

	start := time.Now()
	err := db.WaitReady(ctx, p, pol)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("WaitReady took %v to notice the cancel", elapsed)
	}
}

func TestWaitReady_BoundsEachAttempt(t *testing.T) {
	pol := db.WaitPolicy{Initial: time.Millisecond, Max: time.Millisecond, Budget: 50 * time.Millisecond, PingTimeout: 10 * time.Millisecond}
	start := time.Now()
	err := db.WaitReady(t.Context(), hangingPinger{}, pol)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady = %v, want the per-attempt deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a hanging ping held WaitReady for %v", elapsed)
	}
}
