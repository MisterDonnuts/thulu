package webhook

import (
	"context"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
)

// Pending outbox rows are never pruned, however old: the outbox is the only record of a delivery
// still owed.
func (s *Store) PruneBefore(ctx context.Context, cutoff time.Time) (deliveries, outbox int64, err error) {
	cutoff = cutoff.UTC()
	deliveries, err = dbpkg.DeleteBatched(ctx, s.db, "webhook_deliveries", `created_at < ?`, cutoff)
	if err != nil {
		return deliveries, 0, err
	}
	outbox, err = dbpkg.DeleteBatched(ctx, s.db, "webhook_outbox",
		`status IN (?, ?) AND updated_at < ?`, statusDelivered, statusFailed, cutoff)
	return deliveries, outbox, err
}
