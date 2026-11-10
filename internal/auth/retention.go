package auth

import (
	"context"
	"time"

	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
)

// Scope columns identify run keys. Operator and personal keys never carry them, so their revoked
// rows stay as the audit record.
func (s *UserStore) PruneRunKeys(ctx context.Context, cutoff time.Time) (int64, error) {
	return dbpkg.DeleteBatched(ctx, s.db, "api_keys",
		`(scope_task_id <> '' OR scope_chat_id <> '' OR scope_agent <> '')
		 AND (revoked_at < ? OR expires_at < ?)`,
		cutoff, cutoff)
}
