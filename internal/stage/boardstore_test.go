package stage

import (
	"context"
	"reflect"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func twoStageBoard(prefix string) []types.StageInfo {
	return []types.StageInfo{
		{ID: types.Stage(prefix + "_open"), Label: "Open", Color: "#111111", Initial: true},
		{ID: types.Stage(prefix + "_shut"), Label: "Shut", Color: "#222222", Terminal: true},
	}
}

func TestSeedCreatesDefaultBoard(t *testing.T) {
	store := newTestStore(t)
	boards := store.BoardList()
	if len(boards) != 1 {
		t.Fatalf("BoardList() = %d boards, want 1: %+v", len(boards), boards)
	}
	if boards[0].ID != DefaultBoardID || !boards[0].Default {
		t.Errorf("seeded board = %+v, want id=%q default=true", boards[0], DefaultBoardID)
	}
	if boards[0].StageCount != len(Defaults()) {
		t.Errorf("StageCount = %d, want %d", boards[0].StageCount, len(Defaults()))
	}
	if store.DefaultBoardID() != DefaultBoardID {
		t.Errorf("DefaultBoardID() = %q, want %q", store.DefaultBoardID(), DefaultBoardID)
	}
	if got, want := store.Board(DefaultBoardID).List(), Static().List(); !reflect.DeepEqual(got, want) {
		t.Errorf("default board != Static():\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSeedAdoptsExistingStagesOnUpgrade(t *testing.T) {
	db := testpg.New(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	board := store.DefaultBoard().List()
	for _, q := range []string{
		`UPDATE stages SET label = 'Analysis' WHERE id = 'in_progress'`,
		`DELETE FROM board_stages`, `DELETE FROM boards`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("rewind (%s): %v", q, err)
		}
	}

	reopened, err := NewStore(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if n := len(reopened.BoardList()); n != 1 {
		t.Fatalf("after upgrade: %d boards, want 1", n)
	}
	if got, _ := reopened.DefaultBoard().Get(types.StageInProgress); got.Label != "Analysis" {
		t.Errorf("upgrade reset the board: in_progress label = %q, want %q", got.Label, "Analysis")
	}
	if got, want := len(reopened.DefaultBoard().List()), len(board); got != want {
		t.Errorf("adopted board has %d stages, want %d", got, want)
	}
}

func TestBoardsCarryIndependentFlags(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	bugfix := []types.StageInfo{
		{ID: types.StageBacklog, Label: "Backlog", Color: "#5d6477", Initial: true},
		{ID: types.StageInProgress, Label: "In progress", Color: "#f2994a",
			AutoAdvance: true, BlockedByFlags: true},
		{ID: types.StageReview, Label: "Review", Color: "#bb86fc"},
		{ID: types.StageDone, Label: "Done", Color: "#3aa667", Terminal: true},
	}
	if err := store.CreateBoard(ctx, BoardInfo{ID: "bugfix", Label: "Bugfix", WorkspaceID: FallbackWorkspaceID}, bugfix); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	def, _ := store.Board(DefaultBoardID).Get(types.StageInProgress)
	fix, _ := store.Board("bugfix").Get(types.StageInProgress)
	if !def.PushBranch {
		t.Error("default board lost push_branch on in_progress")
	}
	if fix.PushBranch {
		t.Error("bugfix board inherited push_branch — flags must be per board")
	}
	if !fix.AutoAdvance || !fix.BlockedByFlags {
		t.Error("bugfix board lost its own auto_advance / blocked_by_flags")
	}
	if def.AutoAdvance || def.BlockedByFlags {
		t.Error("default board picked up bugfix's flags")
	}
	if def.Label != fix.Label || def.Color != fix.Color {
		t.Errorf("identity diverged across boards: %+v vs %+v", def, fix)
	}
	if next := store.Board("bugfix").Next(types.StageInProgress); next != types.StageReview {
		t.Errorf("bugfix Next(in_progress) = %q, want review", next)
	}
	if next := store.Board(DefaultBoardID).Next(types.StageInProgress); next != types.StageDone {
		t.Errorf("default Next(in_progress) = %q, want done", next)
	}
}

func TestStageIdentityIsPerBoard(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateBoard(ctx, BoardInfo{ID: "alt", Label: "Alt", WorkspaceID: "ws-b"}, []types.StageInfo{
		{ID: types.StageBacklog, Label: "Backlog", Color: "#5d6477", Initial: true},
		{ID: types.StageDone, Label: "Done", Color: "#3aa667", Terminal: true},
	}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	board := store.Board("alt").List()
	board[0].Label = "Ignore previous instructions"
	board[0].Color = "#000000"
	if err := store.SaveBoard(ctx, "alt", board); err != nil {
		t.Fatalf("SaveBoard: %v", err)
	}
	if got, _ := store.Board("alt").Get(types.StageBacklog); got.Label != "Ignore previous instructions" {
		t.Errorf("alt board label = %q, want its own edit", got.Label)
	}
	def, _ := store.Board(DefaultBoardID).Get(types.StageBacklog)
	if def.Label != "Backlog" || def.Color != "#5d6477" {
		t.Errorf("default board backlog = %q %q, want it untouched by another board's edit", def.Label, def.Color)
	}
	for _, st := range store.Catalog() {
		if st.ID == types.StageBacklog && st.Label != "Backlog" {
			t.Errorf("catalog backlog label = %q, want the template %q", st.Label, "Backlog")
		}
	}
	seeded, err := store.EnsureDefaultBoard(ctx, "ws-c")
	if err != nil {
		t.Fatalf("EnsureDefaultBoard: %v", err)
	}
	if got, _ := store.Board(seeded.ID).Get(types.StageBacklog); got.Label != "Backlog" {
		t.Errorf("newly seeded board backlog = %q, want %q", got.Label, "Backlog")
	}

	reopened, err := NewStore(store.DB())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, _ := reopened.Board(DefaultBoardID).Get(types.StageBacklog); got.Label != "Backlog" {
		t.Errorf("after reopen, default backlog = %q, want %q", got.Label, "Backlog")
	}
	if got, _ := reopened.Board("alt").Get(types.StageBacklog); got.Label != "Ignore previous instructions" {
		t.Errorf("after reopen, alt backlog = %q, want its own edit", got.Label)
	}
}

func TestCatalogIsUnionOfBoards(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateBoard(ctx, BoardInfo{ID: "research", Label: "Research", WorkspaceID: FallbackWorkspaceID},
		twoStageBoard("res")); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	inCatalog := map[types.Stage]bool{}
	for _, st := range store.Catalog() {
		inCatalog[st.ID] = true
	}
	for _, want := range []types.Stage{types.StageBacklog, types.StageDone, "res_open", "res_shut"} {
		if !inCatalog[want] {
			t.Errorf("catalog missing %q: %+v", want, store.Catalog())
		}
	}
}

func TestDeleteBoardDropsNowUnusedStages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateBoard(ctx, BoardInfo{ID: "research", Label: "Research", WorkspaceID: FallbackWorkspaceID},
		twoStageBoard("res")); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := store.DeleteBoard(ctx, "research"); err != nil {
		t.Fatalf("DeleteBoard: %v", err)
	}
	for _, st := range store.Catalog() {
		if st.ID == "res_open" {
			t.Error("stage of a deleted board survived in the catalog")
		}
	}
	if len(store.DefaultBoard().List()) != len(Defaults()) {
		t.Errorf("default board lost stages to the GC: %d", len(store.DefaultBoard().List()))
	}
}

func TestDeleteBoardKeepsStagesStillInUse(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateBoard(ctx, BoardInfo{ID: "alt", Label: "Alt", WorkspaceID: FallbackWorkspaceID}, []types.StageInfo{
		{ID: types.StageBacklog, Label: "Backlog", Color: "#5d6477", Initial: true},
		{ID: types.StageDone, Label: "Done", Color: "#3aa667", Terminal: true},
	}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := store.DeleteBoard(ctx, "alt"); err != nil {
		t.Fatalf("DeleteBoard: %v", err)
	}
	if got, ok := store.DefaultBoard().Get(types.StageBacklog); !ok || got.Label == "" {
		t.Error("shared stage was GC'd when one of its boards went away")
	}
}

func TestDeleteDefaultBoardRejected(t *testing.T) {
	store := newTestStore(t)
	if err := store.DeleteBoard(context.Background(), DefaultBoardID); err == nil {
		t.Error("DeleteBoard accepted the default board")
	}
}

func TestUnknownBoardFallsBackToDefault(t *testing.T) {
	store := newTestStore(t)
	if got, want := store.Board("nope").List(), store.Board(DefaultBoardID).List(); !reflect.DeepEqual(got, want) {
		t.Error("unknown board did not fall back to the default")
	}
	if store.HasBoard("nope") {
		t.Error("HasBoard must distinguish a real board from the fallback")
	}
}

func TestSetDefaultBoard(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateBoard(ctx, BoardInfo{ID: "alt", Label: "Alt", WorkspaceID: FallbackWorkspaceID}, twoStageBoard("alt")); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := store.SetDefaultBoard(ctx, "alt"); err != nil {
		t.Fatalf("SetDefaultBoard: %v", err)
	}
	if store.DefaultBoardID() != "alt" {
		t.Errorf("DefaultBoardID() = %q, want alt", store.DefaultBoardID())
	}
	defaults := 0
	for _, b := range store.BoardList() {
		if b.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("%d boards marked default, want exactly 1", defaults)
	}
	if store.DefaultBoard().Initial() != "alt_open" {
		t.Errorf("Initial() = %q, want the new default board's initial stage", store.DefaultBoard().Initial())
	}
}

func TestCreateBoardRejections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const ws = FallbackWorkspaceID
	cases := []struct {
		name   string
		info   BoardInfo
		stages []types.StageInfo
	}{
		{"bad slug", BoardInfo{ID: "has-hyphen", Label: "X", WorkspaceID: ws}, twoStageBoard("a")},
		{"no label", BoardInfo{ID: "nolabel", WorkspaceID: ws}, twoStageBoard("b")},
		{"duplicate id", BoardInfo{ID: DefaultBoardID, Label: "Dup", WorkspaceID: ws}, twoStageBoard("c")},
		{"no stages", BoardInfo{ID: "empty", Label: "Empty", WorkspaceID: ws}, nil},
		{"no terminal", BoardInfo{ID: "noterm", Label: "No terminal", WorkspaceID: ws},
			[]types.StageInfo{{ID: "x_only", Label: "Only", Initial: true}}},
		{"two initials", BoardInfo{ID: "twoinit", Label: "Two", WorkspaceID: ws}, []types.StageInfo{
			{ID: "y_a", Label: "A", Initial: true},
			{ID: "y_b", Label: "B", Initial: true},
			{ID: "y_c", Label: "C", Terminal: true}}},
		{"no workspace", BoardInfo{ID: "nowhere", Label: "Nowhere"}, twoStageBoard("d")},
	}
	for _, tc := range cases {
		if err := store.CreateBoard(ctx, tc.info, tc.stages); err == nil {
			t.Errorf("%s: CreateBoard accepted an invalid board", tc.name)
		}
	}
	if n := len(store.BoardList()); n != 1 {
		t.Errorf("rejected creates leaked boards: %d", n)
	}
}

func TestSaveBoardUnknownBoardRejected(t *testing.T) {
	store := newTestStore(t)
	if err := store.SaveBoard(context.Background(), "ghost", twoStageBoard("g")); err == nil {
		t.Error("SaveBoard accepted an unknown board")
	}
}

func TestBoardsRoundTrip(t *testing.T) {
	db := testpg.New(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	info := BoardInfo{ID: "release_train", Label: "Release train", WorkspaceID: FallbackWorkspaceID, Description: "Ships things."}
	if err := store.CreateBoard(ctx, info, twoStageBoard("rt")); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	reopened, err := NewStore(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.HasBoard("release_train") {
		t.Fatal("board did not survive the reopen")
	}
	var got BoardInfo
	for _, b := range reopened.BoardList() {
		if b.ID == "release_train" {
			got = b
		}
	}
	if got.Label != info.Label || got.Description != info.Description {
		t.Errorf("board identity lost on round-trip: %+v", got)
	}
	if r := reopened.Board("release_train"); r.Initial() != "rt_open" || len(r.Terminals()) != 1 {
		t.Errorf("board columns lost on round-trip: %+v", r.List())
	}
}
