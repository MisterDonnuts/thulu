package api

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestCloneTypesIsolatesCallersFromTheCache(t *testing.T) {
	original := []types.AgentType{{
		Name:          "developer",
		ValidStages:   []types.Stage{"in_progress"},
		SkillsByStage: map[string][]string{"in_progress": {"worktree-protocol"}},
		MCPByStage:    map[string][]string{"in_progress": {"thulu-github@write"}},
		Auditor: &types.AuditorSpec{
			Stages:        []string{"in_progress"},
			ModelByStage:  map[string]string{"in_progress": "haiku"},
			EngineByStage: map[string]string{"in_progress": "codex"},
		},
	}}

	copied := cloneTypes(original)
	copied[0].ValidStages[0] = "release"
	copied[0].SkillsByStage["in_progress"][0] = "something-else"
	copied[0].MCPByStage["in_progress"] = append(copied[0].MCPByStage["in_progress"], "ticket-reader")
	copied[0].Auditor.ModelByStage["in_progress"] = "opus"
	copied[0].Auditor.EngineByStage["in_progress"] = "opencode"
	copied[0].Auditor.Stages[0] = "testing"

	if original[0].ValidStages[0] != "in_progress" {
		t.Fatal("mutating a copy changed the cached stages")
	}
	if original[0].SkillsByStage["in_progress"][0] != "worktree-protocol" {
		t.Fatal("mutating a copy changed the cached skill grants")
	}
	if len(original[0].MCPByStage["in_progress"]) != 1 {
		t.Fatal("mutating a copy changed the cached MCP grants")
	}
	if original[0].Auditor.EngineByStage["in_progress"] != "codex" {
		t.Error("EngineByStage was not deep-copied")
	}
	if original[0].Auditor.ModelByStage["in_progress"] != "haiku" {
		t.Fatal("mutating a copy changed the cached auditor model")
	}
	if original[0].Auditor.Stages[0] != "in_progress" {
		t.Fatal("mutating a copy changed the cached auditor stages")
	}
}

func TestCatalogContentReadsEachKindFromItsTenant(t *testing.T) {
	t.Parallel()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	put := func(tenant, kind, owner, body string) {
		t.Helper()
		path := "prompt.md"
		if kind == agentstore.KindTool {
			path = "skill.md"
		}
		if _, err := store.PutFile(ctx, tenant, kind, owner, path, []byte(body), "test"); err != nil {
			t.Fatalf("put %s/%s/%s: %v", tenant, kind, owner, err)
		}
	}
	put("ws-a", agentstore.KindAgent, "dev", "a's prompt")
	put("ws-b", agentstore.KindAgent, "dev", "b's prompt")
	put("ws-a", agentstore.KindSkill, "notes", "a's notes")
	put("ws-a", agentstore.KindSkill, "octopus-test-cases", "a's stale copy")
	put(agentstore.SystemTenant, agentstore.KindSkill, "octopus-test-cases", "system copy")
	put(agentstore.GlobalTenant, agentstore.KindTool, "thulu-github", "manual")

	src := catalogContent{store: store, tenant: "ws-a"}
	read := func(kind, owner string) string {
		t.Helper()
		files, err := src.Files(ctx, kind, owner)
		if err != nil {
			t.Fatalf("Files %s/%s: %v", kind, owner, err)
		}
		if len(files) != 1 {
			return ""
		}
		return string(files[0].Body)
	}
	if got := read(agentstore.KindAgent, "dev"); got != "a's prompt" {
		t.Fatalf("agent = %q, want ws-a's own", got)
	}
	if got := read(agentstore.KindSkill, "notes"); got != "a's notes" {
		t.Fatalf("workspace skill = %q", got)
	}
	if got := read(agentstore.KindSkill, "octopus-test-cases"); got != "system copy" {
		t.Fatalf("system skill = %q, want the system copy over the workspace's", got)
	}
	if got := read(agentstore.KindTool, "thulu-github"); got != "manual" {
		t.Fatalf("tool manual = %q, want the install's", got)
	}
	other := catalogContent{store: store, tenant: "ws-c"}
	if files, _ := other.Files(ctx, agentstore.KindAgent, "dev"); len(files) != 0 {
		t.Fatalf("a workspace with no agent read another's: %+v", files)
	}
}

func TestStageSkillsAddsSystemSkillsByMCP(t *testing.T) {
	at := types.AgentType{
		SkillsByStage: map[string][]string{"in_progress": {"notes"}, "review": {"notes"}},
		MCPByStage:    map[string][]string{"in_progress": {"thulu-browser@write"}},
		SystemSkills: []types.SystemSkillRef{
			{Name: "octopus-browser-evidence", MCP: "thulu-browser"},
			{Name: "octopus-everywhere"},
			{Name: "notes"},
		},
	}
	got := at.StageSkills("in_progress")
	want := []string{"notes", "octopus-browser-evidence", "octopus-everywhere"}
	if len(got) != len(want) {
		t.Fatalf("in_progress = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("in_progress = %v, want %v", got, want)
		}
	}
	if got := at.StageSkills("review"); len(got) != 2 || got[1] != "octopus-everywhere" {
		t.Fatalf("review = %v: the browser skill needs the browser wired", got)
	}
}
