package api

import (
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func promptBoard() []types.StageInfo {
	return []types.StageInfo{
		{ID: "refinement", Label: "Refinement", Position: 1},
		{ID: "development", Label: "Development", Position: 2},
		{ID: "review", Label: "Review", Position: 3},
	}
}

func stageIDs(entries []PromptEntry) []string {
	out := make([]string, len(entries))
	for i := range entries {
		out[i] = entries[i].Stage
		if entries[i].Orphan {
			out[i] += "(orphan)"
		}
	}
	return out
}

func sameIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

func TestAgentBoardStagesKeepsTheDeclaredOnesInFlowOrder(t *testing.T) {
	board := promptBoard()
	got := agentBoardStages(board, []types.Stage{"review", types.StageChat, "refinement"})
	ids := make([]string, len(got))
	for i := range got {
		ids[i] = string(got[i].ID)
	}
	sameIDs(t, "declared review+chat+refinement", ids, []string{"refinement", "review"})

	if all := agentBoardStages(board, nil); len(all) != len(board) {
		t.Errorf("an agent that declares no stages got %d slots, want all %d", len(all), len(board))
	}
}

func TestPromptSlotsOfferOnlyTheModesTheAgentRunsIn(t *testing.T) {
	virtual := stagepkg.Virtuals()
	ids := func(in []types.StageInfo) []string {
		out := make([]string, len(in))
		for i := range in {
			out[i] = string(in[i].ID)
		}
		return out
	}
	sameIDs(t, "chat only", ids(agentBoardStages(virtual, []types.Stage{"review", types.StageChat})),
		[]string{"chat"})
	sameIDs(t, "both", ids(agentBoardStages(virtual, []types.Stage{types.StageChat, types.StageMention})),
		[]string{"mention", "chat"})
	sameIDs(t, "board stages only", ids(agentBoardStages(virtual, []types.Stage{"review"})), []string{})
	if all := agentBoardStages(virtual, nil); len(all) != len(virtual) {
		t.Errorf("an agent that declares no stages got %d mode slots, want all %d", len(all), len(virtual))
	}
}

func TestPromptSetListsChatMentionAndOrphans(t *testing.T) {
	board := promptBoard()
	slots := append(agentBoardStages(board, []types.Stage{"development", types.StageChat}), stagepkg.Virtuals()...)
	paths := []string{
		"prompts/main.md",
		"prompts/chat.md",
		"prompts/review.md",
		"prompts/qa_legacy.md",
		"prompts/notes/x.md",
		"prompts/Bad-Name.md",
		"auditor/default.md",
	}
	have := map[string]bool{}
	for _, p := range paths {
		have[p] = true
	}
	exists := func(rel string) bool { return have[rel] }

	set := buildPromptSet(slots, board, paths, exists, "agent", "", "", "prompts", "main.md")
	sameIDs(t, "agent set", stageIDs(set.Stages),
		[]string{"development", "mention", "chat", "qa_legacy(orphan)", "review(orphan)"})

	byStage := map[string]PromptEntry{}
	for _, e := range set.Stages {
		byStage[e.Stage] = e
	}
	if e := byStage["chat"]; !e.Exists || e.Fallback != "" || e.Path != "prompts/chat.md" {
		t.Errorf("chat entry = %+v, want an existing prompts/chat.md", e)
	}
	if e := byStage["mention"]; e.Exists || e.Fallback != "prompts/main.md" {
		t.Errorf("mention entry = %+v, want missing with main.md as fallback", e)
	}
	if e := byStage["review"]; e.Label != "Review" || !e.Exists {
		t.Errorf("orphan on a board = %+v, want its board label", e)
	}
	if e := byStage["qa_legacy"]; e.Label != "qa_legacy" {
		t.Errorf("orphan off every board = %+v, want its id as label", e)
	}

	aud := buildPromptSet(agentBoardStages(board, []types.Stage{"development"}), board, paths, exists,
		"auditor", "", "", "auditor", "default.md")
	sameIDs(t, "auditor set", stageIDs(aud.Stages), []string{"development"})
}
