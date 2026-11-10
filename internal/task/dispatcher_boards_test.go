package task

import (
	"context"
	"path/filepath"
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func twoBoards() stagepkg.Boards {
	return &fakeBoards{
		def: "fast",
		regs: map[string]stagepkg.Registry{
			"fast": stagepkg.NewSnapshot([]types.StageInfo{
				{ID: "intake", Label: "Intake", Position: 10, Initial: true, AutoAdvance: true},
				{ID: "work", Label: "Work", Position: 20, AutoAdvance: true},
				{ID: "out", Label: "Out", Position: 30, Terminal: true},
			}),
			"slow": stagepkg.NewSnapshot([]types.StageInfo{
				{ID: "intake", Label: "Intake", Position: 10, Initial: true},
				{ID: "work", Label: "Work", Position: 20},
				{ID: "review", Label: "Review", Position: 30, BlockedByFlags: true},
				{ID: "out", Label: "Out", Position: 40, Terminal: true},
			}),
		},
	}
}

type fakeBoards struct {
	def   string
	regs  map[string]stagepkg.Registry
	types map[string][]stagepkg.TaskTypeInfo
}

func (f *fakeBoards) Board(id string) stagepkg.Registry {
	if r, ok := f.regs[id]; ok {
		return r
	}
	return f.regs[f.def]
}

func (f *fakeBoards) BoardFor(boardID, typeID string) stagepkg.Registry {
	reg := f.Board(boardID)
	for _, t := range f.types[boardID] {
		if t.ID == typeID {
			return stagepkg.NewSnapshot(stagepkg.FilterStages(reg.List(), t.Skips))
		}
	}
	return reg
}

func (f *fakeBoards) BoardTypes(boardID string) []stagepkg.TaskTypeInfo {
	return f.types[boardID]
}

func (f *fakeBoards) TypeCatalog() []stagepkg.TaskTypeInfo {
	var out []stagepkg.TaskTypeInfo
	for _, b := range f.BoardList() {
		out = append(out, f.types[b.ID]...)
	}
	return out
}

func (f *fakeBoards) BoardList() []stagepkg.BoardInfo {
	out := []stagepkg.BoardInfo{{ID: f.def, Label: f.def, Position: 10, Default: true}}
	for id := range f.regs {
		if id != f.def {
			out = append(out, stagepkg.BoardInfo{ID: id, Label: id, Position: 20})
		}
	}
	return out
}

func (f *fakeBoards) DefaultBoardID() string { return f.def }

func (f *fakeBoards) DefaultBoardIDFor(string) string { return f.def }

func (f *fakeBoards) Catalog() []types.StageInfo {
	seen := map[types.Stage]bool{}
	var out []types.StageInfo
	for _, b := range f.BoardList() {
		for _, st := range f.regs[b.ID].List() {
			if !seen[st.ID] {
				seen[st.ID] = true
				out = append(out, st)
			}
		}
	}
	return out
}

func newBoardsTestDispatcher(t *testing.T) (*Dispatcher, *Store) {
	t.Helper()
	db := newTaskDB(t)
	store, err := NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	boards := twoBoards()
	store.WithBoards(boards)
	return NewDispatcher(store, nil, nil, nil, nil).WithBoards(boards), store
}

func mustCreateOn(t *testing.T, store *Store, board string, stage Stage) *Task {
	t.Helper()
	tk := &Task{WorkspaceID: testWS, Title: "t", BoardID: board, Stage: stage}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create on %s: %v", board, err)
	}
	return tk
}

func TestCreateUsesOwnBoardInitialStage(t *testing.T) {
	_, store := newBoardsTestDispatcher(t)
	slow := &Task{WorkspaceID: testWS, Title: "slow one", BoardID: "slow"}
	if err := store.Create(t.Context(), slow); err != nil {
		t.Fatalf("create: %v", err)
	}
	if slow.Stage != "intake" {
		t.Errorf("stage = %q, want the slow board's initial", slow.Stage)
	}
	got, err := store.Get(t.Context(), slow.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.BoardID != "slow" {
		t.Errorf("board_id = %q after round-trip, want slow", got.BoardID)
	}
}

func TestCreateDefaultsToDefaultBoard(t *testing.T) {
	_, store := newBoardsTestDispatcher(t)
	tk := &Task{WorkspaceID: testWS, Title: "x"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	if tk.BoardID != "fast" {
		t.Errorf("board_id = %q, want the default board", tk.BoardID)
	}
}

func TestAutoAdvanceFollowsTheTasksOwnBoard(t *testing.T) {
	d, store := newBoardsTestDispatcher(t)
	fast := mustCreateOn(t, store, "fast", "work")
	slow := mustCreateOn(t, store, "slow", "work")

	d.autoAdvanceStage(context.Background(), fast, fast.ID, "work", 0, "")
	d.autoAdvanceStage(context.Background(), slow, slow.ID, "work", 0, "")

	afterFast, _ := store.Get(t.Context(), fast.ID)
	afterSlow, _ := store.Get(t.Context(), slow.ID)
	if afterFast.Stage != "out" {
		t.Errorf("fast board: stage = %q, want it to have advanced to out", afterFast.Stage)
	}
	if afterSlow.Stage != "work" {
		t.Errorf("slow board: stage = %q, want it to have stayed in work", afterSlow.Stage)
	}
}

func TestDependencyTerminalityIsPerBoard(t *testing.T) {
	_, store := newBoardsTestDispatcher(t)

	doneBlocker := mustCreateOn(t, store, "slow", "out")
	openBlocker := mustCreateOn(t, store, "slow", "work")

	waiterA := mustCreateOn(t, store, "fast", "intake")
	waiterB := mustCreateOn(t, store, "fast", "intake")
	if err := store.AddDependency(t.Context(), waiterA.ID, doneBlocker.ID, DepHard); err != nil {
		t.Fatalf("add dep A: %v", err)
	}
	if err := store.AddDependency(t.Context(), waiterB.ID, openBlocker.ID, DepHard); err != nil {
		t.Fatalf("add dep B: %v", err)
	}

	blockedA, _, err := store.IsBlocked(t.Context(), waiterA.ID)
	if err != nil {
		t.Fatalf("IsBlocked A: %v", err)
	}
	if blockedA {
		t.Error("blocker in its board's terminal stage should not block")
	}
	blockedB, _, err := store.IsBlocked(t.Context(), waiterB.ID)
	if err != nil {
		t.Fatalf("IsBlocked B: %v", err)
	}
	if !blockedB {
		t.Error("blocker in a non-terminal stage should still block")
	}
}

func TestTaskOnMissingBoardFallsBackToDefault(t *testing.T) {
	d, store := newBoardsTestDispatcher(t)
	orphan := mustCreateOn(t, store, "deleted_board", "work")

	d.autoAdvanceStage(context.Background(), orphan, orphan.ID, "work", 0, "")
	after, _ := store.Get(t.Context(), orphan.ID)
	if after.Stage != "out" {
		t.Errorf("stage = %q, want the default board's semantics to apply", after.Stage)
	}
}

func TestCountByStageSeparatesBoards(t *testing.T) {
	_, store := newBoardsTestDispatcher(t)
	mustCreateOn(t, store, "fast", "work")
	mustCreateOn(t, store, "fast", "work")
	mustCreateOn(t, store, "slow", "work")

	counts, err := store.CountByStage(t.Context())
	if err != nil {
		t.Fatalf("CountByStage: %v", err)
	}
	got := map[string]int{}
	for _, c := range counts {
		got[c.BoardID+"/"+c.Stage] = c.Count
	}
	if got["fast/work"] != 2 || got["slow/work"] != 1 {
		t.Errorf("census = %+v, want fast/work=2 slow/work=1", got)
	}
}

func TestAllInGroupDoneAcrossBoards(t *testing.T) {
	_, store := newBoardsTestDispatcher(t)
	parent := mustCreateOn(t, store, "fast", "out")
	child := &Task{WorkspaceID: testWS, Title: "child", BoardID: "slow", Stage: "work", ParentID: parent.ID}
	if err := store.Create(t.Context(), child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	done, err := store.AllInGroupDone(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("AllInGroupDone: %v", err)
	}
	if done {
		t.Error("group counted as done while the subtask is mid-flow on its own board")
	}

	out := Stage("out")
	if _, err := store.Update(t.Context(), child.ID, Patch{Stage: &out}); err != nil {
		t.Fatalf("move child: %v", err)
	}
	done, err = store.AllInGroupDone(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("AllInGroupDone: %v", err)
	}
	if !done {
		t.Error("group should be done once every member is terminal on its own board")
	}
}

func TestListByBoardAndStage(t *testing.T) {
	_, store := newBoardsTestDispatcher(t)
	mustCreateOn(t, store, "fast", "work")
	mustCreateOn(t, store, "slow", "work")
	mustCreateOn(t, store, "slow", "intake")

	fast, err := store.ListByBoard(t.Context(), "fast")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(fast) != 1 {
		t.Errorf("ListByBoard(fast) = %d tasks, want 1", len(fast))
	}
	inColumn, err := store.ListInBoardStage(t.Context(), "slow", "work")
	if err != nil {
		t.Fatalf("ListInBoardStage: %v", err)
	}
	if len(inColumn) != 1 || inColumn[0].BoardID != "slow" {
		t.Errorf("ListInBoardStage(slow, work) = %+v, want exactly the slow task", inColumn)
	}
}

func TestBuildStageEnv_CarriesBoardID(t *testing.T) {
	at := &types.AgentType{Name: "dev", ValidStages: []types.Stage{types.StageInProgress}}

	onBoard := buildStageEnv(
		&Task{WorkspaceID: testWS, ID: "abc123", Stage: types.StageInProgress, BoardID: "acme"},
		types.StageInProgress, "dev", "dev-1", defaultAgentBaseURL, at, "",
	)
	if onBoard["BOARD_ID"] != "acme" {
		t.Errorf("BOARD_ID = %q, want acme", onBoard["BOARD_ID"])
	}

	bare := buildStageEnv(
		&Task{WorkspaceID: testWS, ID: "abc123", Stage: types.StageInProgress},
		types.StageInProgress, "dev", "dev-1", defaultAgentBaseURL, at, "",
	)
	if _, ok := bare["BOARD_ID"]; ok {
		t.Errorf("BOARD_ID exported for a task with no board: %q", bare["BOARD_ID"])
	}
}

func TestResolveSkillsMode(t *testing.T) {
	for _, tc := range []struct {
		name      string
		declared  string
		engineCan bool
		want      string
	}{
		{"auto on a discovering engine", types.SkillsModeAuto, true, types.SkillsModeNative},
		{"auto on an engine without discovery", types.SkillsModeAuto, false, types.SkillsModeInline},
		{"empty behaves as auto", "", true, types.SkillsModeNative},
		{"garbage behaves as auto", "sure-why-not", false, types.SkillsModeInline},
		{"explicit inline wins over a capable engine", types.SkillsModeInline, true, types.SkillsModeInline},
		{"explicit native is honoured even unsupported", types.SkillsModeNative, false, types.SkillsModeNative},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSkillsMode(tc.declared, tc.engineCan); got != tc.want {
				t.Errorf("resolveSkillsMode(%q, %v) = %q, want %q", tc.declared, tc.engineCan, got, tc.want)
			}
		})
	}
}
