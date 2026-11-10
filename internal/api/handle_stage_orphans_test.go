package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func decodePrune(t *testing.T, w *httptest.ResponseRecorder) PruneStagesResponse {
	t.Helper()
	var resp PruneStagesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode prune response: %v (body: %s)", err, w.Body.String())
	}
	return resp
}

func setupOrphanedAgent(t *testing.T, srv *Server, name string) string {
	t.Helper()
	agentDir := filepath.Join(srv.agentsDir, "agents", name)
	if err := os.MkdirAll(filepath.Join(agentDir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(agentDir, "auditor"), 0o755); err != nil {
		t.Fatalf("mkdir auditor: %v", err)
	}
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "` + name + `"
      thulu.project: "keep-me"
      thulu.stages: "refinement,ghost_stage"
      thulu.turns: "refinement:5,ghost_stage:9"
      thulu.skills.default.ghost_stage: "one,two"
      thulu.mcp.default.ghost_stage: "thulu-github"
      thulu.auditor.stages: "refinement,ghost_stage"
      thulu.auditor.turns.ghost_stage: "3"
`
	if err := os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	for path, body := range map[string]string{
		"prompts/main.md":         "# fallback\n",
		"prompts/refinement.md":   "# real stage\n",
		"prompts/ghost_stage.md":  "# orphan\n",
		"auditor/default.md":      "# fallback\n",
		"auditor/ghost_stage.md":  "# orphan\n",
		"prompts/not-a-stage!.md": "# never touched\n",
	} {
		if err := os.WriteFile(filepath.Join(agentDir, path), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return name
}

func TestAgentSettingsReportUnknownStages(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	name := setupOrphanedAgent(t, srv, "orphaned")

	w := getJSON(t, h, "/types/"+name+"/settings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET settings = %d: %s", w.Code, w.Body.String())
	}
	var settings AgentSettings
	json.Unmarshal(w.Body.Bytes(), &settings)
	if len(settings.UnknownStages) != 1 || settings.UnknownStages[0] != "ghost_stage" {
		t.Errorf("unknown_stages = %v, want [ghost_stage]", settings.UnknownStages)
	}
}

func TestPruneAgentStagesDropsLabelsAndFiles(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	name := setupOrphanedAgent(t, srv, "orphaned")
	agentDir := filepath.Join(srv.agentsDir, "agents", name)

	w := postJSON(t, h, "/types/"+name+"/settings/prune-stages", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("prune = %d: %s", w.Code, w.Body.String())
	}
	resp := decodePrune(t, w)
	if len(resp.Stages) != 1 || resp.Stages[0] != "ghost_stage" {
		t.Errorf("pruned stages = %v, want [ghost_stage]", resp.Stages)
	}
	wantFiles := map[string]bool{"prompts/ghost_stage.md": true, "auditor/ghost_stage.md": true}
	if len(resp.Files) != len(wantFiles) {
		t.Errorf("pruned files = %v, want %v", resp.Files, wantFiles)
	}
	for _, f := range resp.Files {
		if !wantFiles[f] {
			t.Errorf("unexpected file pruned: %s", f)
		}
	}

	for _, keep := range []string{"prompts/main.md", "prompts/refinement.md",
		"auditor/default.md", "prompts/not-a-stage!.md"} {
		if _, err := os.Stat(filepath.Join(agentDir, keep)); err != nil {
			t.Errorf("%s should have survived: %v", keep, err)
		}
	}
	for _, gone := range []string{"prompts/ghost_stage.md", "auditor/ghost_stage.md"} {
		if _, err := os.Stat(filepath.Join(agentDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be gone", gone)
		}
	}

	w = getJSON(t, h, "/types/"+name+"/settings", "")
	var settings AgentSettings
	json.Unmarshal(w.Body.Bytes(), &settings)
	if len(settings.UnknownStages) != 0 {
		t.Errorf("unknown_stages after prune = %v", settings.UnknownStages)
	}
	if len(settings.ValidStages) != 1 || settings.ValidStages[0] != "refinement" {
		t.Errorf("valid_stages = %v, want [refinement]", settings.ValidStages)
	}
	if settings.TurnsByStage["refinement"] != "5" {
		t.Errorf("real stage config lost: %v", settings.TurnsByStage)
	}
	for _, m := range []map[string]string{settings.TurnsByStage, settings.AuditorTurnsByStage} {
		if _, ok := m["ghost_stage"]; ok {
			t.Errorf("ghost stage survived in a per-stage map: %v", m)
		}
	}
	if _, ok := settings.SkillsByStage["ghost_stage"]; ok {
		t.Error("ghost stage survived in skills_by_stage")
	}
	if _, ok := settings.MCPByStage["ghost_stage"]; ok {
		t.Error("ghost stage survived in mcp_by_stage")
	}
	compose, err := os.ReadFile(filepath.Join(agentDir, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if !strings.Contains(string(compose), "keep-me") {
		t.Errorf("unmanaged label dropped by the rewrite: %s", compose)
	}
}

func TestPruneAgentStagesIsANoOpWhenClean(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	name := setupAgent(t, srv, "clean-agent")

	w := postJSON(t, h, "/types/"+name+"/settings/prune-stages", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("prune = %d: %s", w.Code, w.Body.String())
	}
	resp := decodePrune(t, w)
	if len(resp.Stages) != 0 || len(resp.Files) != 0 {
		t.Errorf("clean agent pruned something: %+v", resp)
	}
}

func TestProjectStageFilesReportAndPruneOrphans(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	w := postJSON(t, h, "/projects?workspace=ws-test", map[string]any{
		"name": "Orphan Proj", "repo_url": testgit.BareRepo(t),
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", w.Code, w.Body.String())
	}
	var proj map[string]any
	json.Unmarshal(w.Body.Bytes(), &proj)
	projID, _ := proj["id"].(string)

	if w := putRequest(t, h, "/projects/"+projID+"/stage-files/hooks/testing", "",
		[]byte("#!/bin/sh\necho real\n")); w.Code != http.StatusOK {
		t.Fatalf("PUT hooks/testing = %d: %s", w.Code, w.Body.String())
	}
	stored, err := srv.projectStore.Get(t.Context(), projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	stageDir := srv.projectStore.StageDir(stored)
	for path, body := range map[string]string{
		"prompts/ghost_stage.md": "# orphan prompt\n",
		"hooks/ghost_stage.sh":   "#!/bin/sh\necho orphan\n",
	} {
		if err := os.WriteFile(filepath.Join(stageDir, path), []byte(body), 0o750); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	w = getJSON(t, h, "/projects/"+projID+"/stage-files", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET stage-files = %d: %s", w.Code, w.Body.String())
	}
	var listing ProjectStageFilesResponse
	json.Unmarshal(w.Body.Bytes(), &listing)
	if len(listing.Orphans) != 2 {
		t.Errorf("orphans = %v, want 2 entries", listing.Orphans)
	}

	w = postJSON(t, h, "/projects/"+projID+"/stage-files/prune", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("prune = %d: %s", w.Code, w.Body.String())
	}
	resp := decodePrune(t, w)
	if len(resp.Files) != 2 {
		t.Errorf("pruned files = %v, want 2", resp.Files)
	}
	if len(resp.Stages) != 1 || resp.Stages[0] != "ghost_stage" {
		t.Errorf("pruned stages = %v, want [ghost_stage]", resp.Stages)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "hooks", "testing.sh")); err != nil {
		t.Errorf("real stage hook should have survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "hooks", "ghost_stage.sh")); !os.IsNotExist(err) {
		t.Error("orphan hook should be gone")
	}

	w = getJSON(t, h, "/projects/"+projID+"/stage-files", "")
	listing = ProjectStageFilesResponse{}
	json.Unmarshal(w.Body.Bytes(), &listing)
	if len(listing.Orphans) != 0 {
		t.Errorf("orphans after prune = %v", listing.Orphans)
	}
}
