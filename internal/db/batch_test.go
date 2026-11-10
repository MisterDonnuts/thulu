package db_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/db"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestDeleteBatched(t *testing.T) {
	conn := testpg.New(t)
	ctx := t.Context()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE t (n INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	const rows = db.DeleteBatchSize*2 + 500
	if _, err := conn.ExecContext(ctx, `INSERT INTO t SELECT g FROM generate_series(1, $1) g`, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}

	keepFrom := rows - 100
	n, err := db.DeleteBatched(ctx, conn, "t", `n < ?`, keepFrom)
	if err != nil {
		t.Fatalf("DeleteBatched: %v", err)
	}
	if n != int64(keepFrom-1) {
		t.Errorf("deleted %d, want %d", n, keepFrom-1)
	}
	var left int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != rows-keepFrom+1 {
		t.Errorf("rows left = %d, want %d", left, rows-keepFrom+1)
	}

	if n, err := db.DeleteBatched(ctx, conn, "t", `n < ?`, 0); err != nil || n != 0 {
		t.Errorf("no-match delete = %d, %v", n, err)
	}
}
