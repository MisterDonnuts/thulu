package usage

import (
	"context"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
)

// Cut at a UTC midnight: RebuildDaily recomputes the oldest remaining day and needs it whole.
// Spend survives in usage_daily, which Add fills in the same transaction.
func (s *Store) PruneSessions(ctx context.Context, cutoff time.Time) (sessions, toolEvents int64, err error) {
	day := utcMidnight(cutoff)
	// Events first: a failure between deletes leaves sessions without events, which the next sweep
	// retries.
	// NOT EXISTS keeps events of a session that spans the cut.
	toolEvents, err = dbpkg.DeleteBatched(ctx, s.db, "session_tool_events",
		`ts < ? AND NOT EXISTS (SELECT 1 FROM sessions s
			WHERE s.id = session_tool_events.session_id AND s.finished_at >= ?)`, day, day)
	if err != nil {
		return 0, toolEvents, err
	}
	for _, table := range []string{"session_calls", "session_cost_breakdown"} {
		if _, err = dbpkg.DeleteBatched(ctx, s.db, table,
			`NOT EXISTS (SELECT 1 FROM sessions s WHERE s.id = `+table+`.session_id AND s.finished_at >= ?)`, day); err != nil {
			return 0, toolEvents, err
		}
	}
	if _, err = dbpkg.DeleteBatched(ctx, s.db, "delegations", `created_at < ?`, day); err != nil {
		return 0, toolEvents, err
	}
	sessions, err = dbpkg.DeleteBatched(ctx, s.db, "sessions", `finished_at < ?`, day)
	return sessions, toolEvents, err
}
