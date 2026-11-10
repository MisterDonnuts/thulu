package auth

import (
	"testing"
	"time"
)

func TestPruneRunKeys(t *testing.T) {
	store := newSSOTestStore(t)
	ctx := t.Context()
	old := time.Now().Add(-10 * 24 * time.Hour)
	cutoff := time.Now().Add(-7 * 24 * time.Hour)

	_, operator, err := store.CreateAPIKey(ctx, "ci", "user-1",
		KeyGrant{WorkspaceID: "ws-1", Role: RoleDeveloper, Permissions: []Permission{PermAll}})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	_, revokedRun, err := store.CreateRunScopedAPIKey(ctx, RunScope{TaskID: "t1", Agent: "dev"}, "ws-1", time.Hour)
	if err != nil {
		t.Fatalf("mint run key: %v", err)
	}
	_, expiredChat, err := store.CreateRunScopedAPIKey(ctx, RunScope{ChatID: "c1", Agent: "dev"}, "ws-1", time.Hour)
	if err != nil {
		t.Fatalf("mint chat key: %v", err)
	}
	_, liveRun, err := store.CreateRunScopedAPIKey(ctx, RunScope{TaskID: "t2", Agent: "dev"}, "ws-1", time.Hour)
	if err != nil {
		t.Fatalf("mint live key: %v", err)
	}

	for _, q := range []struct {
		sql string
		id  string
	}{
		{`UPDATE api_keys SET revoked_at = $2 WHERE id = $1`, operator.ID},
		{`UPDATE api_keys SET revoked_at = $2 WHERE id = $1`, revokedRun.ID},
		{`UPDATE api_keys SET expires_at = $2 WHERE id = $1`, expiredChat.ID},
	} {
		if _, err := store.db.ExecContext(ctx, q.sql, q.id, old); err != nil {
			t.Fatalf("backdate %s: %v", q.id, err)
		}
	}

	n, err := store.PruneRunKeys(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneRunKeys: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d keys, want 2", n)
	}
	for id, want := range map[string]bool{
		operator.ID: true, revokedRun.ID: false, expiredChat.ID: false, liveRun.ID: true,
	} {
		var exists bool
		if err := store.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM api_keys WHERE id = $1)`, id).Scan(&exists); err != nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if exists != want {
			t.Errorf("key %s exists=%t, want %t", id, exists, want)
		}
	}
}
