package task

import (
	"fmt"
	"testing"
)

func TestDecorate_BlockedAndChildCountsOnAFilteredList(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(twoBoards())

	for i := range 5 {
		noise := &Task{WorkspaceID: testWS, Title: fmt.Sprintf("noise %d", i), BoardID: "slow", Stage: "intake"}
		if err := s.Create(t.Context(), noise); err != nil {
			t.Fatalf("create noise: %v", err)
		}
	}

	blocker := &Task{WorkspaceID: testWS, Title: "blocker", BoardID: "fast", Stage: "intake"}
	blocked := &Task{WorkspaceID: testWS, Title: "blocked", BoardID: "fast", Stage: "intake"}
	parent := &Task{WorkspaceID: testWS, Title: "parent", BoardID: "fast", Stage: "intake"}
	for _, tk := range []*Task{blocker, blocked, parent} {
		if err := s.Create(t.Context(), tk); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	for i := range 2 {
		child := &Task{WorkspaceID: testWS, Title: fmt.Sprintf("child %d", i), ParentID: parent.ID, BoardID: "fast", Stage: "intake"}
		if err := s.Create(t.Context(), child); err != nil {
			t.Fatalf("create child: %v", err)
		}
	}
	if err := s.AddDependency(t.Context(), blocked.ID, blocker.ID, DepHard); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	rows, err := s.ListByBoard(t.Context(), "fast")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	byID := map[string]Task{}
	for i := range rows {
		byID[rows[i].ID] = rows[i]
	}

	if got := byID[blocked.ID]; !got.IsBlocked || got.BlockedByCount != 1 {
		t.Errorf("blocked task: IsBlocked=%v count=%d, want true/1", got.IsBlocked, got.BlockedByCount)
	}
	if got := byID[blocker.ID]; got.IsBlocked {
		t.Error("the blocker itself must not be marked blocked")
	}
	if got := byID[parent.ID]; got.ChildCount != 2 {
		t.Errorf("parent ChildCount = %d, want 2", got.ChildCount)
	}
	if got := byID[blocker.ID]; got.ChildCount != 0 {
		t.Errorf("childless task ChildCount = %d, want 0", got.ChildCount)
	}
}

func TestDecorate_EmptyListDoesNoWork(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.ListInBoardStage(t.Context(), "nothing", "nowhere")
	if err != nil {
		t.Fatalf("ListInBoardStage: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want none", len(rows))
	}
}

func TestDecorate_ScopeDoesNotChangeAnswers(t *testing.T) {
	s := newTestStore(t)
	s.WithBoards(twoBoards())

	blockerElsewhere := &Task{WorkspaceID: testWS, Title: "blocker on slow", BoardID: "slow", Stage: "intake"}
	if err := s.Create(t.Context(), blockerElsewhere); err != nil {
		t.Fatalf("create: %v", err)
	}
	waiting := &Task{WorkspaceID: testWS, Title: "waiting on fast", BoardID: "fast", Stage: "intake"}
	if err := s.Create(t.Context(), waiting); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AddDependency(t.Context(), waiting.ID, blockerElsewhere.ID, DepHard); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	rows, err := s.ListByBoard(t.Context(), "fast")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if !rows[0].IsBlocked || rows[0].BlockedByCount != 1 {
		t.Errorf("a blocker on another board still blocks: IsBlocked=%v count=%d",
			rows[0].IsBlocked, rows[0].BlockedByCount)
	}
}

func TestCountByType_CoversEveryPopulation(t *testing.T) {
	s := newTestStore(t)
	mk := func(board, typeID string, n int) {
		for i := range n {
			tk := &Task{WorkspaceID: testWS, Title: fmt.Sprintf("%s/%s/%d", board, typeID, i), BoardID: board, TypeID: typeID}
			if err := s.Create(t.Context(), tk); err != nil {
				t.Fatalf("create: %v", err)
			}
		}
	}
	mk("default", "bugfix", 3)
	mk("default", "", 2)
	mk("other", "bugfix", 1)

	counts, err := s.CountByType(t.Context())
	if err != nil {
		t.Fatalf("CountByType: %v", err)
	}
	got := map[string]int{}
	for _, c := range counts {
		got[c.BoardID+"/"+c.TypeID] = c.Count
	}
	for key, want := range map[string]int{
		"default/bugfix": 3,
		"default/":       2,
		"other/bugfix":   1,
	} {
		if got[key] != want {
			t.Errorf("count[%q] = %d, want %d (all: %v)", key, got[key], want, got)
		}
	}
}

func TestCountByType_EmptyStore(t *testing.T) {
	s := newTestStore(t)
	counts, err := s.CountByType(t.Context())
	if err != nil {
		t.Fatalf("CountByType: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("counts = %v, want none", counts)
	}
}
