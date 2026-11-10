package stage

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestStore_IsABoardsNotARegistry(t *testing.T) {
	var b Boards = &Store{}
	if _, ok := b.(Registry); ok {
		t.Error("*Store implements Registry again — a Registry-typed field can now " +
			"silently answer for the default board when asked about a task on another one")
	}
}

func TestSingleBoard_IsABoards(t *testing.T) {
	var _ Boards = SingleBoard(Static())
}

func TestStore_DefaultBoardFollowsTheDefaultMarker(t *testing.T) {
	s := &Store{state: &boardState{
		defaultID: "a",
		byID: map[string]Registry{
			"a": NewSnapshot([]types.StageInfo{
				{ID: "a_in", Label: "In", Position: 10, Initial: true},
				{ID: "a_out", Label: "Out", Position: 20, Terminal: true},
			}),
			"b": NewSnapshot([]types.StageInfo{
				{ID: "b_in", Label: "In", Position: 10, Initial: true},
				{ID: "b_out", Label: "Out", Position: 20, Terminal: true},
			}),
		},
	}}

	if got := s.DefaultBoard().Initial(); got != "a_in" {
		t.Errorf("DefaultBoard().Initial() = %q, want a_in", got)
	}
	s.state.defaultID = "b"
	if got := s.DefaultBoard().Initial(); got != "b_in" {
		t.Errorf("after the default moved, Initial() = %q, want b_in", got)
	}
}

func TestStore_DefaultBoardNeverNil(t *testing.T) {
	s := &Store{state: &boardState{defaultID: "gone", byID: map[string]Registry{}}}
	if s.DefaultBoard() == nil {
		t.Fatal("DefaultBoard() returned nil")
	}
	if got := s.DefaultBoard().Initial(); got == "" {
		t.Error("DefaultBoard().Initial() is empty — a task created here would land nowhere")
	}
}
