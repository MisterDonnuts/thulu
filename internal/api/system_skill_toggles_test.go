package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/auth"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func putTestSystemSkills(t *testing.T, store *agentstore.Store) {
	t.Helper()
	ctx := context.Background()
	for name, yaml := range map[string]string{
		"octopus-run": "name: octopus-run\ndescription: how runs work\nenforcement: always\nversion: 1\n",
		"octopus-gh":  "name: octopus-gh\ndescription: github rules\nenforcement: on_demand\nversion: 1\nmcp: thulu-github\n",
	} {
		if _, err := store.PutSystemSkill(ctx, name, map[string][]byte{
			"prompt.md":  []byte(name + " text"),
			"skill.yaml": []byte(yaml),
		}, "test"); err != nil {
			t.Fatalf("put system skill %s: %v", name, err)
		}
	}
}

func statusOf(t *testing.T, s AgentSettings, stage, name string) SystemSkillStatus {
	t.Helper()
	for _, row := range s.SystemSkillsByStage[stage] {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no %s row for stage %s in %+v", name, stage, s.SystemSkillsByStage)
	return SystemSkillStatus{}
}

func TestAgentSettingsSystemSkillTogglesRoundTrip(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	putTestSystemSkills(t, store)
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards()}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")
	const ws = "ws-toggles"

	do := func(h func(http.ResponseWriter, *http.Request), method, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := tenantRequestBody(method, "/types/developer/settings", ws, body)
		r.SetPathValue("name", "developer")
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	read := func() AgentSettings {
		t.Helper()
		w := do(srv.handleGetAgentSettings, http.MethodGet, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET settings = %d: %s", w.Code, w.Body.String())
		}
		var s AgentSettings
		if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
			t.Fatalf("decode settings: %v (%s)", err, w.Body.String())
		}
		return s
	}
	save := func(s AgentSettings) AgentSettings {
		t.Helper()
		body, _ := json.Marshal(s)
		w := do(srv.handlePutAgentSettings, http.MethodPut, string(body))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT settings = %d: %s", w.Code, w.Body.String())
		}
		var out AgentSettings
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode save response: %v (%s)", err, w.Body.String())
		}
		return out
	}

	created := httptest.NewRecorder()
	srv.handleCreateAgent(created, tenantRequestBody(http.MethodPost, "/types", ws, `{"name":"developer"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create agent: %d %s", created.Code, created.Body.String())
	}
	initial := AgentSettings{
		Name:        "developer",
		ValidStages: []string{"in_progress", "review"},
		MCPByStage:  map[string][]string{"in_progress": {"thulu-github@write"}},
	}
	save(initial)

	s := read()
	if len(s.SystemSkills) != 2 {
		t.Fatalf("system_skills = %+v, want the two the install has", s.SystemSkills)
	}
	if run := statusOf(t, s, "review", "octopus-run"); !run.Wired || !run.Enabled || run.MCP != "" || run.Enforcement != "always" {
		t.Fatalf("octopus-run on review = %+v, want wired, enabled, every run, always", run)
	}
	if gh := statusOf(t, s, "in_progress", "octopus-gh"); !gh.Wired || !gh.Enabled || gh.Description != "github rules" {
		t.Fatalf("octopus-gh on in_progress = %+v", gh)
	}
	if gh := statusOf(t, s, "review", "octopus-gh"); gh.Wired || !gh.Enabled {
		t.Fatalf("octopus-gh on review = %+v, want enabled but not wired (no thulu-github there)", gh)
	}

	s.SystemSkillsOff = map[string][]string{"review": {"octopus-run", "octopus-gone", "octopus-run"}}
	saved := save(s)
	if got := saved.SystemSkillsOff["review"]; !slices.Equal(got, []string{"octopus-gone", "octopus-run"}) {
		t.Fatalf("saved opt-outs = %v, want them deduped and sorted, unknown kept", got)
	}
	s = read()
	if run := statusOf(t, s, "review", "octopus-run"); run.Enabled {
		t.Fatalf("octopus-run on review still enabled: %+v", run)
	}
	if run := statusOf(t, s, "in_progress", "octopus-run"); !run.Enabled {
		t.Fatalf("the opt-out leaked to in_progress: %+v", run)
	}

	at := types.FindAgent(srv.agents(ws), "developer")
	if at == nil {
		t.Fatal("the workspace's runs do not resolve the agent")
	}
	if got := at.StageSkills("review"); slices.Contains(got, "octopus-run") {
		t.Fatalf("review runs still get octopus-run: %v", got)
	}
	if got := at.StageSkills("in_progress"); !slices.Contains(got, "octopus-run") || !slices.Contains(got, "octopus-gh") {
		t.Fatalf("in_progress lost a system skill nobody turned off: %v", got)
	}

	s.SystemSkillsOff = nil
	save(s)
	if run := statusOf(t, read(), "review", "octopus-run"); run.Enabled {
		t.Fatal("a save without system_skills_off turned octopus-run back on")
	}

	s.SystemSkillsOff = map[string][]string{}
	save(s)
	if run := statusOf(t, read(), "review", "octopus-run"); !run.Enabled {
		t.Fatal("saving {} did not clear the opt-outs")
	}
}

func TestBundleLeavesOutATurnedOffSystemSkill(t *testing.T) {
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	putTestSystemSkills(t, store)
	if _, err := store.PutFile(ctx, bundleWorkspace, agentstore.KindAgent, "developer",
		"prompts/main.md", []byte("you are the developer"), "test"); err != nil {
		t.Fatalf("put prompt: %v", err)
	}
	srv := &Server{}
	srv.SetAgentStore(store)
	at := types.AgentType{
		Name:            "developer",
		ValidStages:     []types.Stage{"in_progress"},
		MCPByStage:      map[string][]string{"in_progress": {"thulu-github@write"}},
		SystemSkills:    []types.SystemSkillRef{{Name: "octopus-run"}, {Name: "octopus-gh", MCP: "thulu-github"}},
		SystemSkillsOff: map[string][]string{"in_progress": {"octopus-gh"}},
	}
	var buf bytes.Buffer
	if err := writeRunBundle(ctx, &buf, srv.content(ctx, bundleWorkspace), at, "in_progress"); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	got := entries(t, buf.Bytes())
	if _, ok := got["skills/octopus-gh/prompt.md"]; ok {
		t.Fatal("a turned-off system skill travelled in the bundle")
	}
	if _, ok := got["skills/octopus-run/prompt.md"]; !ok {
		t.Fatalf("octopus-run, still on, is missing; got %v", keysOf(got))
	}
}

func TestAgentSettingsWriteNeedsAnAdmin(t *testing.T) {
	srv, h := newIntegrationServer(t)
	viewer, _ := mintWorkspaceUserAs(t, srv, "sst-viewer@test.com", "SSTV", auth.RoleViewer)
	admin, _ := mintWorkspaceUserAs(t, srv, "sst-admin@test.com", "SSTA", auth.RoleAdmin)
	body := AgentSettings{
		Name:            "developer",
		ValidStages:     []string{"in_progress"},
		SystemSkillsOff: map[string][]string{"in_progress": {"octopus-run"}},
	}
	if w := putJSON(t, h, "/types/developer/settings", body, viewer); w.Code != http.StatusForbidden {
		t.Fatalf("viewer PUT settings = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := putJSON(t, h, "/types/developer/settings", body, admin); w.Code == http.StatusForbidden {
		t.Fatalf("admin PUT settings = 403: %s", w.Body.String())
	}
}

func TestNormalizeSystemSkillsOff(t *testing.T) {
	got := normalizeSystemSkillsOff(map[string][]string{
		"review":   {" octopus-run ", "", "octopus-gh", "octopus-run"},
		"Bad-Id":   {"octopus-run"},
		"testing":  {"", " "},
		"chat":     nil,
		"in_progr": {"octopus-gh"},
	})
	want := map[string][]string{"review": {"octopus-gh", "octopus-run"}, "in_progr": {"octopus-gh"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for stage, names := range want {
		if !slices.Equal(got[stage], names) {
			t.Fatalf("%s = %v, want %v", stage, got[stage], names)
		}
	}
	if normalizeSystemSkillsOff(map[string][]string{"review": {}}) != nil {
		t.Fatal("nothing left must be nil, so the store keeps no empty entries")
	}
}
