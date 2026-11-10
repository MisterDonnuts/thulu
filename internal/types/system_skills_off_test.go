package types

import (
	"slices"
	"testing"
)

func systemSkillsAgent() AgentType {
	return AgentType{
		SkillsByStage: map[string][]string{"in_progress": {"notes"}, "review": {"notes"}},
		MCPByStage: map[string][]string{
			"in_progress": {"octopus-api@write"},
			"review":      {"octopus-api"},
		},
		SystemSkills: []SystemSkillRef{
			{Name: "octopus-run"},
			{Name: "octopus-task-workflow", MCP: "octopus-api"},
			{Name: "octopus-browser-evidence", MCP: "thulu-browser"},
		},
	}
}

func TestStageSkillsWithNothingTurnedOffKeepsEverySystemSkill(t *testing.T) {
	at := systemSkillsAgent()
	want := []string{"notes", "octopus-run", "octopus-task-workflow"}
	if got := at.StageSkills("in_progress"); !slices.Equal(got, want) {
		t.Fatalf("in_progress = %v, want %v", got, want)
	}
}

func TestStageSkillsDropsATurnedOffSkillOnlyOnItsStage(t *testing.T) {
	at := systemSkillsAgent()
	at.SystemSkillsOff = map[string][]string{"in_progress": {"octopus-task-workflow"}}

	if got, want := at.StageSkills("in_progress"), []string{"notes", "octopus-run"}; !slices.Equal(got, want) {
		t.Fatalf("in_progress = %v, want %v", got, want)
	}
	if got, want := at.StageSkills("review"), []string{"notes", "octopus-run", "octopus-task-workflow"}; !slices.Equal(got, want) {
		t.Fatalf("review = %v, want %v: the opt-out belongs to in_progress", got, want)
	}
	if got := at.DefaultSkills("in_progress"); got != "notes,octopus-run" {
		t.Fatalf("DefaultSkills = %q: $SKILLS must honor the opt-out too", got)
	}
}

func TestStageSkillsCanTurnOffOctopusRun(t *testing.T) {
	at := systemSkillsAgent()
	at.SystemSkillsOff = map[string][]string{"review": {" octopus-run "}}
	if got, want := at.StageSkills("review"), []string{"notes", "octopus-task-workflow"}; !slices.Equal(got, want) {
		t.Fatalf("review = %v, want %v", got, want)
	}
}

func TestStageSkillsIgnoresUnknownOptOuts(t *testing.T) {
	at := systemSkillsAgent()
	at.SystemSkillsOff = map[string][]string{"in_progress": {"octopus-gone", "notes", ""}}
	want := []string{"notes", "octopus-run", "octopus-task-workflow"}
	if got := at.StageSkills("in_progress"); !slices.Equal(got, want) {
		t.Fatalf("in_progress = %v, want %v", got, want)
	}
}

func TestStageSkillsDropsAGrantNamedLikeATurnedOffSystemSkill(t *testing.T) {
	at := systemSkillsAgent()
	at.SkillsByStage["in_progress"] = []string{"notes", "octopus-run"}
	at.SystemSkillsOff = map[string][]string{"in_progress": {"octopus-run"}}
	if got, want := at.StageSkills("in_progress"), []string{"notes", "octopus-task-workflow"}; !slices.Equal(got, want) {
		t.Fatalf("in_progress = %v, want %v", got, want)
	}
}

func TestMCPWired(t *testing.T) {
	entries := []string{"octopus-api@write+task_create", " thulu-github "}
	for server, want := range map[string]bool{
		"":              true,
		"octopus-api":   true,
		"thulu-github":  true,
		"thulu-browser": false,
	} {
		if got := MCPWired(entries, server); got != want {
			t.Errorf("MCPWired(%q) = %v, want %v", server, got, want)
		}
	}
}

func TestSystemSkillsOffLabelParses(t *testing.T) {
	var at AgentType
	parseDefaultSkillsAndMCP(map[string]string{
		LabelSystemSkillsOffPrefix + "in_progress": "octopus-run, octopus-task-workflow",
		LabelSystemSkillsOffPrefix + "review":      "",
		LabelSkillsDefaultPrefix + "in_progress":   "notes",
	}, &at)
	if got := at.SystemSkillsOff["in_progress"]; !slices.Equal(got, []string{"octopus-run", "octopus-task-workflow"}) {
		t.Fatalf("system_off.in_progress = %v", got)
	}
	if _, ok := at.SystemSkillsOff["review"]; ok {
		t.Fatal("an empty label must not create an entry")
	}
	if got := at.SkillsByStage["in_progress"]; !slices.Equal(got, []string{"notes"}) {
		t.Fatalf("the grants label was read as an opt-out: %v", got)
	}
}
