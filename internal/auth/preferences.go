package auth

import (
	"context"
	"time"

	"github.com/MisterDonnuts/thulu/internal/migrate"
)

// How the dashboard looks to one person, kept with the account so it follows them to another
// browser. user_id is the actor id: a user's id, or ActorFromContext's id for the no-login dev
// session, which has no users row; that is why there is no foreign key and DeleteUser removes the
// rows itself.
func preferenceMigrations() []migrate.Migration {
	return []migrate.Migration{
		{ID: 32, SQL: `CREATE TABLE IF NOT EXISTS user_preferences (
				user_id    TEXT NOT NULL,
				pref_key   TEXT NOT NULL,
				value      TEXT NOT NULL,
				updated_at TIMESTAMPTZ NOT NULL,
				PRIMARY KEY (user_id, pref_key)
			)`},
	}
}

// Preferences returns every stored value of userID by key, as the JSON it was saved with.
func (s *UserStore) Preferences(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT pref_key, value FROM user_preferences WHERE user_id = ?`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetPreference stores value under key for userID, replacing what was there. The caller validates
// both: the store keeps whatever JSON it is given.
func (s *UserStore) SetPreference(ctx context.Context, userID, key, value string) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO user_preferences (user_id, pref_key, value, updated_at) VALUES (?,?,?,?)
		 ON CONFLICT (user_id, pref_key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`),
		userID, key, value, time.Now())
	return err
}
