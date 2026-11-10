package task

import (
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestNotTerminal_UnknownBoardResolvesToDefaultBoard(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(twoBoards())

	ghost := &Task{WorkspaceID: testWS, Title: "orphaned", BoardID: "deleted-board", Stage: "out"}
	if err := s.Create(t.Context(), ghost); err != nil {
		t.Fatalf("Create ghost: %v", err)
	}
	dependent := &Task{WorkspaceID: testWS, Title: "waiting", BoardID: "fast", Stage: "intake"}
	if err := s.Create(t.Context(), dependent); err != nil {
		t.Fatalf("Create dependent: %v", err)
	}
	if err := s.AddDependency(t.Context(), dependent.ID, ghost.ID, DepHard); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	blocked, blockers, err := s.IsBlocked(t.Context(), dependent.ID)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if blocked {
		t.Errorf("dependent is blocked by a task sitting in the default board's terminal stage; blockers=%v", blockers)
	}
}

func TestNotTerminal_UnknownBoardNonTerminalStageStillBlocks(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(twoBoards())

	ghost := &Task{WorkspaceID: testWS, Title: "orphaned mid-flow", BoardID: "deleted-board", Stage: "work"}
	if err := s.Create(t.Context(), ghost); err != nil {
		t.Fatalf("Create ghost: %v", err)
	}
	dependent := &Task{WorkspaceID: testWS, Title: "waiting", BoardID: "fast", Stage: "intake"}
	if err := s.Create(t.Context(), dependent); err != nil {
		t.Fatalf("Create dependent: %v", err)
	}
	if err := s.AddDependency(t.Context(), dependent.ID, ghost.ID, DepHard); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	blocked, _, err := s.IsBlocked(t.Context(), dependent.ID)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked {
		t.Error("a ghost-board task in a non-terminal column must still block its dependents")
	}
}

func TestNotTerminal_KnownBoardsKeepTheirOwnTerminals(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(twoBoards())

	onSlow := &Task{WorkspaceID: testWS, Title: "in review", BoardID: "slow", Stage: "review"}
	if err := s.Create(t.Context(), onSlow); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dependent := &Task{WorkspaceID: testWS, Title: "waiting", BoardID: "fast", Stage: "intake"}
	if err := s.Create(t.Context(), dependent); err != nil {
		t.Fatalf("Create dependent: %v", err)
	}
	if err := s.AddDependency(t.Context(), dependent.ID, onSlow.ID, DepHard); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	blocked, _, err := s.IsBlocked(t.Context(), dependent.ID)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked {
		t.Error("`review` is not terminal on `slow`; the dependent must stay blocked")
	}
}

func TestAllInGroupDone_UnknownBoardRoot(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(twoBoards())

	root := &Task{WorkspaceID: testWS, Title: "root", BoardID: "deleted-board", Stage: "out"}
	if err := s.Create(t.Context(), root); err != nil {
		t.Fatalf("Create root: %v", err)
	}
	child := &Task{WorkspaceID: testWS, Title: "child", ParentID: root.ID, BoardID: "deleted-board", Stage: "out"}
	if err := s.Create(t.Context(), child); err != nil {
		t.Fatalf("Create child: %v", err)
	}

	done, err := s.AllInGroupDone(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("AllInGroupDone: %v", err)
	}
	if !done {
		t.Error("group whose members all sit in a terminal column is not reported done — the sandbox would never be released")
	}
}

func TestAllInGroupDone_UnknownBoardChildStillRunning(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(twoBoards())

	root := &Task{WorkspaceID: testWS, Title: "root", BoardID: "deleted-board", Stage: "out"}
	if err := s.Create(t.Context(), root); err != nil {
		t.Fatalf("Create root: %v", err)
	}
	child := &Task{WorkspaceID: testWS, Title: "child", ParentID: root.ID, BoardID: "deleted-board", Stage: "work"}
	if err := s.Create(t.Context(), child); err != nil {
		t.Fatalf("Create child: %v", err)
	}

	done, err := s.AllInGroupDone(t.Context(), root.ID)
	if err != nil {
		t.Fatalf("AllInGroupDone: %v", err)
	}
	if done {
		t.Error("a child still mid-flow must keep the group open")
	}
}

func TestNotTerminal_BoardWithoutTerminalStage(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(&fakeBoards{
		def: "endless",
		regs: map[string]stagepkg.Registry{
			"endless": stagepkg.NewSnapshot([]types.StageInfo{
				{ID: "intake", Label: "Intake", Position: 10, Initial: true},
				{ID: "work", Label: "Work", Position: 20},
			}),
		},
	})

	tk := &Task{WorkspaceID: testWS, Title: "t", BoardID: "endless", Stage: "work"}
	if err := s.Create(t.Context(), tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	done, err := s.AllInGroupDone(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("AllInGroupDone on a terminal-less board: %v", err)
	}
	if done {
		t.Error("a board with no terminal column can never report its tasks done")
	}

	ghost := &Task{WorkspaceID: testWS, Title: "ghost", BoardID: "deleted", Stage: "work"}
	if err := s.Create(t.Context(), ghost); err != nil {
		t.Fatalf("Create ghost: %v", err)
	}
	if done, err = s.AllInGroupDone(t.Context(), ghost.ID); err != nil {
		t.Fatalf("AllInGroupDone ghost: %v", err)
	}
	if done {
		t.Error("ghost-board task must not read as done when the default board has no terminal")
	}
}
