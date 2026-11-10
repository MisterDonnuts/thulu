package db_test

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/db"
)

func TestOpen_RejectsNonPostgresDSN(t *testing.T) {
	cases := []string{
		"",
		"/var/lib/octopus/octopus.db",
		":memory:",
		"sqlite:///tmp/x.db",
		"mysql://u:p@host/db",
	}
	for _, dsn := range cases {
		if _, err := db.Open(dsn); err == nil {
			t.Errorf("Open(%q) accepted a non-postgres DSN", dsn)
		}
	}
}

func TestOpen_PostgresHandle(t *testing.T) {
	handle, err := db.Open("postgres://u:p@localhost:5999/octopus?sslmode=disable")
	if err != nil {
		t.Fatalf("Open postgres handle: %v", err)
	}
	defer handle.Close()
}

func TestRebind(t *testing.T) {
	q := "SELECT * FROM t WHERE a = ? AND b = ?"
	want := "SELECT * FROM t WHERE a = $1 AND b = $2"
	if got := db.Rebind(q); got != want {
		t.Errorf("Rebind = %q, want %q", got, want)
	}
	in := "?,?,?,?,?,?,?,?,?,?"
	wantMulti := "$1,$2,$3,$4,$5,$6,$7,$8,$9,$10"
	if got := db.Rebind(in); got != wantMulti {
		t.Errorf("multi-digit Rebind = %q, want %q", got, wantMulti)
	}
	verbatim := "SELECT 1"
	if got := db.Rebind(verbatim); got != verbatim {
		t.Errorf("verbatim Rebind = %q, want %q", got, verbatim)
	}
}

func TestRebind_RepeatedCallsAgree(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"SELECT * FROM t WHERE a = ?", "SELECT * FROM t WHERE a = $1"},
		{"SELECT * FROM t WHERE a = ? AND b = ? AND c = ?", "SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3"},
		{"UPDATE t SET a = $1 WHERE id = $2", "UPDATE t SET a = $1 WHERE id = $2"},
		{"?", "$1"},
	}
	for _, c := range cases {
		first := db.Rebind(c.in)
		if first != c.want {
			t.Errorf("Rebind(%q) = %q, want %q", c.in, first, c.want)
		}
		for i := 0; i < 3; i++ {
			if again := db.Rebind(c.in); again != first {
				t.Errorf("Rebind(%q) call %d = %q, want %q", c.in, i+2, again, first)
			}
		}
	}
}

func TestRebind_VaryingPlaceholderCounts(t *testing.T) {
	for n := 1; n <= 12; n++ {
		in := strings.Repeat("?,", n-1) + "?"
		parts := make([]string, n)
		for i := range parts {
			parts[i] = "$" + strconv.Itoa(i+1)
		}
		want := strings.Join(parts, ",")
		if got := db.Rebind(in); got != want {
			t.Errorf("Rebind(%q) = %q, want %q", in, got, want)
		}
		if got := db.Rebind(in); got != want {
			t.Errorf("cached Rebind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRebind_BeyondCacheCap(t *testing.T) {
	for i := 0; i < 2000; i++ {
		in := "SELECT " + strconv.Itoa(i) + " FROM t WHERE a = ? AND b = ?"
		want := "SELECT " + strconv.Itoa(i) + " FROM t WHERE a = $1 AND b = $2"
		if got := db.Rebind(in); got != want {
			t.Fatalf("Rebind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRebind_ConcurrentCallersAgree(t *testing.T) {
	const q = "SELECT * FROM tasks WHERE workspace_id = ? AND board_id = ? AND stage = ?"
	const want = "SELECT * FROM tasks WHERE workspace_id = $1 AND board_id = $2 AND stage = $3"
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if got := db.Rebind(q); got != want {
					t.Errorf("Rebind = %q, want %q", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

const benchQuery = `INSERT INTO tasks (id, title, body, stage, stage_assignments,
	    branch_name, worktree_paths, pr_urls, current_run_id, current_instance,
	    status, tags, priority, workspace_id, board_id, type_id, parent_id, created_at, updated_at)
	 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

var benchSink string

func BenchmarkRebind(b *testing.B) {
	for i := 0; i < b.N; i++ {
		benchSink = db.Rebind(benchQuery)
	}
}

func BenchmarkRebindNoPlaceholders(b *testing.B) {
	const q = `SELECT id, title FROM tasks WHERE workspace_id = $1 ORDER BY created_at DESC`
	for i := 0; i < b.N; i++ {
		benchSink = db.Rebind(q)
	}
}

func BenchmarkRebindParallel(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			benchSink = db.Rebind(benchQuery)
		}
	})
}
