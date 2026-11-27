package singleton

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

var fast = Options{
	Poll:        50 * time.Millisecond,
	Notice:      time.Hour,
	Heartbeat:   100 * time.Millisecond,
	Deadline:    300 * time.Millisecond,
	IdleTimeout: time.Second,
}

func holderPID(t *testing.T, db *sql.DB) int {
	t.Helper()
	var pid int
	err := db.QueryRow(`SELECT COALESCE(MAX(pid), 0) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND ((classid::bigint << 32) | objid::bigint) = $1`, lockKey).Scan(&pid)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestSecondDaemonWaitsForTheFirst(t *testing.T) {
	db := testpg.New(t)
	first, err := Acquire(t.Context(), db, fast)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if l, err := Acquire(ctx, db, fast); err == nil {
		l.Release()
		t.Fatal("a second daemon took the lease while the first held it")
	}

	got := make(chan *Lease, 1)
	go func() {
		l, err := Acquire(t.Context(), db, fast)
		if err != nil {
			t.Error(err)
		}
		got <- l
	}()
	time.Sleep(200 * time.Millisecond)
	first.Release()
	select {
	case l := <-got:
		if l != nil {
			l.Release()
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the second daemon did not take the lease 3 s after the first released it")
	}
}

func TestHolderReportsLostSession(t *testing.T) {
	db := testpg.New(t)
	l, err := Acquire(t.Context(), db, fast)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	pid := holderPID(t, db)
	if pid == 0 {
		t.Fatal("no backend holds the daemon lock after Acquire")
	}
	if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Lost():
	case <-time.After(3 * time.Second):
		t.Fatal("Lost not closed 3 s after the session was terminated")
	}
}

func TestSilentHolderIsReplaced(t *testing.T) {
	db := testpg.New(t)
	silent := fast
	silent.Heartbeat = time.Hour
	silent.Deadline = time.Hour
	silent.IdleTimeout = 2*time.Hour + time.Second
	l, err := Acquire(t.Context(), db, silent)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err := l.conn.ExecContext(t.Context(), `SET idle_session_timeout = 500`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	next, err := Acquire(t.Context(), db, fast)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Release()
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("replacement took the lease after %s, want about the idle timeout", waited)
	}
}

func TestReleaseFreesTheLock(t *testing.T) {
	db := testpg.New(t)
	l, err := Acquire(t.Context(), db, fast)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l.Release()
	if pid := holderPID(t, db); pid != 0 {
		t.Fatalf("backend %d still holds the daemon lock after Release", pid)
	}
}

func TestAcquireRefusesUnsafeTiming(t *testing.T) {
	db := testpg.New(t)
	bad := fast
	bad.Deadline = bad.IdleTimeout
	if l, err := Acquire(t.Context(), db, bad); err == nil {
		l.Release()
		t.Fatal("Acquire accepted a deadline that outlives the idle timeout")
	}
}

func singleReplica() Options {
	o := fast
	o.SingleReplica = true
	o.MaxBackoff = 200 * time.Millisecond
	return o
}

// Kubernetes keeps the old safety: a lease that cannot confirm is lost, so the replacement pod takes
// over. The Docker install has no replacement: the only daemon takes the lock back on a new session
// when its own ended, and keeps running.
func TestSingleReplicaTakesTheLockBackWhenItsSessionEnds(t *testing.T) {
	db := testpg.New(t)
	l, err := Acquire(t.Context(), db, singleReplica())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	old := holderPID(t, db)
	if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, old); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pid := holderPID(t, db); pid != 0 && pid != old {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid := holderPID(t, db); pid == 0 || pid == old {
		t.Fatalf("holder after the session ended = %d (was %d); want the lease back on a new session", pid, old)
	}
	select {
	case <-l.Lost():
		t.Fatal("the only daemon gave up a lease nobody else took")
	case <-time.After(500 * time.Millisecond):
	}
	l.Release()
	if pid := holderPID(t, db); pid != 0 {
		t.Errorf("backend %d still holds the lock after Release of a reclaimed lease", pid)
	}
}

// Pings that time out under load close their session; the lease keeps taking the lock back instead
// of stopping the daemon.
func TestSingleReplicaSurvivesPingsThatTimeOut(t *testing.T) {
	db := testpg.New(t)
	pingSQL = `SELECT pg_sleep(0.5)`
	defer func() { pingSQL = `SELECT 1` }()
	l, err := Acquire(t.Context(), db, singleReplica())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.Lost():
		t.Fatal("a slow database stopped the only daemon")
	case <-time.After(1500 * time.Millisecond):
	}
	l.Release()
	if pid := holderPID(t, db); pid != 0 {
		t.Errorf("backend %d still holds the lock after Release", pid)
	}
}

// Only another daemon holding the lock ends a single-replica lease.
func TestSingleReplicaStopsWhenAnotherDaemonTookTheLock(t *testing.T) {
	db := testpg.New(t)
	l, err := Acquire(t.Context(), db, singleReplica())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, holderPID(t, db)); err != nil {
		t.Fatal(err)
	}
	// The newcomer polls faster than the lease's deadline, so it wins the freed lock.
	other, err := Acquire(t.Context(), db, fast)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	select {
	case <-l.Lost():
	case <-time.After(3 * time.Second):
		t.Fatal("the lease kept running while another daemon held the lock")
	}
}

func TestKubernetesLeaseStillStopsWithoutConfirmation(t *testing.T) {
	db := testpg.New(t)
	pingSQL = `SELECT pg_sleep(0.5)`
	defer func() { pingSQL = `SELECT 1` }()
	l, err := Acquire(t.Context(), db, fast)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	select {
	case <-l.Lost():
	case <-time.After(3 * time.Second):
		t.Fatal("a lease with replicas kept running without confirmation")
	}
}
