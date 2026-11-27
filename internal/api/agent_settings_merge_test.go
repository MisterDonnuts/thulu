package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

// A PUT that names only mcp_by_stage once reset the refiner's turns from 80 to the default 20 and
// dropped its skills and auditor. A key the body leaves out keeps its stored value; a key it sends
// empty still clears it.
func TestPutAgentSettingsKeepsWhatTheBodyLeavesOut(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards()}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")
	const ws = "ws-settings-merge"

	created := httptest.NewRecorder()
	srv.handleCreateAgent(created, tenantRequestBody(http.MethodPost, "/types", ws, `{"name":"refiner"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", created.Code, created.Body.String())
	}
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := tenantRequestBody(http.MethodPut, "/types/refiner/settings", ws, body)
		r.SetPathValue("name", "refiner")
		w := httptest.NewRecorder()
		srv.handlePutAgentSettings(w, r)
		return w
	}
	get := func() AgentSettings {
		t.Helper()
		r := tenantRequestBody(http.MethodGet, "/types/refiner/settings", ws, "")
		r.SetPathValue("name", "refiner")
		w := httptest.NewRecorder()
		srv.handleGetAgentSettings(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET = %d: %s", w.Code, w.Body)
		}
		var out AgentSettings
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	full := `{"name":"refiner","description":"Refines tickets","effort":"high",
		"valid_stages":["in_progress"],
		"turns_by_stage":{"in_progress":"80"},
		"skills_by_stage":{"in_progress":["brief"]},
		"auditor_stages":["in_progress"],
		"auditor_turns_by_stage":{"in_progress":"30"}}`
	if w := put(full); w.Code != http.StatusOK {
		t.Fatalf("full PUT = %d: %s", w.Code, w.Body)
	}

	if w := put(`{"mcp_by_stage":{"in_progress":["octopus-api@write"]}}`); w.Code != http.StatusOK {
		t.Fatalf("partial PUT = %d: %s", w.Code, w.Body)
	}
	got := get()
	if got.TurnsByStage["in_progress"] != "80" {
		t.Errorf("turns = %v, want 80 kept", got.TurnsByStage)
	}
	if !slices.Equal(got.SkillsByStage["in_progress"], []string{"brief"}) {
		t.Errorf("skills = %v, want brief kept", got.SkillsByStage)
	}
	if !slices.Equal(got.AuditorStages, []string{"in_progress"}) || got.AuditorTurnsByStage["in_progress"] != "30" {
		t.Errorf("auditor = %v %v, want it kept", got.AuditorStages, got.AuditorTurnsByStage)
	}
	if got.Description != "Refines tickets" || got.Effort != "high" || !slices.Equal(got.ValidStages, []string{"in_progress"}) {
		t.Errorf("description/effort/stages = %q %q %v, want them kept", got.Description, got.Effort, got.ValidStages)
	}
	if !slices.Equal(got.MCPByStage["in_progress"], []string{"octopus-api@write"}) {
		t.Errorf("mcp = %v, want the new value", got.MCPByStage)
	}

	if w := put(`{"turns_by_stage":{},"skills_by_stage":null,"auditor_stages":[],"description":""}`); w.Code != http.StatusOK {
		t.Fatalf("clearing PUT = %d: %s", w.Code, w.Body)
	}
	got = get()
	if len(got.TurnsByStage) != 0 || len(got.SkillsByStage) != 0 || len(got.AuditorStages) != 0 || got.Description != "" {
		t.Errorf("after clearing: turns %v skills %v auditor %v description %q, want all empty",
			got.TurnsByStage, got.SkillsByStage, got.AuditorStages, got.Description)
	}
	if !slices.Equal(got.MCPByStage["in_progress"], []string{"octopus-api@write"}) || got.Effort != "high" {
		t.Errorf("a clearing PUT touched keys it did not name: mcp %v effort %q", got.MCPByStage, got.Effort)
	}

	for name, body := range map[string]string{
		"array":      `[]`,
		"wrong type": `{"turns_by_stage":"80"}`,
	} {
		if w := put(body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d, want 400", name, w.Code)
		}
	}

	r := tenantRequestBody(http.MethodPut, "/types/nobody/settings", ws, `{"effort":"low"}`)
	r.SetPathValue("name", "nobody")
	w := httptest.NewRecorder()
	srv.handlePutAgentSettings(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("PUT on an unknown agent = %d, want 404", w.Code)
	}
}

func TestMergeSettingsBodyReplacesNamedKeysWhole(t *testing.T) {
	stored := AgentSettings{
		Name:         "dev",
		TurnsByStage: map[string]string{"a": "10", "b": "20"},
		MCPByStage:   map[string][]string{"a": {"x"}},
	}
	out, err := mergeSettingsBody(stored, map[string]json.RawMessage{
		"turns_by_stage": json.RawMessage(`{"a":"40"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.TurnsByStage) != 1 || out.TurnsByStage["a"] != "40" {
		t.Errorf("turns = %v, want the sent map alone (no merge inside a key)", out.TurnsByStage)
	}
	if !slices.Equal(out.MCPByStage["a"], []string{"x"}) {
		t.Errorf("mcp = %v, want it kept", out.MCPByStage)
	}
	if out.TokenOptimizer != nil || out.SystemSkillsOff != nil {
		t.Errorf("token optimizer %v / system skills off %v, want nil (keep) when the body leaves them out",
			out.TokenOptimizer, out.SystemSkillsOff)
	}
}

// The compose path (no agent store) merges the same way.
func TestIntegration_AgentSettings_PartialPutKeepsComposeLabels(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token := registerCatalogOperator(t, h, "agsmerge@test.com", "AM", "SecurePassword1")
	setupAgent(t, srv, "probe-merge")

	full := map[string]any{
		"name": "probe-merge", "valid_stages": []string{"refinement", "done"},
		"turns_by_stage":  map[string]string{"refinement": "80"},
		"skills_by_stage": map[string][]string{"refinement": {"brief"}},
		"auditor_stages":  []string{"refinement"},
	}
	if w := putJSON(t, h, "/types/probe-merge/settings", full, token); w.Code != http.StatusOK {
		t.Fatalf("full PUT = %d: %s", w.Code, w.Body)
	}
	partial := map[string]any{"mcp_by_stage": map[string][]string{"refinement": {"octopus-api"}}}
	if w := putJSON(t, h, "/types/probe-merge/settings", partial, token); w.Code != http.StatusOK {
		t.Fatalf("partial PUT = %d: %s", w.Code, w.Body)
	}
	w := getJSON(t, h, "/types/probe-merge/settings", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, w.Body)
	}
	var got AgentSettings
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TurnsByStage["refinement"] != "80" || !slices.Equal(got.SkillsByStage["refinement"], []string{"brief"}) ||
		!slices.Equal(got.AuditorStages, []string{"refinement"}) {
		t.Errorf("partial PUT dropped settings: turns %v skills %v auditor %v",
			got.TurnsByStage, got.SkillsByStage, got.AuditorStages)
	}
	if !slices.Equal(got.MCPByStage["refinement"], []string{"octopus-api"}) {
		t.Errorf("mcp = %v, want the new value", got.MCPByStage)
	}
}
