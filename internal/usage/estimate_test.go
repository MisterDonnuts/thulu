package usage

import (
	"testing"
	"time"
)

func TestAddEstimatesUnreportedCost(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	p, ok := store.PricingForModel(ctx, "sonnet")
	if !ok {
		t.Fatal("no price for sonnet; the seed should carry one")
	}
	sess := makeSess("est-1", "agent", "dev", 1_000_000, 100_000, 0, 0, 3)
	if err := store.Add(ctx, sess); err != nil {
		t.Fatalf("Add: %v", err)
	}
	want := p.Cost(1_000_000, 100_000, 0, 0)
	got := store.Get(ctx, "est-1")
	if got == nil || !approxEqual(got.CostUSD, want) || !got.CostEstimated {
		t.Fatalf("session = %+v; want cost %v marked estimated", got, want)
	}

	all := store.RollupTotals(ctx, Everything())
	if !approxEqual(all.TotalCostUSD, want) {
		t.Errorf("rollup cost = %v, want %v", all.TotalCostUSD, want)
	}
	if !approxEqual(all.EstimatedCostUSD, want) {
		t.Errorf("rollup estimated = %v, want %v", all.EstimatedCostUSD, want)
	}
	tot := store.Totals(ctx, Everything())
	if !approxEqual(tot.EstimatedCostUSD, want) {
		t.Errorf("Totals.EstimatedCostUSD = %v, want %v", tot.EstimatedCostUSD, want)
	}
}

func TestAddKeepsReportedCost(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	if err := store.Add(ctx, makeSess("rep-1", "agent", "dev", 1_000_000, 0, 0, 0.42, 1)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := store.Get(ctx, "rep-1")
	if got == nil || !approxEqual(got.CostUSD, 0.42) || got.CostEstimated {
		t.Fatalf("session = %+v; want the reported 0.42, not estimated", got)
	}
}

func TestAddLeavesUnknownModelUnpriced(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	sess := makeSess("unk-1", "agent", "dev", 5000, 500, 0, 0, 1)
	sess.Model = "local-llm-7b"
	if err := store.Add(ctx, sess); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := store.Get(ctx, "unk-1")
	if got == nil || got.CostUSD != 0 || got.CostEstimated {
		t.Fatalf("session = %+v; want $0, not estimated", got)
	}
}

func TestEstimateUnpricedSessionsBackfills(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	sess := makeSess("late-1", "agent", "dev", 2_000_000, 0, 0, 0, 1)
	sess.Model = "local-llm-7b"
	if err := store.Add(ctx, sess); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO model_pricing
		(model, in_per_mtok, out_per_mtok, cache_read_per_mtok, cache_creation_per_mtok)
		VALUES ('local-llm-7b', 0.5, 1, 0, 0)`); err != nil {
		t.Fatalf("insert price: %v", err)
	}

	for pass := 1; pass <= 2; pass++ {
		if err := store.estimateUnpricedSessions(ctx); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		got := store.Get(ctx, "late-1")
		if got == nil || !approxEqual(got.CostUSD, 1.0) || !got.CostEstimated {
			t.Fatalf("pass %d: session = %+v; want $1 estimated", pass, got)
		}
		if all := store.RollupTotals(ctx, Everything()); !approxEqual(all.TotalCostUSD, 1.0) || !approxEqual(all.EstimatedCostUSD, 1.0) {
			t.Fatalf("pass %d: rollup = %+v, want $1, all of it estimated", pass, all)
		}
	}
}

func TestReportsCarryEstimatedShare(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	reported := mkSess("r", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.30, now)
	estimated := mkSess("e", "codex", "sonnet", "dev", "p1", "t1", 1_000_000, 0, 0, 0, now)
	seed(t, store, []Session{reported, estimated})
	est := store.Get(ctx, "e").CostUSD

	f := Filter{Scope: Everything()}
	rows, err := store.ByDimension(ctx, f, "project_id")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ByDimension = %v, %v", rows, err)
	}
	if !approxEqual(rows[0].EstimatedCostUSD, est) || !approxEqual(rows[0].CostUSD, 0.30+est) {
		t.Errorf("row = %+v; want estimated %v of %v", rows[0], est, 0.30+est)
	}
	tasks, err := store.TopTasks(ctx, f, 5)
	if err != nil || len(tasks) != 1 || !approxEqual(tasks[0].EstimatedCostUSD, est) {
		t.Errorf("TopTasks = %+v, %v; want estimated %v", tasks, err, est)
	}
	eff, err := store.Efficiency(ctx, f)
	if err != nil || !approxEqual(eff.EstimatedCostUSD, est) {
		t.Errorf("Efficiency = %+v, %v; want estimated %v", eff, err, est)
	}
}

func TestRollupTotalsOutlivesPrune(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	old := time.Now().UTC().AddDate(0, 0, -200)
	seed(t, store, []Session{
		mkSess("old", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.25, old),
	})
	if _, _, err := store.PruneSessions(ctx, time.Now().UTC().AddDate(0, 0, -90)); err != nil {
		t.Fatalf("PruneSessions: %v", err)
	}
	if tot := store.Totals(ctx, Everything()); tot.Sessions != 0 {
		t.Fatalf("Totals after prune = %+v, want no sessions", tot)
	}
	all := store.RollupTotals(ctx, Everything())
	if all.Sessions != 1 || !approxEqual(all.TotalCostUSD, 0.25) || all.TotalTokens != 150 {
		t.Errorf("RollupTotals = %+v; want 1 session, $0.25, 150 tokens", all)
	}
	if other := store.RollupTotals(ctx, InWorkspace("ws-other")); other.Sessions != 0 {
		t.Errorf("another workspace sees %+v, want nothing", other)
	}
}

func TestReportsCountUnpricedSessions(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	priced := mkSess("p", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.30, now)
	unknown := mkSess("u", "codex", "local-llm-7b", "dev", "p1", "t1", 400_000, 70_000, 0, 0, now)
	empty := mkSess("z", "codex", "local-llm-7b", "dev", "p1", "t1", 0, 0, 0, 0, now)
	seed(t, store, []Session{priced, unknown, empty})

	f := Filter{Scope: Everything()}
	rows, err := store.ByDimension(ctx, f, "model")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		want := 0
		if r.Key == "local-llm-7b" {
			want = 1
		}
		if r.UnpricedSessions != want {
			t.Errorf("model %s: unpriced = %d, want %d", r.Key, r.UnpricedSessions, want)
		}
	}
	tasks, err := store.TopTasks(ctx, f, 5)
	if err != nil || len(tasks) != 1 || tasks[0].UnpricedSessions != 1 {
		t.Errorf("TopTasks = %+v, %v; want 1 unpriced session", tasks, err)
	}
	eff, err := store.Efficiency(ctx, f)
	if err != nil || eff.UnpricedSessions != 1 {
		t.Errorf("Efficiency = %+v, %v; want 1 unpriced session", eff, err)
	}
	if tot := store.TotalsByIDs(ctx, []string{"p", "u"}); tot.UnpricedSessions != 1 {
		t.Errorf("TotalsByIDs = %+v; want 1 unpriced session", tot)
	}
}

func TestRollupEstimatedShareBackfillAndRebuild(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	seed(t, store, []Session{
		mkSess("r", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.30, now),
		mkSess("e", "codex", "sonnet", "dev", "p1", "t1", 1_000_000, 0, 0, 0, now),
	})
	est := store.Get(ctx, "e").CostUSD
	if _, err := store.DB().ExecContext(ctx, `UPDATE usage_daily SET estimated_cost_usd = 0`); err != nil {
		t.Fatal(err)
	}
	if err := store.backfillDailyEstimated(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if all := store.RollupTotals(ctx, Everything()); !approxEqual(all.EstimatedCostUSD, est) {
		t.Errorf("after backfill: estimated = %v, want %v", all.EstimatedCostUSD, est)
	}
	if err := store.RebuildDaily(ctx); err != nil {
		t.Fatal(err)
	}
	all := store.RollupTotals(ctx, Everything())
	if !approxEqual(all.EstimatedCostUSD, est) || !approxEqual(all.TotalCostUSD, 0.30+est) {
		t.Errorf("after rebuild: %+v, want %v estimated of %v", all, est, 0.30+est)
	}
}
