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
}

var Defaults = Options{
	Poll:        2 * time.Second,
	Notice:      30 * time.Second,
	Heartbeat:   3 * time.Second,
	Deadline:    9 * time.Second,
	IdleTimeout: 20 * time.Second,
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
	return o
}

type Lease struct {
	conn *sql.Conn
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
	l := &Lease{conn: conn, opts: opts, lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	// Uncanceled on purpose: the lease is held through orderly shutdown until Release, not until
	// the signal that starts it.
	go l.heartbeat(context.WithoutCancel(ctx))
	return l, nil
}

// Try the lock instead of waiting in Postgres: the pool's lock_timeout (internal/db) would end the
// wait after 10 s.
func waitForLock(ctx context.Context, conn *sql.Conn, opts Options) error {
	// SET does not take bind parameters.
	idle := fmt.Sprintf("SET idle_session_timeout = %d", opts.IdleTimeout.Milliseconds())
	if _, err := conn.ExecContext(ctx, idle); err != nil {
		return apperr.Wrap(err, "singleton.errors.configure")
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

func (l *Lease) heartbeat(base context.Context) {
	defer close(l.done)
	t := time.NewTicker(l.opts.Heartbeat)
	defer t.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(base, l.opts.Heartbeat)
		_, err := l.conn.ExecContext(ctx, `SELECT 1`)
		cancel()
		if err == nil {
			lastOK = time.Now()
			continue
		}
		if time.Since(lastOK) >= l.opts.Deadline {
			log.Printf("[singleton] %s", i18n.Log("singleton.log.lost", "since", time.Since(lastOK).Round(time.Second).String(), "err", err))
			close(l.lost)
			return
		}
	}
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
	// is worth logging.
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		logx.Warn(err, "singleton.log.drop_conn")
	}
	logx.Warn(conn.Close(), "singleton.log.close_conn")
}
