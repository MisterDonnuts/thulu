package usage

import (
	"context"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func seed(t *testing.T, store *Store, sessions []Session) {
	t.Helper()
	for _, s := range sessions {
		if err := store.Add(t.Context(), s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func mkSess(id, engine, model, agentType, projectID, taskID string, in, out, cacheRead int, cost float64, finished time.Time) Session {
	return Session{
		ID:           id,
		Agent:        "a-" + id,
		AgentType:    agentType,
		Engine:       engine,
		Model:        model,
		ProjectID:    projectID,
		TaskID:       taskID,
		Stage:        "build",
		StartedAt:    finished.Add(-10 * time.Minute),
		FinishedAt:   finished,
		DurationMs:   600000,
		Turns:        5,
		InputTokens:  in,
		OutputTokens: out,
		CacheRead:    cacheRead,
		CostUSD:      cost,
		StopReason:   "end_turn",
	}
}

func TestTimeSeries_DailyBuckets(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	seed(t, store, []Session{
		mkSess("s1", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.10, now.Add(-2*24*time.Hour)),
		mkSess("s2", "claude-code", "sonnet", "dev", "p1", "t1", 200, 100, 50, 0.25, now.Add(-2*24*time.Hour).Add(2*time.Hour)),
		mkSess("s3", "claude-code", "opus", "dev", "p1", "t2", 300, 150, 0, 0.40, now.Add(-1*24*time.Hour)),
	})
	pts, err := store.TimeSeries(context.Background(), Filter{
		Scope: Everything(),
		From:  now.Add(-7 * 24 * time.Hour),
		To:    now,
	}, BucketDay)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(pts) != 2 {
		t.Fatalf("want 2 day buckets, got %d (%+v)", len(pts), pts)
	}
	if pts[0].Sessions != 2 || pts[0].CostUSD < 0.349 || pts[0].CostUSD > 0.351 {
		t.Errorf("day1 = %+v, want sessions=2 cost~=0.35", pts[0])
	}
	if pts[1].Sessions != 1 {
		t.Errorf("day2 sessions = %d, want 1", pts[1].Sessions)
	}
}

func TestByDimension_GroupsAndPercentiles(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		s := mkSess("op"+itoa(i), "claude-code", "opus", "dev", "p", "t", 100, 50, 20, 0.10, now)
		s.DurationMs = int64(100 + i*100)
		if err := store.Add(t.Context(), s); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	rows, err := store.ByDimension(context.Background(), Filter{Scope: Everything(), From: now.Add(-time.Hour), To: now.Add(time.Hour)}, "model")
	if err != nil {
		t.Fatalf("ByDimension(model): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Key != "opus" {
		t.Errorf("key = %q, want opus", r.Key)
	}
	if r.Sessions != 10 || r.CostUSD < 0.99 || r.CostUSD > 1.01 {
		t.Errorf("aggregates wrong: sessions=%d cost=%.4f", r.Sessions, r.CostUSD)
	}
	if r.P50DurationMs == 0 || r.P95DurationMs == 0 {
		t.Errorf("percentiles missing: p50=%d p95=%d", r.P50DurationMs, r.P95DurationMs)
	}
	if r.P95DurationMs < r.P50DurationMs {
		t.Errorf("p95 < p50: %d < %d", r.P95DurationMs, r.P50DurationMs)
	}
	if r.CacheHitRatio < 0.16 || r.CacheHitRatio > 0.17 {
		t.Errorf("cache_hit_ratio = %.4f, want ~0.1667", r.CacheHitRatio)
	}
}

func TestFilter_DefaultsTo30Days(t *testing.T) {
	f := Filter{Scope: Everything()}.Defaults()
	if f.From.IsZero() || f.To.IsZero() {
		t.Fatal("Defaults left zero values")
	}
	if f.To.Sub(f.From) < 29*24*time.Hour || f.To.Sub(f.From) > 31*24*time.Hour {
		t.Errorf("default window %v, want ~30d", f.To.Sub(f.From))
	}
}

func TestFilter_AppliesDimensions(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	seed(t, store, []Session{
		mkSess("a", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.1, now),
		mkSess("b", "claude-code", "opus", "dev", "p1", "t1", 200, 100, 0, 0.3, now),
		mkSess("c", "opencode", "sonnet", "dev", "p2", "t2", 50, 20, 0, 0.05, now),
	})
	f := Filter{Scope: Everything(), From: now.Add(-time.Hour), To: now.Add(time.Hour), Engine: "opencode"}
	rows, err := store.ByDimension(context.Background(), f, "model")
	if err != nil {
		t.Fatalf("ByDimension(model): %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "sonnet" || rows[0].Sessions != 1 {
		t.Fatalf("filter not applied: %+v", rows)
	}
}

func TestByTool_AttributesCostShare(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	s := mkSess("sess1", "claude-code", "sonnet", "dev", "p", "t", 100, 100, 0, 1.0, now)
	seed(t, store, []Session{s})
	if err := store.AddToolEvents(context.Background(), []ToolEvent{
		{SessionID: "sess1", TS: now, TurnIndex: 1, ToolName: "Bash", ToolKind: "bash", InputBytes: 30},
		{SessionID: "sess1", TS: now, TurnIndex: 1, ToolName: "Read", ToolKind: "fs", InputBytes: 20},
		{SessionID: "sess1", TS: now, TurnIndex: 2, ToolName: "Skill", ToolKind: "skill", SkillName: "frontend-design", InputBytes: 50},
	}); err != nil {
		t.Fatalf("AddToolEvents: %v", err)
	}
	rows, err := store.ByTool(context.Background(), Filter{Scope: Everything(), From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("ByTool: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 tools, got %d (%+v)", len(rows), rows)
	}
	totalShare := 0.0
	for _, r := range rows {
		totalShare += r.CostShareUSD
		if r.Invocations != 1 || r.Sessions != 1 {
			t.Errorf("row %s: counts wrong: %+v", r.ToolName, r)
		}
	}
	if totalShare < 0.99 || totalShare > 1.01 {
		t.Errorf("total share = %.4f, want ~1.00", totalShare)
	}
	skills, err := store.BySkill(context.Background(), Filter{Scope: Everything(), From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("BySkill: %v", err)
	}
	if len(skills) != 1 || skills[0].Skill != "frontend-design" {
		t.Fatalf("BySkill wrong: %+v", skills)
	}
}

func TestEfficiency_MixAndRatios(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	good := mkSess("g", "claude-code", "sonnet", "dev", "p", "t", 100, 50, 200, 0.10, now)
	max := mkSess("m", "claude-code", "sonnet", "dev", "p", "t", 100, 50, 0, 0.10, now)
	max.StopReason = "max_tokens"
	err := mkSess("e", "claude-code", "sonnet", "dev", "p", "t", 100, 50, 0, 0.10, now)
	err.StopReason = "error_timeout"
	seed(t, store, []Session{good, max, err})

	eff, qerr := store.Efficiency(context.Background(), Filter{Scope: Everything(), From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	if qerr != nil {
		t.Fatalf("Efficiency: %v", qerr)
	}
	if eff.Sessions != 3 {
		t.Fatalf("sessions = %d, want 3", eff.Sessions)
	}
	wantMax := 1.0 / 3.0
	if eff.MaxTokensRate < wantMax-0.001 || eff.MaxTokensRate > wantMax+0.001 {
		t.Errorf("max_tokens_rate = %.4f, want ~0.333", eff.MaxTokensRate)
	}
	if eff.ErrorRate < wantMax-0.001 || eff.ErrorRate > wantMax+0.001 {
		t.Errorf("error_rate = %.4f, want ~0.333", eff.ErrorRate)
	}
	if eff.CacheHitRatio < 0.39 || eff.CacheHitRatio > 0.41 {
		t.Errorf("cache_hit_ratio = %.4f, want ~0.4", eff.CacheHitRatio)
	}
	if eff.StopReasonMix["end_turn"] != 1 || eff.StopReasonMix["max_tokens"] != 1 {
		t.Errorf("stop_reason_mix = %+v", eff.StopReasonMix)
	}
}

func TestBurnRate_DetectsSpike(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	for i := 0; i < 30; i++ {
		s := mkSess("b"+itoa(i), "claude-code", "sonnet", "dev", "p", "t",
			100, 50, 0, 0.10, now.Add(-time.Duration(i+2)*24*time.Hour))
		if err := store.Add(t.Context(), s); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	for i := 0; i < 10; i++ {
		s := mkSess("r"+itoa(i), "claude-code", "sonnet", "dev", "p", "t",
			100, 50, 0, 1.0, now.Add(-time.Duration(i)*time.Hour))
		if err := store.Add(t.Context(), s); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	br, err := store.BurnRate(context.Background(), Filter{Scope: Everything()}, 24, 30)
	if err != nil {
		t.Fatalf("BurnRate: %v", err)
	}
	if br.Ratio < 5 {
		t.Errorf("ratio = %.2f, expected a sharp spike (>5)", br.Ratio)
	}
}

func TestRollup_KeepsSyncOnAdd(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	seed(t, store, []Session{
		mkSess("a", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.10, now),
		mkSess("b", "claude-code", "sonnet", "dev", "p1", "t2", 200, 80, 0, 0.20, now),
	})
	pts, err := store.TimeSeries(context.Background(), Filter{
		Scope: Everything(),
		From:  now.Add(-1 * time.Hour),
		To:    now.Add(24 * time.Hour),
	}, BucketDay)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(pts) != 1 || pts[0].Sessions != 2 {
		t.Fatalf("rollup wrong: %+v", pts)
	}
	if pts[0].CostUSD < 0.29 || pts[0].CostUSD > 0.31 {
		t.Errorf("cost = %.4f, want ~0.30", pts[0].CostUSD)
	}
}

func TestRebuildDaily_RecomputesFromSessions(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(24 * time.Hour)
	seed(t, store, []Session{
		mkSess("a", "claude-code", "sonnet", "dev", "p1", "t1", 100, 50, 0, 0.10, now),
		mkSess("b", "claude-code", "sonnet", "dev", "p1", "t2", 200, 80, 0, 0.20, now),
	})
	if _, err := store.db.Exec(`DELETE FROM usage_daily`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.RebuildDaily(context.Background()); err != nil {
		t.Fatalf("RebuildDaily: %v", err)
	}
	pts, err := store.TimeSeries(context.Background(), Filter{
		Scope: Everything(),
		From:  now.Add(-1 * time.Hour),
		To:    now.Add(24 * time.Hour),
	}, BucketDay)
	if err != nil {
		t.Fatalf("TimeSeries: %v", err)
	}
	if len(pts) != 1 || pts[0].Sessions != 2 {
		t.Fatalf("rebuilt rollup wrong: %+v", pts)
	}
}

func TestPricing_SeedAndLookup(t *testing.T) {
	store := newTestStore(t)
	p, ok := store.PricingForModel(context.Background(), "sonnet")
	if !ok {
		t.Fatal("expected pricing for sonnet (seeded)")
	}
	if p.InPerMTok <= 0 {
		t.Errorf("seeded pricing missing input rate: %+v", p)
	}
	if _, ok := store.PricingForModel(context.Background(), "claude-haiku-4-5-20251001"); !ok {
		t.Errorf("expected haiku family fallback to resolve")
	}
	if _, ok := store.PricingForModel(context.Background(), "completely-unknown-model"); ok {
		t.Errorf("unknown model should not resolve")
	}
	if p, ok := store.PricingForModel(context.Background(), "gpt-4o-mini"); !ok || p.InPerMTok != 0.15 {
		t.Errorf("gpt-4o-mini = %+v, %v; want the codex list price", p, ok)
	}
}

func TestPricing_FallsBackToEngineList(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, store.rb(`DELETE FROM model_pricing WHERE model = ?`), "o3"); err != nil {
		t.Fatal(err)
	}
	p, ok := store.PricingForModel(ctx, "o3")
	if !ok || p.InPerMTok != 2.0 || p.OutPerMTok != 8.0 {
		t.Fatalf("o3 = %+v, %v; want the engine list price", p, ok)
	}
	if _, err := store.db.ExecContext(ctx, store.rb(`UPDATE model_pricing SET in_per_mtok = ? WHERE model = ?`), 9.0, "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.PricingForModel(ctx, "gpt-4o"); p.InPerMTok != 9.0 {
		t.Errorf("gpt-4o in = %v, want the operator's 9.0", p.InPerMTok)
	}
}

func TestClassifyTool_KnownAndMCP(t *testing.T) {
	cases := map[string]string{
		"Skill":           "skill",
		"Bash":            "bash",
		"Read":            "fs",
		"Edit":            "fs",
		"WebFetch":        "web",
		"mcp__notion__db": "mcp",
		"SomethingNew":    "other",
		"":                "",
	}
	for in, want := range cases {
		if got := ClassifyTool(in); got != want {
			t.Errorf("ClassifyTool(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkillFromInput_AcceptsBothKeys(t *testing.T) {
	if got := SkillFromInput(map[string]any{"skill": "loop"}); got != "loop" {
		t.Errorf("got %q, want loop", got)
	}
	if got := SkillFromInput(map[string]any{"name": "verify"}); got != "verify" {
		t.Errorf("got %q, want verify (via name fallback)", got)
	}
	if got := SkillFromInput(map[string]any{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestBackfill_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	db := testpg.New(t)
	store, err := NewStore(db, dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now().UTC()
	sess := mkSess("backfill-id", "claude-code", "sonnet", "dev", "p", "t", 100, 50, 0, 0.1, now)
	if err := store.Add(t.Context(), sess); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.AddToolEvents(context.Background(), []ToolEvent{
		{SessionID: "backfill-id", TS: now, TurnIndex: 1, ToolName: "Bash", ToolKind: "bash"},
	}); err != nil {
		t.Fatalf("AddToolEvents: %v", err)
	}
	if !store.SessionHasToolEvents(context.Background(), "backfill-id") {
		t.Fatal("expected events to exist")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
