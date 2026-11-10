package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/usage"
)

func seedSessions(t *testing.T, srv *Server, wid string) {
	t.Helper()
	now := time.Now().UTC().Add(-1 * time.Hour)
	mk := func(id, engine, model, agentType, projectID, taskID string, cost float64) usage.Session {
		return usage.Session{
			ID:           id,
			WorkspaceID:  wid,
			Agent:        "a-" + id,
			AgentType:    agentType,
			Engine:       engine,
			Model:        model,
			ProjectID:    projectID,
			TaskID:       taskID,
			Stage:        "build",
			StartedAt:    now.Add(-10 * time.Minute),
			FinishedAt:   now,
			DurationMs:   500_000,
			Turns:        4,
			InputTokens:  1000,
			OutputTokens: 500,
			CacheRead:    200,
			CostUSD:      cost,
			StopReason:   "end_turn",
		}
	}
	rows := []usage.Session{
		mk("s1", "claude-code", "sonnet", "developer", "proj-a", "task-1", 0.50),
		mk("s2", "claude-code", "opus", "developer", "proj-a", "task-1", 1.20),
		mk("s3", "opencode", "sonnet", "reviewer", "proj-b", "task-2", 0.30),
	}
	for _, s := range rows {
		if err := srv.usageStore.Add(t.Context(), s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := srv.usageStore.AddToolEvents(t.Context(), []usage.ToolEvent{
		{SessionID: "s2", TS: now, TurnIndex: 1, ToolName: "Skill", ToolKind: "skill", SkillName: "frontend-design", InputBytes: 100},
		{SessionID: "s2", TS: now, TurnIndex: 2, ToolName: "Bash", ToolKind: "bash", InputBytes: 80},
	}); err != nil {
		t.Fatalf("seed tool events: %v", err)
	}
}

func TestIntegration_UsageTimeseries(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)

	w := getJSON(t, h, "/usage/timeseries?bucket=day", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/timeseries = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Bucket string            `json:"bucket"`
		Points []usage.TimePoint `json:"points"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Bucket != "day" {
		t.Errorf("bucket = %q, want day", resp.Bucket)
	}
	if len(resp.Points) == 0 {
		t.Errorf("expected at least one timeseries point, got %+v", resp)
	}
}

func TestIntegration_UsageByEngine(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)

	w := getJSON(t, h, "/usage/by-engine", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/by-engine = %d: %s", w.Code, w.Body.String())
	}
	var rows []usage.DimensionRow
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys := map[string]bool{}
	for _, r := range rows {
		keys[r.Key] = true
	}
	if !keys["claude-code"] || !keys["opencode"] {
		t.Errorf("expected both engines, got %+v", rows)
	}
}

func TestIntegration_UsageByModel_RespectsEngineFilter(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)

	w := getJSON(t, h, "/usage/by-model?engine=opencode", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/by-model = %d: %s", w.Code, w.Body.String())
	}
	var rows []usage.DimensionRow
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "sonnet" {
		t.Fatalf("engine filter not applied: %+v", rows)
	}
}

func TestIntegration_UsageBySkill_ReturnsSkillRows(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)

	w := getJSON(t, h, "/usage/by-skill", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/by-skill = %d: %s", w.Code, w.Body.String())
	}
	var rows []usage.SkillRow
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].Skill != "frontend-design" {
		t.Fatalf("expected one skill row for frontend-design, got %+v", rows)
	}
}

func TestIntegration_UsageEfficiencyAndBurnRate(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)

	wEff := getJSON(t, h, "/usage/efficiency", token)
	if wEff.Code != http.StatusOK {
		t.Fatalf("GET /usage/efficiency = %d: %s", wEff.Code, wEff.Body.String())
	}
	var eff usage.Efficiency
	if err := json.NewDecoder(wEff.Body).Decode(&eff); err != nil {
		t.Fatalf("decode efficiency: %v", err)
	}
	if eff.Sessions != 3 {
		t.Errorf("sessions = %d, want 3", eff.Sessions)
	}
	if eff.CacheHitRatio <= 0 {
		t.Errorf("cache_hit_ratio = %.4f, want > 0", eff.CacheHitRatio)
	}

	wBR := getJSON(t, h, "/usage/burn-rate", token)
	if wBR.Code != http.StatusOK {
		t.Fatalf("GET /usage/burn-rate = %d: %s", wBR.Code, wBR.Body.String())
	}
	var br usage.BurnRate
	if err := json.NewDecoder(wBR.Body).Decode(&br); err != nil {
		t.Fatalf("decode burn-rate: %v", err)
	}
	if br.WindowHours != 24 {
		t.Errorf("window_hours = %d, want 24 (default)", br.WindowHours)
	}
}

func TestIntegration_UsageTopTasks(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)

	w := getJSON(t, h, "/usage/top-tasks?limit=10", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/top-tasks = %d: %s", w.Code, w.Body.String())
	}
	var rows []usage.TaskRow
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("want 2 tasks (task-1, task-2), got %d: %+v", len(rows), rows)
	}
	if rows[0].TaskID != "task-1" {
		t.Errorf("expected task-1 first (most expensive), got %+v", rows[0])
	}
}

func TestIntegration_UsageTopTasks_Titles(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)
	taskID := createTaskViaAPI(t, h, token, "Checkout totals")
	now := time.Now().UTC().Add(-time.Hour)
	if err := srv.usageStore.Add(t.Context(), usage.Session{
		ID: "s-titled", WorkspaceID: wid, Agent: "dev", AgentType: "developer",
		TaskID: taskID, StartedAt: now.Add(-time.Minute), FinishedAt: now, CostUSD: 9,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := getJSON(t, h, "/usage/top-tasks?limit=10", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/top-tasks = %d: %s", w.Code, w.Body.String())
	}
	var rows []usage.TaskRow
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 3 || rows[0].TaskID != taskID || rows[0].Title != "Checkout totals" {
		t.Fatalf("want the titled task first with its title, got %+v", rows)
	}
	if rows[1].Title != "" {
		t.Errorf("a session whose task does not exist has no title, got %q", rows[1].Title)
	}
}

func TestIntegration_UsagePricing_Seeded(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "u@example.com", "U")
	w := getJSON(t, h, "/usage/pricing", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/pricing = %d: %s", w.Code, w.Body.String())
	}
	var rows []usage.Pricing
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) < 3 {
		t.Errorf("expected seeded pricing rows, got %d", len(rows))
	}
}

func TestIntegration_UsageFilteredSessions_LimitClamped(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, wid := mintWorkspaceUser(t, srv, "u@example.com", "U")
	seedSessions(t, srv, wid)

	w := getJSON(t, h, "/usage/filtered-sessions?limit=2&engine=claude-code", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage/filtered-sessions = %d: %s", w.Code, w.Body.String())
	}
	var rows []usage.Session
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("limit not honoured: got %d rows", len(rows))
	}
	for _, r := range rows {
		if r.Engine != "claude-code" {
			t.Errorf("engine filter not applied: %+v", r)
		}
	}
}

func TestParseFilterTimeLayouts(t *testing.T) {
	want := time.Date(2026, 7, 30, 17, 46, 0, 0, time.UTC)

	tests := []struct {
		name, query string
		want        time.Time
	}{
		{"rfc3339", "2026-07-30T17:46:00Z", want},
		{"no offset, with seconds", "2026-07-30T17:46:00", want},
		{"no offset, no seconds (datetime-local)", "2026-07-30T17:46", want},
		{"date only", "2026-07-30", time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)},
		{"garbage yields zero", "not-a-time", time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodGet, "/usage/timeseries?from="+tc.query, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			got := (&Server{}).parseFilter(r).From
			if !got.Equal(tc.want) {
				t.Errorf("parseFilter(from=%q).From = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}
