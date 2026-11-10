package usage

import (
	"testing"
	"time"
)

func rollupCost(t *testing.T, store *Store, day time.Time) float64 {
	t.Helper()
	var cost float64
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COALESCE(SUM(cost_usd), 0) FROM usage_daily WHERE day = $1`,
		day.UTC().Format("2006-01-02")).Scan(&cost); err != nil {
		t.Fatalf("rollup cost: %v", err)
	}
	return cost
}

func countTable(t *testing.T, store *Store, table string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestPruneSessions_KeepsRollupAcrossRebuild(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	today := utcMidnight(time.Now())
	oldDay := today.AddDate(0, 0, -100)
	cutDay := today.AddDate(0, 0, -90)

	seed(t, store, []Session{
		mkSess("old", "claude-code", "sonnet", "dev", "p1", "t1", 10, 5, 0, 1.50, oldDay.Add(12*time.Hour)),
		mkSess("edge", "claude-code", "sonnet", "dev", "p1", "t1", 10, 5, 0, 0.25, cutDay.Add(time.Hour)),
		mkSess("new", "claude-code", "sonnet", "dev", "p1", "t2", 10, 5, 0, 0.75, today.Add(-time.Hour)),
	})
	if err := store.AddToolEvents(ctx, []ToolEvent{
		{SessionID: "old", TS: oldDay.Add(11 * time.Hour), ToolName: "Read"},
		{SessionID: "new", TS: today.Add(-2 * time.Hour), ToolName: "Edit"},
		{SessionID: "ghost", TS: oldDay, ToolName: "Bash"},
	}); err != nil {
		t.Fatalf("AddToolEvents: %v", err)
	}

	sessions, events, err := store.PruneSessions(ctx, cutDay.Add(6*time.Hour))
	if err != nil {
		t.Fatalf("PruneSessions: %v", err)
	}
	if sessions != 1 || events != 2 {
		t.Errorf("pruned sessions=%d events=%d, want 1 and 2", sessions, events)
	}
	if store.Get(ctx, "old") != nil {
		t.Error("old session survived the prune")
	}
	if store.Get(ctx, "edge") == nil || store.Get(ctx, "new") == nil {
		t.Error("a session inside the window was pruned")
	}
	if n := countTable(t, store, "session_tool_events"); n != 1 {
		t.Errorf("tool events left = %d, want 1", n)
	}

	if got := rollupCost(t, store, oldDay); got < 1.49 || got > 1.51 {
		t.Errorf("pruned day's rollup cost = %.2f, want 1.50", got)
	}
	if err := store.RebuildDaily(ctx); err != nil {
		t.Fatalf("RebuildDaily: %v", err)
	}
	if got := rollupCost(t, store, oldDay); got < 1.49 || got > 1.51 {
		t.Errorf("RebuildDaily erased the pruned day: cost = %.2f, want 1.50", got)
	}
	if got := rollupCost(t, store, cutDay); got < 0.24 || got > 0.26 {
		t.Errorf("cut day's rollup cost = %.2f, want 0.25", got)
	}
}

func TestRebuildDaily_NoSessionsKeepsRollup(t *testing.T) {
	store := newTestStore(t)
	day := utcMidnight(time.Now()).AddDate(0, 0, -200)
	seed(t, store, []Session{
		mkSess("only", "claude-code", "sonnet", "dev", "p1", "t1", 10, 5, 0, 2.00, day.Add(time.Hour)),
	})
	if _, _, err := store.PruneSessions(t.Context(), time.Now()); err != nil {
		t.Fatalf("PruneSessions: %v", err)
	}
	if err := store.RebuildDaily(t.Context()); err != nil {
		t.Fatalf("RebuildDaily: %v", err)
	}
	if got := rollupCost(t, store, day); got < 1.99 || got > 2.01 {
		t.Errorf("rollup cost = %.2f, want 2.00", got)
	}
}
