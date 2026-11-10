package stage

import (
	"sort"

	"github.com/MisterDonnuts/thulu/internal/types"
)

// Implementations must be safe for concurrent use.
type Registry interface {
	List() []types.StageInfo
	ListWithVirtual() []types.StageInfo
	Get(id types.Stage) (types.StageInfo, bool)
	Valid(id types.Stage) bool
	Next(id types.Stage) types.Stage
	Initial() types.Stage
	Terminals() []types.Stage
	KickbackDefault() types.Stage
}

// Immutable, so it is shared across goroutines without locking; Store swaps in a new one on every
// write.
type snapshot struct {
	board     []types.StageInfo
	byID      map[types.Stage]types.StageInfo
	initial   types.Stage
	terminals []types.Stage
	kickback  types.Stage
}

func NewSnapshot(board []types.StageInfo) Registry {
	s := &snapshot{
		board: make([]types.StageInfo, len(board)),
		byID:  make(map[types.Stage]types.StageInfo, len(board)+2),
	}
	copy(s.board, board)
	sort.SliceStable(s.board, func(i, j int) bool { return s.board[i].Position < s.board[j].Position })
	for i := range s.board {
		st := &s.board[i]
		s.byID[st.ID] = *st
		if st.Initial && s.initial == "" {
			s.initial = st.ID
		}
		if st.Terminal {
			s.terminals = append(s.terminals, st.ID)
		}
		if s.kickback == "" && !st.Initial && !st.Terminal {
			s.kickback = st.ID
		}
	}
	// Rows that bypassed validation must still answer Initial() and KickbackDefault() with a real
	// stage, never "".
	if s.initial == "" && len(s.board) > 0 {
		s.initial = s.board[0].ID
	}
	if s.kickback == "" {
		s.kickback = s.initial
	}
	virtuals := Virtuals()
	for i := range virtuals {
		s.byID[virtuals[i].ID] = virtuals[i]
	}
	return s
}

func Static() Registry { return NewSnapshot(Defaults()) }

func (s *snapshot) List() []types.StageInfo {
	out := make([]types.StageInfo, len(s.board))
	copy(out, s.board)
	return out
}

func (s *snapshot) ListWithVirtual() []types.StageInfo {
	return append(s.List(), Virtuals()...)
}

func (s *snapshot) Get(id types.Stage) (types.StageInfo, bool) {
	if info, ok := s.byID[id]; ok {
		return info, true
	}
	return types.StageInfo{ID: id, Label: string(id)}, false
}

func (s *snapshot) Valid(id types.Stage) bool {
	_, ok := s.byID[id]
	return ok
}

func (s *snapshot) Next(id types.Stage) types.Stage {
	for i := range s.board {
		if s.board[i].ID != id {
			continue
		}
		if s.board[i].Terminal || i+1 >= len(s.board) {
			return ""
		}
		return s.board[i+1].ID
	}
	return ""
}

func (s *snapshot) Initial() types.Stage { return s.initial }

func (s *snapshot) Terminals() []types.Stage {
	out := make([]types.Stage, len(s.terminals))
	copy(out, s.terminals)
	return out
}

func (s *snapshot) KickbackDefault() types.Stage { return s.kickback }
