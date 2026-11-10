package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

const attributedRunLog = `2026-10-20T10:00:00Z {"type":"octopus.token_optimizer","level":"enforce","threshold_bytes":24576}
2026-10-20T10:00:01Z {"type":"system","subtype":"init","model":"claude-sonnet-4-6","session_id":"s"}
2026-10-20T10:00:02Z {"type":"assistant","message":{"id":"m1","model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/repo/big.go"}}],"usage":{"input_tokens":0,"cache_creation_input_tokens":10000,"output_tokens":1}}}
2026-10-20T10:00:02Z {"type":"assistant","message":{"id":"m1","model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t2","name":"mcp__octopus-api__task_get","input":{"task_id":"abc"}}],"usage":{"input_tokens":0,"cache_creation_input_tokens":10000,"output_tokens":1}}}
2026-10-20T10:00:03Z {"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + `"}]}}
2026-10-20T10:00:03Z {"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"yy"}]}}
2026-10-20T10:00:04Z {"type":"assistant","message":{"id":"m2","model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"octopus-delegate read --question q --paths a b"}}],"usage":{"input_tokens":0,"cache_read_input_tokens":10000,"cache_creation_input_tokens":3000,"output_tokens":1}}}
2026-10-20T10:00:05Z {"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","content":"answer\noctopus-delegate: id=dlg_00000000000000aa model=gemini in=10 out=2 cost=0.0040"}]}}
2026-10-20T10:00:06Z {"type":"assistant","message":{"id":"m3","model":"claude-sonnet-4-6","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":0,"cache_read_input_tokens":13000,"cache_creation_input_tokens":100,"output_tokens":1}}}
2026-10-20T10:00:07Z {"type":"result","subtype":"success","total_cost_usd":0.12,"num_turns":3,"usage":{"input_tokens":0,"output_tokens":200}}
`

func parseLog(t *testing.T, logs string) []engine.Event {
	t.Helper()
	var evs []engine.Event
	for ev := range engine.MustGet("").ParseStream(strings.NewReader(logs)) {
		evs = append(evs, ev)
	}
	return evs
}

func seedAttributedRun(t *testing.T, store *Store, id, taskID string) {
	t.Helper()
	ctx := t.Context()
	sess := makeSess(id, "dev", "dev", 0, 200, 23000, 0.12, 3)
	sess.Model = "claude-sonnet-4-6"
	sess.TaskID = taskID
	sess.WorkspaceID = "ws1"
	sess.Stage = "in-progress"
	if err := store.Add(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDelegation(ctx, Delegation{
		ID: "dlg_00000000000000aa", WorkspaceID: "ws1", TaskID: taskID, Agent: "dev", Stage: "in-progress",
		Kind: "read", Model: "gemini", InputTokens: 10, OutputTokens: 2, CostUSD: 0.004, Status: DelegationOK,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAttribution(ctx, id, parseLog(t, attributedRunLog), TokenOptimizerLevel(attributedRunLog)); err != nil {
		t.Fatalf("SaveAttribution: %v", err)
	}
}

func TestSaveAttribution_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	seedAttributedRun(t, store, "run1", "task1")

	b, err := store.Breakdown(ctx, "run1")
	if err != nil {
		t.Fatal(err)
	}
	if b.AttributionVersion != AttributionVersion || b.APICalls != 3 || b.BaseTokens != 10000 || b.TokenOptimizer != "enforce" {
		t.Fatalf("breakdown header = %+v", b)
	}
	if len(b.Calls) != 3 || b.Calls[1].CacheRead != 10000 || b.Calls[0].TS == nil {
		t.Fatalf("calls = %+v", b.Calls)
	}
	var sum, delegated float64
	cats := map[string]float64{}
	for _, c := range b.Categories {
		cats[c.Category] = c.CostUSD
		if c.Category == CategoryDelegation {
			delegated = c.CostUSD
			continue
		}
		sum += c.CostUSD
	}
	if !near(sum, 0.12) {
		t.Errorf("categories without delegation add up to %v, want 0.12", sum)
	}
	if !near(delegated, 0.004) {
		t.Errorf("delegation category = %v, want 0.004", delegated)
	}
	if len(b.Tools) != 3 {
		t.Fatalf("tools = %+v", b.Tools)
	}
	byID := map[string]ToolCostRow{}
	for _, r := range b.Tools {
		byID[r.ToolUseID] = r
	}
	if r := byID["t1"]; r.InputSummary != "/repo/big.go" || r.TurnIndex != 1 || r.CarriedCalls != 2 || r.CostUSD <= 0 {
		t.Errorf("Read row = %+v", r)
	}
	if r := byID["t2"]; r.MCPServer != "octopus-api" || r.ToolKind != "mcp" {
		t.Errorf("MCP row = %+v", r)
	}
	if r := byID["t3"]; r.DelegationID != "dlg_00000000000000aa" || !near(r.DelegatedCostUSD, 0.004) || r.TurnIndex != 2 {
		t.Errorf("Bash row = %+v", r)
	}

	if err := store.SaveAttribution(ctx, "run1", parseLog(t, attributedRunLog), "enforce"); err != nil {
		t.Fatal(err)
	}
	if n := countTable(t, store, "session_tool_events"); n != 3 {
		t.Errorf("tool events after a second save = %d, want 3", n)
	}
}

func TestAttributionReports(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	seedAttributedRun(t, store, "run1", "task1")
	f := Filter{Scope: InWorkspace("ws1")}

	tb, ok, err := store.TaskBreakdownFor(ctx, "task1", []string{"run1", "missing"})
	if err != nil || !ok {
		t.Fatalf("TaskBreakdownFor ok=%v err=%v", ok, err)
	}
	if !near(tb.CostUSD, 0.12) || tb.Delegations.Count != 1 || len(tb.TopCalls) != 3 || tb.TopCalls[0].Agent != "dev" {
		t.Errorf("task breakdown = %+v", tb)
	}
	if _, ok, _ := store.TaskBreakdownFor(ctx, "task2", []string{"unknown"}); ok {
		t.Error("a task with no attributed run reported a breakdown")
	}

	tools, err := store.ByTool(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	var toolsCost float64
	for _, r := range tools {
		toolsCost += r.CostUSD
		if r.CostUSD != r.CostShareUSD {
			t.Errorf("%s: cost_usd %v != cost_share_usd %v", r.ToolName, r.CostUSD, r.CostShareUSD)
		}
	}
	b, _ := store.Breakdown(ctx, "run1")
	var toolsCat float64
	for _, c := range b.Categories {
		if c.Category == CategoryTools {
			toolsCat = c.CostUSD
		}
	}
	if !near(toolsCost, toolsCat+0.004) {
		t.Errorf("by-tool total %v, want tools category %v plus the delegation", toolsCost, toolsCat)
	}

	mcp, err := store.ByMCPServer(ctx, f)
	if err != nil || len(mcp) != 1 || mcp[0].MCPServer != "octopus-api" || mcp[0].Invocations != 1 {
		t.Errorf("ByMCPServer = %+v, %v", mcp, err)
	}
	levels, err := store.ByTokenOptimizer(ctx, f)
	if err != nil || len(levels) != 1 || levels[0].Level != "enforce" || !near(levels[0].DelegationsCostUSD, 0.004) {
		t.Errorf("ByTokenOptimizer = %+v, %v", levels, err)
	}
	rep, err := store.Delegations(ctx, f, 10)
	if err != nil || rep.Summary.Count != 1 || rep.Summary.OK != 1 || len(rep.ByAgent) != 1 || len(rep.Recent) != 1 {
		t.Errorf("Delegations = %+v, %v", rep, err)
	}
	other, err := store.Delegations(ctx, Filter{Scope: InWorkspace("ws2")}, 10)
	if err != nil || other.Summary.Count != 0 {
		t.Errorf("another workspace sees %d delegations (%v)", other.Summary.Count, err)
	}
}

func TestReattributeSessions(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "dev_2026-10-20_10-00-00_run1.log")
	if err := os.WriteFile(path, []byte(attributedRunLog), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := makeSess("run1", "dev", "dev", 0, 200, 23000, 0.12, 3)
	sess.LogsFile = path
	if err := store.Add(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := store.AddToolEvents(ctx, []ToolEvent{{SessionID: "run1", TS: time.Now(), ToolName: "Read", ToolKind: "fs"}}); err != nil {
		t.Fatal(err)
	}
	n, err := store.ReattributeSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ReattributeSessions = %d, %v", n, err)
	}
	if got := countTable(t, store, "session_tool_events"); got != 3 {
		t.Errorf("tool events = %d, want the 3 the log names", got)
	}
	if n, _ := store.ReattributeSessions(ctx); n != 0 {
		t.Errorf("second pass redid %d sessions", n)
	}
}

func TestPruneSessions_TakesTheAttribution(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	seedAttributedRun(t, store, "run1", "task1")
	if _, _, err := store.PruneSessions(ctx, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"session_calls", "session_cost_breakdown", "delegations", "session_tool_events"} {
		if n := countTable(t, store, table); n != 0 {
			t.Errorf("%s keeps %d rows after the prune", table, n)
		}
	}
}

func TestDeleteByTask_TakesTheAttribution(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	seedAttributedRun(t, store, "run1", "task1")
	if _, err := store.DeleteByTask(ctx, "task1"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"session_calls", "session_cost_breakdown", "delegations"} {
		if n := countTable(t, store, table); n != 0 {
			t.Errorf("%s keeps %d rows after the task's delete", table, n)
		}
	}
}

func TestParseUsageStream_CountsEachCallOnce(t *testing.T) {
	lt := parseUsageStream(strings.NewReader(attributedRunLog), "")
	if lt.Turns != 3 || lt.CacheCreation != 13100 {
		t.Errorf("live totals = %+v", lt)
	}
}

func TestCloseStartedDelegations(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	for _, d := range []Delegation{
		{ID: "dlg_old", TaskID: "t", Agent: "a", Status: DelegationStarted, CreatedAt: now.Add(-time.Hour)},
		{ID: "dlg_new", TaskID: "t", Agent: "a", Status: DelegationStarted, CreatedAt: now},
		{ID: "dlg_done", TaskID: "t", Agent: "a", Status: DelegationOK, CreatedAt: now.Add(-time.Hour)},
	} {
		if err := store.AddDelegation(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.CloseStartedDelegations(ctx, now.Add(-15*time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("closed %d, err %v; want 1", n, err)
	}
	got, err := store.DelegationsByID(ctx, []string{"dlg_old", "dlg_new", "dlg_done"})
	if err != nil {
		t.Fatal(err)
	}
	if d := got["dlg_old"]; d.Status != DelegationFailed || d.ErrorCode != "tokenopt.errors.unfinished" {
		t.Errorf("old started delegation = %s %s", d.Status, d.ErrorCode)
	}
	if got["dlg_new"].Status != DelegationStarted || got["dlg_done"].Status != DelegationOK {
		t.Errorf("touched the others: new %s, done %s", got["dlg_new"].Status, got["dlg_done"].Status)
	}
	if ok, _ := store.FinishDelegation(ctx, Delegation{ID: "dlg_old", TaskID: "t", Agent: "a", Status: DelegationOK}); ok {
		t.Error("finished a closed delegation")
	}
}

func TestSaveAttribution_CountsUnnamedDelegations(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	seedAttributedRun(t, store, "run1", "task1")
	var started, finished time.Time
	if err := store.db.QueryRowContext(ctx, store.rb(`SELECT started_at, finished_at FROM sessions WHERE id = ?`), "run1").
		Scan(&started, &finished); err != nil {
		t.Fatal(err)
	}
	mid := started.Add(finished.Sub(started) / 2)
	for _, d := range []Delegation{
		{ID: "dlg_unnamed", TaskID: "task1", Agent: "dev", Stage: "in-progress", CostUSD: 0.05, Status: DelegationOK, CreatedAt: mid},
		{ID: "dlg_other_task", TaskID: "task2", Agent: "dev", Stage: "in-progress", CostUSD: 1, Status: DelegationOK, CreatedAt: mid},
		{ID: "dlg_later", TaskID: "task1", Agent: "dev", Stage: "in-progress", CostUSD: 1, Status: DelegationOK, CreatedAt: finished.Add(time.Hour)},
	} {
		if err := store.AddDelegation(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveAttribution(ctx, "run1", parseLog(t, attributedRunLog), ""); err != nil {
		t.Fatal(err)
	}
	b, err := store.Breakdown(ctx, "run1")
	if err != nil {
		t.Fatal(err)
	}
	var got float64
	for _, c := range b.Categories {
		if c.Category == CategoryDelegation {
			got = c.CostUSD
		}
	}
	if d := got - 0.054; d > 1e-9 || d < -1e-9 {
		t.Errorf("delegation category = %v, want 0.054", got)
	}
}
