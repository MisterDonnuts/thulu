package singleton

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// "octodmon" in ASCII; every daemon version must use the same key or two can run at once.
// Advisory locks are per database, so installs sharing a Postgres server do not block each other.
const lockKey int64 = 0x6f63746f646d6f6e

type Options struct {
	Poll      time.Duration
	Notice    time.Duration
	Heartbeat time.Duration
	// Deadline plus Heartbeat must stay below IdleTimeout so the old holder gives up before
	// Postgres frees the lock for a new one.
	Deadline    time.Duration
	IdleTimeout time.Duration
	// SingleReplica is an install with one daemon and nobody to take over (Docker compose). There a
	// database too slow to confirm the lease must not stop the only daemon: past Deadline the lease
	// asks Postgres who holds the lock, takes it back on a new session when nobody does, and is
	// lost only when another daemon holds it. Retries back off up to MaxBackoff.
	SingleReplica bool
	MaxBackoff    time.Duration
}

var Defaults = Options{
	Poll:        2 * time.Second,
	Notice:      30 * time.Second,
	Heartbeat:   3 * time.Second,
	Deadline:    9 * time.Second,
	IdleTimeout: 20 * time.Second,
	MaxBackoff:  30 * time.Second,
}

func (o Options) withDefaults() Options {
	if o.Poll <= 0 {
		o.Poll = Defaults.Poll
	}
	if o.Notice <= 0 {
		o.Notice = Defaults.Notice
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = Defaults.Heartbeat
	}
	if o.Deadline <= 0 {
		o.Deadline = Defaults.Deadline
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = Defaults.IdleTimeout
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = Defaults.MaxBackoff
	}
	return o
}

type Lease struct {
	db   *sql.DB
	conn *sql.Conn
	// The backend that holds the lock, so a lease that cannot confirm tells its own session from
	// another daemon's.
	pid  int
	opts Options
	lost chan struct{}
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func Acquire(ctx context.Context, db *sql.DB, opts Options) (*Lease, error) {
	opts = opts.withDefaults()
	if opts.Deadline+opts.Heartbeat >= opts.IdleTimeout {
		return nil, apperr.Invalid("singleton.errors.timing",
			"deadline", opts.Deadline.String(), "heartbeat", opts.Heartbeat.String(), "idle", opts.IdleTimeout.String())
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, apperr.Wrap(err, "singleton.errors.connect")
	}
	if waitErr := waitForLock(ctx, conn, opts); waitErr != nil {
		discard(conn)
		return nil, waitErr
	}
	var pid int
	if err := conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		discard(conn)
		return nil, apperr.Wrap(err, "singleton.errors.lock")
	}
	l := &Lease{db: db, conn: conn, pid: pid, opts: opts,
		lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	// Uncanceled on purpose: the lease is held through orderly shutdown until Release, not until
	// the signal that starts it.
	go l.heartbeat(context.WithoutCancel(ctx))
	return l, nil
}

// Try the lock instead of waiting in Postgres: the pool's lock_timeout (internal/db) would end the
// wait after 10 s.
func waitForLock(ctx context.Context, conn *sql.Conn, opts Options) error {
	if err := configure(ctx, conn, opts); err != nil {
		return err
	}
	start := time.Now()
	var lastNotice time.Time
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil {
			return apperr.Wrap(err, "singleton.errors.lock")
		}
		if got {
			if waited := time.Since(start); waited > opts.Poll {
				log.Printf("[singleton] %s", i18n.Log("singleton.log.acquired_after_wait", "waited", waited.Round(time.Second).String()))
			}
			return nil
		}
		if lastNotice.IsZero() || time.Since(lastNotice) >= opts.Notice {
			log.Printf("[singleton] %s", i18n.Log("singleton.log.waiting", "waited", time.Since(start).Round(time.Second).String()))
			lastNotice = time.Now()
		}
		select {
		case <-ctx.Done():
			return apperr.Wrap(ctx.Err(), "singleton.errors.lock")
		case <-time.After(opts.Poll):
		}
	}
}

func configure(ctx context.Context, conn *sql.Conn, opts Options) error {
	// SET does not take bind parameters.
	idle := fmt.Sprintf("SET idle_session_timeout = %d", opts.IdleTimeout.Milliseconds())
	if _, err := conn.ExecContext(ctx, idle); err != nil {
		return apperr.Wrap(err, "singleton.errors.configure")
	}
	return nil
}

// What the heartbeat sends; a test swaps it for a query that outlasts the heartbeat.
var pingSQL = `SELECT 1`

func (l *Lease) heartbeat(base context.Context) {
	defer close(l.done)
	t := time.NewTicker(l.opts.Heartbeat)
	defer t.Stop()
	lastOK := time.Now()
	var retry retryState
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(base, l.opts.Heartbeat)
		_, err := l.conn.ExecContext(ctx, pingSQL)
		cancel()
		if err == nil {
			if !retry.since.IsZero() {
				log.Printf("[singleton] %s", i18n.Log("singleton.log.confirmed_again", "since", time.Since(lastOK).Round(time.Second).String()))
			}
			lastOK, retry = time.Now(), retryState{}
			continue
		}
		silent := time.Since(lastOK)
		if silent < l.opts.Deadline {
			continue
		}
		if !l.opts.SingleReplica {
			log.Printf("[singleton] %s", i18n.Log("singleton.log.lost", "since", silent.Round(time.Second).String(), "err", err))
			close(l.lost)
			return
		}
		if !retry.due(lastOK, l.opts) {
			continue
		}
		switch l.recover(base) {
		case holderOther:
			log.Printf("[singleton] %s", i18n.Log("singleton.log.taken", "since", silent.Round(time.Second).String()))
			close(l.lost)
			return
		case holderReclaimed:
			log.Printf("[singleton] %s", i18n.Log("singleton.log.reclaimed", "since", silent.Round(time.Second).String()))
			lastOK, retry = time.Now(), retryState{}
		default:
			retry.backOff(l.opts, silent, err)
		}
	}
}

// retryState paces a single-replica lease's checks while the database does not confirm it: the
// first one at Deadline, then each wait doubling up to MaxBackoff.
type retryState struct {
	since time.Time
	next  time.Time
	wait  time.Duration
	told  bool
}

func (r *retryState) due(lastOK time.Time, opts Options) bool {
	if r.since.IsZero() {
		r.since, r.next, r.wait = lastOK, time.Now(), opts.Heartbeat
	}
	return !time.Now().Before(r.next)
}

func (r *retryState) backOff(opts Options, silent time.Duration, err error) {
	if !r.told {
		// Said once per silence: the backoff goes on quietly until the database answers.
		r.told = true
		log.Printf("[singleton] %s", i18n.Log("singleton.log.unconfirmed", "since", silent.Round(time.Second).String(), "err", err))
	}
	r.next = time.Now().Add(r.wait)
	r.wait = min(2*r.wait, opts.MaxBackoff)
}

type holder int

const (
	// Postgres did not answer, or still shows this lease's own session holding the lock.
	holderUnknown holder = iota
	holderReclaimed
	holderOther
)

// recover asks a fresh session who holds the lock. This lease's own session still holding it means
// only the pings are slow. Nobody holding it means the session ended (a canceled ping closes it):
// the lease takes the lock on the fresh session and keeps going. Another backend holding it means
// another daemon took over.
func (l *Lease) recover(base context.Context) holder {
	ctx, cancel := context.WithTimeout(base, l.opts.MaxBackoff)
	defer cancel()
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return holderUnknown
	}
	keep := false
	defer func() {
		if !keep {
			discard(conn)
		}
	}()
	var pid int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(pid), 0) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND objsubid = 1
		  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND ((classid::bigint << 32) | objid::bigint) = $1`, lockKey).Scan(&pid); err != nil {
		return holderUnknown
	}
	switch {
	case pid == l.pid:
		return holderUnknown
	case pid != 0:
		return holderOther
	}
	if configure(ctx, conn, l.opts) != nil {
		return holderUnknown
	}
	var got bool
	var newPID int
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1), pg_backend_pid()`, lockKey).Scan(&got, &newPID); err != nil {
		return holderUnknown
	}
	if !got {
		return holderOther
	}
	discard(l.conn)
	l.conn, l.pid, keep = conn, newPID, true
	return holderReclaimed
}

func (l *Lease) Lost() <-chan struct{} { return l.lost }

func (l *Lease) Release() {
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		discard(l.conn)
	})
}

// Close the session instead of pooling it: ending it frees the lock even if unlock fails, and its
// idle_session_timeout must not reach a pooled connection.
func discard(conn *sql.Conn) {
	// Returning driver.ErrBadConn from Raw makes database/sql drop the connection; any other error
	// is worth logging. A connection database/sql already dropped (a canceled ping closes it) has
	// nothing left to close.
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, sql.ErrConnDone) {
		return
	}
	if !errors.Is(err, driver.ErrBadConn) {
		logx.Warn(err, "singleton.log.drop_conn")
	}
	if err := conn.Close(); !errors.Is(err, sql.ErrConnDone) {
		logx.Warn(err, "singleton.log.close_conn")
	}
}
