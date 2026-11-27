package api

import (
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
)

// GET /boards says whether a flow board changed since the Workshop wrote it, so the board card can
// show Conflict instead of Update.
func TestIntegration_BoardListMarksAnEditedFlowBoard(t *testing.T) {
	t.Parallel()
	srv, h, token := newFlowServer(t)
	applyThulu(t, h, token)

	flowOf := func() *stagepkg.BoardFlow {
		t.Helper()
		for _, b := range decodeBoards(t, getJSON(t, h, "/boards", token)).Boards {
			if b.ID == "thulu" {
				return b.Flow
			}
		}
		t.Fatal("no thulu board")
		return nil
	}
	if f := flowOf(); f == nil || f.Edited {
		t.Fatalf("fresh flow board = %+v, want it unedited", f)
	}

	board := srv.boards.Board("thulu").List()
	board[3].PushBranch = false
	if err := srv.stageAdmin.SaveBoard(t.Context(), "thulu", board); err != nil {
		t.Fatal(err)
	}
	if f := flowOf(); !f.Edited {
		t.Errorf("flow board after a column change = %+v, want edited", f)
	}
	if info, _ := stagepkg.BoardInfoOf(srv.stageAdmin, "thulu"); info.Flow.Edited {
		t.Error("GET /boards wrote edited into the store's board")
	}
}
