package sandbox

import (
	"context"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
)

func (s *Store) PruneReleasedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return dbpkg.DeleteBatched(ctx, s.db, "task_sandboxes",
		`status IN (?, ?) AND COALESCE(released_at, last_activity_at) < ?`,
		string(StatusEvicted), string(StatusFailed), cutoff.UTC())
}
