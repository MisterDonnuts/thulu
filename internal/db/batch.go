package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const DeleteBatchSize = 1000

// Batched so a retention sweep holds row locks and a pool connection for one batch at most.
// ctid serves tables with no primary key (session_tool_events); `= ANY(ARRAY(...))`, not IN, gets a
// TID scan.
// table and where are SQL text, never caller data.
func DeleteBatched(ctx context.Context, db *sql.DB, table, where string, args ...any) (int64, error) {
	q := Rebind(`DELETE FROM ` + table + ` WHERE ctid = ANY(ARRAY(
		SELECT ctid FROM ` + table + ` WHERE ` + where + ` LIMIT ` + fmt.Sprint(DeleteBatchSize) + `))`)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			return total, apperr.Wrap(err, "db.errors.delete_batch", "table", table)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, apperr.Wrap(err, "db.errors.delete_rows_affected", "table", table)
		}
		total += n
		if n < DeleteBatchSize {
			return total, nil
		}
	}
}
