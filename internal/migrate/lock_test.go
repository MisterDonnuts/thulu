package migrate

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRunSerializesConcurrentMigrators(t *testing.T) {
	db := openDB(t)
	migs := []Migration{
		{ID: 1, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY); SELECT pg_sleep(0.3)`},
		{ID: 2, SQL: `ALTER TABLE t ADD COLUMN name TEXT NOT NULL DEFAULT ''`},
	}
	const n = 4
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Run(context.Background(), db, "demo", migs)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("migrator %d: %v", i, err)
		}
	}
	if got := countApplied(t, db, "demo"); got != 2 {
		t.Fatalf("applied = %d, want 2", got)
	}
}

func TestRunWaitsForTheLockHolder(t *testing.T) {
	db := openDB(t)
	holder, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if _, err := holder.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	migs := []Migration{{ID: 1, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY)`}}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := Run(ctx, db, "demo", migs); err == nil {
		t.Fatal("Run migrated while another process held the lock")
	}
	var created bool
	if err := db.QueryRow(`SELECT to_regclass('t') IS NOT NULL`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("Run applied a migration while another process held the lock")
	}

	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), db, "demo", migs) }()
	time.Sleep(300 * time.Millisecond)
	if _, err := holder.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after the lock was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run still waiting 5 s after the lock was released")
	}
	if got := countApplied(t, db, "demo"); got != 1 {
		t.Fatalf("applied = %d, want 1", got)
	}
}

func TestRunReleasesTheLock(t *testing.T) {
	db := openDB(t)
	if err := Run(context.Background(), db, "demo", []Migration{{ID: 1, SQL: `CREATE TABLE t (id INTEGER PRIMARY KEY)`}}); err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := db.QueryRow(`SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("the migration lock is still held after Run returned")
	}
}
