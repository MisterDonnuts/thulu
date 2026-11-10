package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/usage"
)

func seedRun(t *testing.T, srv *Server, id, wid string) usage.Session {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, id+".log")
	if err := os.WriteFile(logPath, []byte("secret transcript for "+wid+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.md"), []byte("# private\n"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}

	now := time.Now().UTC().Add(-time.Hour)
	sess := usage.Session{
		ID:           id,
		Agent:        "tester-" + id,
		AgentType:    "tester",
		Engine:       "claude-code",
		Model:        "claude-opus-5",
		WorkspaceID:  wid,
		TaskID:       "task-of-" + id,
		StartedAt:    now.Add(-10 * time.Minute),
		FinishedAt:   now,
		DurationMs:   600000,
		Turns:        7,
		InputTokens:  1000,
		OutputTokens: 500,
		CostUSD:      1.25,
		StopReason:   "end_turn",
		LogsFile:     logPath,
		OutputDir:    outDir,
	}
	if err := srv.usageStore.Add(t.Context(), sess); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
	if err := srv.usageStore.AddToolEvents(t.Context(), []usage.ToolEvent{
		{SessionID: id, TS: now, TurnIndex: 1, ToolName: "Bash", ToolKind: "bash", InputBytes: 42},
	}); err != nil {
		t.Fatalf("seed tool events: %v", err)
	}
	return sess
}

func decodeList(t *testing.T, w *httptest.ResponseRecorder, what string) []map[string]any {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", what, w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode %s: %v", what, err)
	}
	return rows
}

func TestIntegration_WorkspaceIsolation_UsageSessions(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "usage-iso-a@test.com", "UsageIsoA")
	tokenB, _ := mintWorkspaceUser(t, srv, "usage-iso-b@test.com", "UsageIsoB")

	seedRun(t, srv, "run-of-a", wsA)

	rows := decodeList(t, getJSON(t, h, "/usage/sessions", tokenA), "A GET /usage/sessions")
	if len(rows) != 1 || rows[0]["id"] != "run-of-a" {
		t.Errorf("A's session list = %+v, want just run-of-a", rows)
	}
	rows = decodeList(t, getJSON(t, h, "/usage/sessions", tokenB), "B GET /usage/sessions")
	if len(rows) != 0 {
		t.Errorf("B's session list leaked %+v", rows)
	}

	for _, path := range []string{
		"/usage/by-engine", "/usage/by-model", "/usage/by-agent-type",
		"/usage/by-project", "/usage/by-stage", "/usage/by-service-tier",
		"/usage/by-tool", "/usage/top-tasks", "/usage/filtered-sessions",
	} {
		if got := decodeList(t, getJSON(t, h, path, tokenB), "B GET "+path); len(got) != 0 {
			t.Errorf("B GET %s leaked %+v", path, got)
		}
		if got := decodeList(t, getJSON(t, h, path, tokenA), "A GET "+path); len(got) == 0 {
			t.Errorf("A GET %s = empty, want their own run", path)
		}
	}

	assertUsageSessions(t, h, tokenA, 1)
	assertUsageSessions(t, h, tokenB, 0)

	var eff struct {
		Sessions int     `json:"sessions"`
		CostUSD  float64 `json:"cost_usd"`
	}
	ew := getJSON(t, h, "/usage/efficiency", tokenB)
	if err := json.NewDecoder(ew.Body).Decode(&eff); err != nil {
		t.Fatalf("decode efficiency: %v", err)
	}
	if eff.Sessions != 0 || eff.CostUSD != 0 {
		t.Errorf("B GET /usage/efficiency = %+v, want zeroes", eff)
	}
	for _, bucket := range []string{"day", "hour"} {
		var ts struct {
			Points []usage.TimePoint `json:"points"`
		}
		tw := getJSON(t, h, "/usage/timeseries?bucket="+bucket, tokenB)
		if err := json.NewDecoder(tw.Body).Decode(&ts); err != nil {
			t.Fatalf("decode timeseries: %v", err)
		}
		if len(ts.Points) != 0 {
			t.Errorf("B GET /usage/timeseries?bucket=%s leaked %+v", bucket, ts.Points)
		}
	}

	for _, path := range []string{
		"/usage/sessions/run-of-a/logs",
		"/usage/sessions/run-of-a/logs/stream",
		"/usage/sessions/run-of-a/files",
		"/usage/sessions/run-of-a/files/report.md",
		"/usage/sessions/run-of-a/tool-events",
	} {
		if w := getJSON(t, h, path, tokenB); w.Code != http.StatusNotFound {
			t.Errorf("B GET %s = %d, want 404: %.200s", path, w.Code, w.Body.String())
		}
	}

	for _, path := range []string{
		"/usage/sessions/run-of-a/logs",
		"/usage/sessions/run-of-a/files",
		"/usage/sessions/run-of-a/files/report.md",
		"/usage/sessions/run-of-a/tool-events",
	} {
		if w := getJSON(t, h, path, tokenA); w.Code != http.StatusOK {
			t.Errorf("A GET %s = %d, want 200: %.200s", path, w.Code, w.Body.String())
		}
	}
}

func assertUsageSessions(t *testing.T, h http.Handler, token string, want int) {
	t.Helper()
	var payload struct {
		Totals struct {
			Sessions int `json:"sessions"`
		} `json:"totals"`
		ByAgent []map[string]any `json:"by_agent"`
	}
	w := getJSON(t, h, "/usage", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /usage = %d: %s", w.Code, w.Body.String())
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /usage: %v", err)
	}
	if payload.Totals.Sessions != want {
		t.Errorf("GET /usage totals.sessions = %d, want %d", payload.Totals.Sessions, want)
	}
	if want == 0 && len(payload.ByAgent) != 0 {
		t.Errorf("GET /usage by_agent leaked %+v", payload.ByAgent)
	}
}

func TestIntegration_WorkspaceIsolation_UnattributedRunHiddenFromEveryone(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, _ := mintWorkspaceUser(t, srv, "usage-orphan-a@test.com", "UsageOrphanA")

	seedRun(t, srv, "run-unattributed", "")

	if rows := decodeList(t, getJSON(t, h, "/usage/sessions", tokenA), "A GET /usage/sessions"); len(rows) != 0 {
		t.Errorf("scoped caller saw an unattributed run: %+v", rows)
	}
	if w := getJSON(t, h, "/usage/sessions/run-unattributed/logs", tokenA); w.Code != http.StatusNotFound {
		t.Errorf("scoped caller read an unattributed run's logs: %d", w.Code)
	}

	rows := decodeList(t, getJSON(t, h, "/usage/sessions", ""), "unrestricted GET /usage/sessions")
	if len(rows) != 1 || rows[0]["id"] != "run-unattributed" {
		t.Errorf("unrestricted list = %+v, want the unattributed run", rows)
	}
}

func TestIntegration_UsageSessionStampedFromItsTask(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	tokenA, wsA := mintWorkspaceUser(t, srv, "usage-stamp-a@test.com", "UsageStampA")
	tokenB, _ := mintWorkspaceUser(t, srv, "usage-stamp-b@test.com", "UsageStampB")

	cw := postJSONAuth(t, h, "/tasks", tokenA, map[string]any{"title": "A's run"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("A POST /tasks = %d: %s", cw.Code, cw.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(cw.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID, _ := created["id"].(string)

	now := time.Now().UTC().Add(-time.Hour)
	if err := srv.usageStore.Add(t.Context(), usage.Session{
		ID: "run-from-task", Agent: "tester", AgentType: "tester",
		Engine: "claude-code", Model: "claude-opus-5", TaskID: taskID,
		StartedAt: now.Add(-time.Minute), FinishedAt: now,
		Turns: 2, InputTokens: 10, OutputTokens: 5, CostUSD: 0.02,
	}); err != nil {
		t.Fatalf("add session: %v", err)
	}

	got := srv.usageStore.Get(t.Context(), "run-from-task")
	if got == nil || got.WorkspaceID != wsA {
		t.Fatalf("session workspace = %v, want %q", got, wsA)
	}
	if rows := decodeList(t, getJSON(t, h, "/usage/sessions", tokenA), "A GET /usage/sessions"); len(rows) != 1 {
		t.Errorf("A's list = %+v, want their run", rows)
	}
	if rows := decodeList(t, getJSON(t, h, "/usage/sessions", tokenB), "B GET /usage/sessions"); len(rows) != 0 {
		t.Errorf("B's list leaked %+v", rows)
	}
}
