package types

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTurnsLabel(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  map[string]string
	}{
		{"empty", "", nil},
		{"single", "in_progress:5", map[string]string{"in_progress": "5"}},
		{"multi", "refinement:8,in_progress:5,ai_review:3",
			map[string]string{"refinement": "8", "in_progress": "5", "ai_review": "3"}},
		{"whitespace", " refinement : 8 , in_progress : 5 ",
			map[string]string{"refinement": "8", "in_progress": "5"}},
		{"keeps unknown but well-formed stages", "in_progress:5,bogus:9",
			map[string]string{"in_progress": "5", "bogus": "9"}},
		{"drops malformed stage ids", "in_progress:5,Bogus-Id:9",
			map[string]string{"in_progress": "5"}},
		{"drops malformed pair", "in_progress:5,nope",
			map[string]string{"in_progress": "5"}},
		{"empty turns dropped", "in_progress:5,refinement:",
			map[string]string{"in_progress": "5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTurnsLabel(tt.label)
			if len(got) != len(tt.want) {
				t.Errorf("parseTurnsLabel(%q) length = %d, want %d (got %v)", tt.label, len(got), len(tt.want), got)
				return
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseTurnsLabel(%q)[%q] = %q, want %q", tt.label, k, got[k], v)
				}
			}
		})
	}
}

func TestTurnsForStage(t *testing.T) {
	at := AgentType{
		TurnsByStage: map[string]string{
			"refinement":  "8",
			"in_progress": "5",
		},
	}

	if got := at.TurnsForStage("refinement"); got != "8" {
		t.Errorf("TurnsForStage(refinement) = %q, want %q", got, "8")
	}
	if got := at.TurnsForStage("in_progress"); got != "5" {
		t.Errorf("TurnsForStage(in_progress) = %q, want %q", got, "5")
	}
	if got := at.TurnsForStage("ai_review"); got != defaultStageTurns {
		t.Errorf("TurnsForStage(ai_review unset) = %q, want default %q", got, defaultStageTurns)
	}

	empty := AgentType{}
	if got := empty.TurnsForStage("refinement"); got != defaultStageTurns {
		t.Errorf("TurnsForStage on empty agent = %q, want default %q", got, defaultStageTurns)
	}
}

func TestStageChatVirtual(t *testing.T) {
	if !ValidStageID(string(StageChat)) {
		t.Error("StageChat must be a well-formed stage id (thulu.stages parsing depends on it)")
	}

	at := AgentType{ValidStages: []Stage{StageChat}}
	if !at.SupportsChat() {
		t.Error("SupportsChat should be true when chat is declared")
	}
	if (&AgentType{ValidStages: []Stage{StageInProgress}}).SupportsChat() {
		t.Error("SupportsChat should be false without the chat stage")
	}
}

func TestDiscoverRealManagerAgent(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "..", "agents", "manager", "docker-compose.yml")); err != nil {
		t.Skip("agents/manager is not present in this checkout (gitignored local agent)")
	}
	discovered, err := Discover(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("Discover repo agents: %v", err)
	}
	mgr := findByType(discovered, "manager")
	if mgr == nil {
		t.Fatal("manager agent not discovered")
	}
	if !mgr.SupportsChat() {
		t.Fatalf("manager must declare the chat stage, got %v", mgr.ValidStages)
	}
	if got := mgr.DefaultMCP(string(StageChat)); got != "octopus-api@write" {
		t.Errorf("manager chat MCP = %q, want octopus-api@write", got)
	}
	if got := mgr.TurnsForStage(string(StageChat)); got != "30" {
		t.Errorf("manager chat turns = %q, want 30", got)
	}
	if _, _, err := ParseComposeSpec(mgr.Dir); err != nil {
		t.Errorf("manager compose must stay launchable: %v", err)
	}
}

func findByType(agents []AgentType, typeName string) *AgentType {
	for i := range agents {
		if agents[i].Type == typeName {
			return &agents[i]
		}
	}
	return nil
}

func TestDiscoverMissingDirectoryIsEmpty(t *testing.T) {
	got, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover on a base with no agents/: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Discover = %#v, want an empty, non-nil list", got)
	}
	if got, err := NewCatalog(t.TempDir()).All(); err != nil || len(got) != 0 {
		t.Fatalf("Catalog.All = %v, %v; want no agents and no error", got, err)
	}
}

func TestDiscover(t *testing.T) {
	baseDir := t.TempDir()
	agentsDir := filepath.Join(baseDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	agentDir := filepath.Join(agentsDir, "test-agent")
	os.MkdirAll(agentDir, 0755)
	compose := `services:
  agent:
    build: .
    environment:
      - AGENT_NAME=${AGENT_NAME:-test}
      - MAX_TURNS=${MAX_TURNS:-20}
      - MODEL=${MODEL:-sonnet}
    labels:
      thulu.agent: "true"
      thulu.type: "test"
      thulu.stages: "refinement,in_progress,ai_review"
      thulu.turns: "refinement:8,in_progress:5,ai_review:3"
      thulu.skills.default.refinement: "copy-analysis"
      thulu.mcp.default.in_progress: "thulu-browser"
`
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644)

	os.MkdirAll(filepath.Join(agentsDir, "not-an-agent"), 0755)

	noLabelDir := filepath.Join(agentsDir, "no-label")
	os.MkdirAll(noLabelDir, 0755)
	os.WriteFile(filepath.Join(noLabelDir, "docker-compose.yml"), []byte(`services:
  app:
    build: .
    labels:
      foo: "bar"
`), 0644)

	discovered, err := Discover(baseDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("expected 1 agent type, got %d", len(discovered))
	}

	at := discovered[0]
	if at.Name != "test-agent" {
		t.Errorf("name = %q, want %q", at.Name, "test-agent")
	}
	if at.Type != "test" {
		t.Errorf("type = %q, want %q", at.Type, "test")
	}
	if len(at.ValidStages) != 3 {
		t.Fatalf("expected 3 valid stages, got %d (%v)", len(at.ValidStages), at.ValidStages)
	}
	if at.TurnsForStage("refinement") != "8" {
		t.Errorf("TurnsForStage(refinement) = %q, want %q", at.TurnsForStage("refinement"), "8")
	}
	if at.TurnsForStage("in_progress") != "5" {
		t.Errorf("TurnsForStage(in_progress) = %q, want %q", at.TurnsForStage("in_progress"), "5")
	}
	if at.SkillsByStage["refinement"][0] != "copy-analysis" {
		t.Errorf("SkillsByStage[refinement] = %v, want [copy-analysis]", at.SkillsByStage["refinement"])
	}
	if at.MCPByStage["in_progress"][0] != "thulu-browser" {
		t.Errorf("MCPByStage[in_progress] = %v, want [thulu-browser]", at.MCPByStage["in_progress"])
	}

	hasModel, hasMaxTurns, hasPhase := false, false, false
	for _, p := range at.Params {
		switch p.Name {
		case "MODEL":
			hasModel = true
			if p.Type != "select" {
				t.Errorf("MODEL type = %q, want 'select'", p.Type)
			}
		case "MAX_TURNS":
			hasMaxTurns = true
		case "PHASE":
			hasPhase = true
		}
	}
	if !hasModel {
		t.Error("MODEL param not found")
	}
	if !hasMaxTurns {
		t.Error("MAX_TURNS param not found")
	}
	if hasPhase {
		t.Error("PHASE param should be gone — phases are deprecated")
	}
}

func TestDiscoverEmptyDir(t *testing.T) {
	baseDir := t.TempDir()
	agentsDir := filepath.Join(baseDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	discovered, err := Discover(baseDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 0 {
		t.Fatalf("expected 0 agents in empty dir, got %d", len(discovered))
	}
}

func TestValidStageID(t *testing.T) {
	valid := []string{"backlog", "in_progress", "ai_review", "x", "stage_2"}
	for _, s := range valid {
		if !ValidStageID(s) {
			t.Errorf("ValidStageID(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"", "In_Progress", "with-hyphen", "with space", "path/../up", "über",
		"way_too_long_stage_id_that_exceeds_the_thirty_two_char_limit",
	}
	for _, s := range invalid {
		if ValidStageID(s) {
			t.Errorf("ValidStageID(%q) = true, want false", s)
		}
	}
}

func TestAuditorSpec_Nil(t *testing.T) {
	var a *AuditorSpec
	if a.RunsForStage("in_progress") {
		t.Error("nil AuditorSpec.RunsForStage should return false")
	}
	if a.TurnsForStage("in_progress") != "" {
		t.Error("nil AuditorSpec.TurnsForStage should return empty")
	}
	if a.ModelForStage("in_progress") != "" {
		t.Error("nil AuditorSpec.ModelForStage should return empty")
	}
	if a.SkillsForStage("in_progress") != "" {
		t.Error("nil AuditorSpec.SkillsForStage should return empty")
	}
}

func TestAuditorEngineByStage(t *testing.T) {
	var none *AuditorSpec
	if got := none.EngineForStage("done"); got != "" {
		t.Errorf("nil spec: %q", got)
	}
	spec := parseAuditorLabels(map[string]string{
		LabelAuditorStages:                "done",
		LabelAuditorEnginePrefix + "done": "codex",
	}, []Stage{"done"})
	if spec.EngineForStage("done") != "codex" || spec.EngineForStage("refinement") != "" {
		t.Errorf("engine_by_stage = %v", spec.EngineByStage)
	}
}

func TestAuditorSpec_Methods(t *testing.T) {
	a := &AuditorSpec{
		Stages:        []string{"in_progress", "ai_review"},
		TurnsByStage:  map[string]string{"in_progress": "5"},
		ModelByStage:  map[string]string{"ai_review": "haiku"},
		SkillsByStage: map[string][]string{"in_progress": {"quality-review", "code-style"}},
	}

	if !a.RunsForStage("in_progress") {
		t.Error("RunsForStage(in_progress) should be true")
	}
	if a.RunsForStage("refinement") {
		t.Error("RunsForStage(refinement) should be false (not in list)")
	}
	if a.TurnsForStage("in_progress") != "5" {
		t.Errorf("TurnsForStage = %q, want 5", a.TurnsForStage("in_progress"))
	}
	if a.TurnsForStage("ai_review") != "" {
		t.Errorf("TurnsForStage(unset) = %q, want empty", a.TurnsForStage("ai_review"))
	}
	if a.ModelForStage("ai_review") != "haiku" {
		t.Errorf("ModelForStage = %q, want haiku", a.ModelForStage("ai_review"))
	}
	skills := a.SkillsForStage("in_progress")
	if skills != "quality-review,code-style" {
		t.Errorf("SkillsForStage = %q", skills)
	}
}

func TestAgentType_SupportsStage(t *testing.T) {
	at := AgentType{ValidStages: []Stage{StageRefinement, StageInProgress}}
	if !at.SupportsStage(StageRefinement) {
		t.Error("SupportsStage(refinement) should be true")
	}
	if at.SupportsStage(StageBacklog) {
		t.Error("SupportsStage(backlog) should be false (not declared)")
	}
}

func TestAgentType_DefaultSkillsAndMCP(t *testing.T) {
	at := AgentType{
		SkillsByStage: map[string][]string{"in_progress": {"skill-a", "skill-b"}},
		MCPByStage:    map[string][]string{"in_progress": {"thulu-browser"}},
	}
	if at.DefaultSkills("in_progress") != "skill-a,skill-b" {
		t.Errorf("DefaultSkills = %q", at.DefaultSkills("in_progress"))
	}
	if at.DefaultMCP("in_progress") != "thulu-browser" {
		t.Errorf("DefaultMCP = %q", at.DefaultMCP("in_progress"))
	}
	if at.DefaultSkills("refinement") != "" {
		t.Error("missing stage DefaultSkills should be empty")
	}
	empty := AgentType{}
	if empty.DefaultSkills("in_progress") != "" {
		t.Error("nil SkillsByStage should return empty")
	}
	if empty.DefaultMCP("in_progress") != "" {
		t.Error("nil MCPByStage should return empty")
	}
}

func TestSanitizeImageName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"my-agent", "my-agent"},
		{"My Agent!", "my-agent"},
		{"  leading spaces  ", "leading-spaces"},
		{"", ""},
		{"123abc", "123abc"},
		{"___test___", "test"},
	}
	for _, c := range cases {
		got := SanitizeImageName(c.in)
		if got != c.want {
			t.Errorf("SanitizeImageName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseAuditorLabels_LegacyOn(t *testing.T) {
	labels := map[string]string{
		LabelAuditor: "true",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement, StageDone})
	if spec == nil {
		t.Fatal("expected non-nil spec for legacy auditor=true")
	}
	if len(spec.Stages) != 2 {
		t.Errorf("expected 2 stages, got %d: %v", len(spec.Stages), spec.Stages)
	}
}

func TestParseAuditorLabels_ExplicitStages(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:                     "refinement, done",
		LabelAuditorTurnsLegacy:                "3",
		LabelAuditorModelPrefix + "refinement": "claude-haiku-4-5",
		LabelAuditorSkillsPrefix + "done":      "code-review",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement, StageDone})
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.TurnsByStage["refinement"] != "3" {
		t.Errorf("legacy turns not set: %v", spec.TurnsByStage)
	}
	if spec.ModelByStage["refinement"] != "claude-haiku-4-5" {
		t.Errorf("per-stage model not set: %v", spec.ModelByStage)
	}
	if len(spec.SkillsByStage["done"]) != 1 || spec.SkillsByStage["done"][0] != "code-review" {
		t.Errorf("per-stage skills not set: %v", spec.SkillsByStage)
	}
}

func TestParseAuditorLabels_Disabled(t *testing.T) {
	spec := parseAuditorLabels(map[string]string{}, []Stage{StageRefinement})
	if spec != nil {
		t.Errorf("expected nil for no auditor config, got %+v", spec)
	}
}

func TestParseAuditorLabels_MalformedStageIgnored(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages: "refinement, Not-A-Valid-Stage, custom_qa",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec (refinement is valid)")
	}
	if len(spec.Stages) != 2 || spec.Stages[0] != "refinement" || spec.Stages[1] != "custom_qa" {
		t.Errorf("stages = %v, want [refinement custom_qa]", spec.Stages)
	}
}

func TestParseAuditorLabels_LegacyModelAndSkills(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:       "refinement",
		LabelAuditorModelLegacy:  "claude-haiku-4-5",
		LabelAuditorSkillsLegacy: "code-review,visual-analysis",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.ModelByStage["refinement"] != "claude-haiku-4-5" {
		t.Errorf("legacy model not set: %v", spec.ModelByStage)
	}
	if len(spec.SkillsByStage["refinement"]) != 2 {
		t.Errorf("legacy skills not set: %v", spec.SkillsByStage)
	}
}

func TestParseAuditorLabels_PerStageTurns(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:                     "refinement",
		LabelAuditorTurnsPrefix + "refinement": "5",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.TurnsByStage["refinement"] != "5" {
		t.Errorf("per-stage turns not set: %v", spec.TurnsByStage)
	}
}

func TestParseAuditorLabels_PerStageModelNoLegacy(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:               "done",
		LabelAuditorModelPrefix + "done": "claude-sonnet-4-6",
	}
	spec := parseAuditorLabels(labels, []Stage{StageDone})
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.ModelByStage["done"] != "claude-sonnet-4-6" {
		t.Errorf("per-stage model not set: %v", spec.ModelByStage)
	}
}

func TestParseAuditorLabels_EmptySkillsIgnored(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:                      "refinement",
		LabelAuditorSkillsPrefix + "refinement": "",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if len(spec.SkillsByStage) != 0 {
		t.Errorf("expected no skills, got %v", spec.SkillsByStage)
	}
}

func TestParseTurnsLabel_EmptyPart(t *testing.T) {
	got := parseTurnsLabel("in_progress:5,,refinement:8")
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(got), got)
	}
}

func TestParseAuditorLabels_BarePrefix(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:       "refinement",
		LabelAuditorTurnsPrefix:  "5",
		LabelAuditorModelPrefix:  "claude-sonnet-4-6",
		LabelAuditorSkillsPrefix: "code-review",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.TurnsByStage != nil {
		t.Errorf("TurnsByStage should not be set from empty-suffix label: %v", spec.TurnsByStage)
	}
}

func TestDiscover_EmptyStageSuffix(t *testing.T) {
	baseDir := t.TempDir()
	agentsDir := filepath.Join(baseDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	agentDir := filepath.Join(agentsDir, "empty-suffix-agent")
	os.MkdirAll(agentDir, 0755)
	compose := `services:
  agent:
    build: .
    labels:
      thulu.agent: "true"
      thulu.type: "test"
      thulu.stages: "refinement"
      thulu.skills.default.: "code-review"
      thulu.mcp.default.: "thulu-browser"
`
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644)

	discovered, err := Discover(baseDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(discovered))
	}
	if len(discovered[0].SkillsByStage) != 0 {
		t.Errorf("SkillsByStage should be empty, got %v", discovered[0].SkillsByStage)
	}
	if len(discovered[0].MCPByStage) != 0 {
		t.Errorf("MCPByStage should be empty, got %v", discovered[0].MCPByStage)
	}
}

func TestParseSkillsList_EmptyPartsSkipped(t *testing.T) {
	got := parseSkillsList("code-review,,visual-analysis")
	if len(got) != 2 || got[0] != "code-review" || got[1] != "visual-analysis" {
		t.Errorf("parseSkillsList with empty part = %v, want [code-review visual-analysis]", got)
	}
}

func TestParseSkillsList_Empty(t *testing.T) {
	if got := parseSkillsList(""); got != nil {
		t.Errorf("parseSkillsList('') = %v, want nil", got)
	}
}

func TestDiscover_HomeEnvSkipped(t *testing.T) {
	baseDir := t.TempDir()
	agentsDir := filepath.Join(baseDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	agentDir := filepath.Join(agentsDir, "env-test")
	os.MkdirAll(agentDir, 0755)
	compose := `services:
  agent:
    build: .
    environment:
      - HOME=/home/node
      - INSTANCE_ID=test-123
      - AGENT_NAME=test
      - MY_CUSTOM_VAR=foo
    labels:
      thulu.agent: "true"
      thulu.type: "test"
`
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644)

	discovered, err := Discover(baseDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(discovered))
	}
	at := discovered[0]
	for _, p := range at.Params {
		if p.Name == "HOME" || p.Name == "INSTANCE_ID" {
			t.Errorf("Params should not contain %q", p.Name)
		}
	}
	found := false
	for _, p := range at.Params {
		if p.Name == "MY_CUSTOM_VAR" {
			found = true
			if p.Type != "text" {
				t.Errorf("MY_CUSTOM_VAR type = %q, want text", p.Type)
			}
		}
	}
	if !found {
		t.Error("MY_CUSTOM_VAR not found in Params")
	}
}

func TestDiscover_WorksWithoutRepo(t *testing.T) {
	baseDir := t.TempDir()
	agentsDir := filepath.Join(baseDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	agentDir := filepath.Join(agentsDir, "norepo-agent")
	os.MkdirAll(agentDir, 0755)
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(`services:
  agent:
    build: .
    labels:
      thulu.agent: "true"
      thulu.type: "test"
      thulu.works_without_repo: "true"
`), 0644)

	discovered, _ := Discover(baseDir)
	if len(discovered) != 1 || !discovered[0].WorksWithoutRepo {
		t.Errorf("WorksWithoutRepo not parsed correctly: %+v", discovered)
	}
}

func TestParseAuditorLabels_EmptyStageSuffix(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:       "refinement",
		LabelAuditorTurnsPrefix:  "5",
		LabelAuditorModelPrefix:  "haiku",
		LabelAuditorSkillsPrefix: "code-review",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec (refinement is valid)")
	}
	if spec.TurnsByStage != nil {
		t.Errorf("TurnsByStage should be nil (empty stage suffix ignored), got %v", spec.TurnsByStage)
	}
}

func TestParseAuditorLabels_MalformedStageSuffix(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:                     "refinement",
		LabelAuditorTurnsPrefix + "Bogus-Sta":  "5",
		LabelAuditorModelPrefix + "ALSO BOGUS": "haiku",
		LabelAuditorSkillsPrefix + "not/valid": "code-review",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec (refinement is valid)")
	}
	if spec.TurnsByStage != nil {
		t.Errorf("TurnsByStage should be nil (malformed stage suffix ignored), got %v", spec.TurnsByStage)
	}
	if spec.ModelByStage != nil {
		t.Errorf("ModelByStage should be nil (malformed stage suffix ignored), got %v", spec.ModelByStage)
	}
	if spec.SkillsByStage != nil {
		t.Errorf("SkillsByStage should be nil (malformed stage suffix ignored), got %v", spec.SkillsByStage)
	}
}

func TestParseTurnsLabel_EmptyStage(t *testing.T) {
	got := parseTurnsLabel(":5,in_progress:3")
	if len(got) != 1 {
		t.Errorf("expected 1 entry (empty stage dropped), got %d: %v", len(got), got)
	}
	if got["in_progress"] != "3" {
		t.Errorf("in_progress = %q, want 3", got["in_progress"])
	}
}

func TestDiscover_EmptySkillsAndMCPStage(t *testing.T) {
	baseDir := t.TempDir()
	agentsDir := filepath.Join(baseDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	agentDir := filepath.Join(agentsDir, "edge-agent")
	os.MkdirAll(agentDir, 0755)
	compose := `services:
  agent:
    build: .
    labels:
      thulu.agent: "true"
      thulu.type: "test"
      thulu.skills.default.: "code-review"
      thulu.mcp.default.: "thulu-browser"
      thulu.skills.default.in_progress: ""
      thulu.mcp.default.in_progress: ""
`
	os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0644)

	discovered, err := Discover(baseDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(discovered))
	}
	at := discovered[0]
	if at.SkillsByStage != nil {
		t.Errorf("SkillsByStage should be nil (all entries filtered), got %v", at.SkillsByStage)
	}
	if at.MCPByStage != nil {
		t.Errorf("MCPByStage should be nil (all entries filtered), got %v", at.MCPByStage)
	}
}

func TestParseStagesLabel(t *testing.T) {
	if got := parseStagesLabel(""); got != nil {
		t.Errorf("empty → %v, want nil", got)
	}
	// Unknown but well-formed slugs are kept so per-stage config survives stage deletion/recreation
	// (the UI shows them as orphaned).
	got := parseStagesLabel("refinement, custom_qa, done")
	if len(got) != 3 || got[0] != StageRefinement || got[1] != Stage("custom_qa") || got[2] != StageDone {
		t.Errorf("mixed = %v, want [refinement custom_qa done]", got)
	}
	if got := parseStagesLabel("With-Hyphen,UPPER, ,../etc"); got != nil {
		t.Errorf("all malformed → %v, want nil", got)
	}
}

func TestDiscoverWithInvalidYAML(t *testing.T) {
	baseDir := t.TempDir()
	agentsDir := filepath.Join(baseDir, "agents")
	os.MkdirAll(agentsDir, 0755)

	badDir := filepath.Join(agentsDir, "bad-agent")
	os.MkdirAll(badDir, 0755)
	os.WriteFile(filepath.Join(badDir, "docker-compose.yml"), []byte("{{{invalid yaml"), 0644)

	goodDir := filepath.Join(agentsDir, "good-agent")
	os.MkdirAll(goodDir, 0755)
	os.WriteFile(filepath.Join(goodDir, "docker-compose.yml"), []byte(`services:
  agent:
    build: .
    labels:
      thulu.agent: "true"
      thulu.type: "test"
`), 0644)

	discovered, err := Discover(baseDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Name != "good-agent" {
		t.Errorf("expected 1 good agent, got %d: %v", len(discovered), discovered)
	}
}

func TestParseTurnsLabel_NoColon(t *testing.T) {
	result := parseTurnsLabel("refinement:3,nocolon,done:5")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := result["nocolon"]; ok {
		t.Error("entry without colon should be skipped")
	}
	if result["refinement"] != "3" || result["done"] != "5" {
		t.Errorf("unexpected result: %v", result)
	}
}

func TestParseTurnsLabel_EmptyTurnsValue(t *testing.T) {
	result := parseTurnsLabel("in_progress:,refinement:3")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := result["in_progress"]; ok {
		t.Error("in_progress with empty turns should be skipped")
	}
	if result["refinement"] != "3" {
		t.Errorf("refinement turns = %q, want 3", result["refinement"])
	}
}

func TestParseTurnsLabel_EmptyLabel(t *testing.T) {
	result := parseTurnsLabel("")
	if result != nil {
		t.Errorf("expected nil for empty label, got %v", result)
	}
}

func TestParseTurnsLabel_AllMalformedStages(t *testing.T) {
	result := parseTurnsLabel("Not-A-Stage:5,ALSO INVALID:3")
	if result != nil {
		t.Errorf("expected nil when all stage ids are malformed, got %v", result)
	}
}

func TestParseTurnsLabel_KeepsUnknownButWellFormedStages(t *testing.T) {
	result := parseTurnsLabel("custom_qa:7")
	if result["custom_qa"] != "7" {
		t.Errorf("expected custom_qa kept, got %v", result)
	}
}

func TestParseAuditorLabels_ModelAndSkillsInvalidSuffix(t *testing.T) {
	labels := map[string]string{
		LabelAuditorStages:                    "refinement",
		LabelAuditorModelPrefix + "":          "opus",
		LabelAuditorSkillsPrefix + "":         "code-review",
		LabelAuditorModelPrefix + "Not-Valid": "haiku",
		LabelAuditorModelPrefix + "custom_qa": "sonnet",
	}
	spec := parseAuditorLabels(labels, []Stage{StageRefinement})
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if len(spec.ModelByStage) != 1 || spec.ModelByStage["custom_qa"] != "sonnet" {
		t.Errorf("expected only custom_qa model entry, got %v", spec.ModelByStage)
	}
	if len(spec.SkillsByStage) > 0 {
		t.Errorf("expected no SkillsByStage entries (empty suffix), got %v", spec.SkillsByStage)
	}
}
