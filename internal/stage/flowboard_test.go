package stage

import (
	"context"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func flowTypes() []TaskTypeInfo {
	return []TaskTypeInfo{
		{ID: "feature", Label: "Feature", Default: true, Skips: []types.Stage{}},
		{ID: "release", Label: "Release", Skips: []types.Stage{"test_design", types.StageInProgress, "testing", types.StageReview},
			Agents: map[types.Stage]string{"release": "shipper"}},
	}
}

func flowInfo(id string) BoardInfo {
	return BoardInfo{ID: id, Label: "Flow", WorkspaceID: "ws-flow", MaxKickBacks: 2,
		Flow: &BoardFlow{Name: "thulu", Revision: 1, Hash: "abc"}}
}

func TestInstallFlowBoardWritesBoardStagesAndTypes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.InstallFlowBoard(ctx, flowInfo("thulu"), flowBoard(), flowTypes()); err != nil {
		t.Fatalf("install: %v", err)
	}
	info, ok := BoardInfoOf(store, "thulu")
	if !ok || info.Flow == nil || info.Flow.Name != "thulu" || info.MaxKickBacks != 2 || !info.Default {
		t.Fatalf("board = %+v (flow %+v)", info, info.Flow)
	}
	tt := store.BoardTypes("thulu")
	if len(tt) != 2 || tt[1].Agents["release"] != "shipper" || tt[1].WorkspaceID != "ws-flow" {
		t.Fatalf("types = %+v", tt)
	}
	if got := store.BoardFor("thulu", "release").Next(types.StageBacklog); got != types.StageRefinement {
		t.Errorf("release after backlog = %q", got)
	}
}

func TestInstallFlowBoardIsAtomic(t *testing.T) {
	store := newTestStore(t)
	bad := flowTypes()
	bad[1].Agents = map[types.Stage]string{types.StageInProgress: "shipper"} // a stage it skips
	if err := store.InstallFlowBoard(context.Background(), flowInfo("thulu"), flowBoard(), bad); err == nil {
		t.Fatal("an invalid type list installed")
	}
	if store.HasBoard("thulu") {
		t.Error("a refused install left a board behind")
	}
	if err := store.InstallFlowBoard(context.Background(), flowInfo(DefaultBoardID), flowBoard(), flowTypes()); err == nil {
		t.Error("an install over an existing board id succeeded")
	}
}

func TestUpdateFlowBoardKeepsIdentityAndDropsStages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.InstallFlowBoard(ctx, flowInfo("thulu"), flowBoard(), flowTypes()); err != nil {
		t.Fatal(err)
	}
	board := store.Board("thulu").List()
	board[1].Label, board[1].Color = "Spec", "#123456"
	if err := store.SaveBoard(ctx, "thulu", board); err != nil {
		t.Fatal(err)
	}
	tt := store.BoardTypes("thulu")
	tt[0].Label, tt[0].Icon = "Story", "star"
	if err := store.SaveBoardTypes(ctx, "thulu", tt); err != nil {
		t.Fatal(err)
	}

	next := flowBoard()
	next[1].AutoAdvance = true
	next = append(next[:4], next[5:]...) // drop testing
	info := flowInfo("thulu")
	info.Label, info.MaxKickBacks, info.Flow.Revision = "Ignored", 3, 2
	types2 := flowTypes()
	types2[1].Skips = []types.Stage{"test_design", types.StageInProgress, types.StageReview}
	if err := store.UpdateFlowBoard(ctx, info, next, types2); err != nil {
		t.Fatalf("update: %v", err)
	}
	ref, _ := store.Board("thulu").Get(types.StageRefinement)
	if !ref.AutoAdvance || ref.Label != "Spec" || ref.Color != "#123456" {
		t.Errorf("refinement after update = %+v", ref)
	}
	if store.Board("thulu").Valid("testing") {
		t.Error("the dropped stage is still on the board")
	}
	got, _ := BoardInfoOf(store, "thulu")
	if got.Label != "Flow" || got.MaxKickBacks != 3 || got.Flow.Revision != 2 {
		t.Errorf("board after update = %+v", got)
	}
	if feat := store.BoardTypes("thulu")[0]; feat.Label != "Story" || feat.Icon != "star" {
		t.Errorf("feature after update = %+v", feat)
	}
}

func TestFreeBoardIDFallsBackPerWorkspace(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if id, err := store.FreeBoardID("thulu", "ws-a"); err != nil || id != "thulu" {
		t.Fatalf("free id = %q %v", id, err)
	}
	if err := store.InstallFlowBoard(ctx, flowInfo("thulu"), flowBoard(), flowTypes()); err != nil {
		t.Fatal(err)
	}
	if id, _ := store.FreeBoardID("thulu", "ws-a"); id != "thulu_ws_a" {
		t.Errorf("second id = %q, want thulu_ws_a", id)
	}
	if _, err := store.FreeBoardID("Bad-Id", "ws-a"); err == nil {
		t.Error("an invalid base was accepted")
	}
}

func TestBoardAgentsLayersColumnAndType(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	board := flowBoard()
	board = append(board[:6], append([]types.StageInfo{{ID: "release", Label: "Release", DefaultAgent: "watcher"}}, board[7:]...)...)
	tt := flowTypes()
	tt[0].Agents = map[types.Stage]string{"release": ""}
	if err := store.InstallFlowBoard(ctx, flowInfo("thulu"), board, tt); err != nil {
		t.Fatal(err)
	}
	cols, byType := BoardAgents(store, "thulu", "feature")
	if cols[types.StageInProgress] != "coder" || cols["release"] != "watcher" {
		t.Errorf("columns = %v", cols)
	}
	if agent, ok := byType["release"]; !ok || agent != "" {
		t.Errorf("feature clears release: %v", byType)
	}
	cols, byType = BoardAgents(store, "thulu", "release")
	if _, ok := cols[types.StageInProgress]; ok {
		t.Errorf("a stage the type skips kept its column agent: %v", cols)
	}
	if byType["release"] != "shipper" {
		t.Errorf("release type agents = %v", byType)
	}
	if got := TypeAgentStages(store, "thulu"); !got["release"] || len(got) != 1 {
		t.Errorf("type agent stages = %v", got)
	}
}
