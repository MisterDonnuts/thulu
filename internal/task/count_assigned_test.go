package task

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestCountAssignedTo(t *testing.T) {
	store := newTestStore(t)
	for _, tk := range []*Task{
		{WorkspaceID: testWS, Title: "a", StageAssignments: map[Stage]string{types.StageRefinement: "backend", types.StageReview: "reviewer"}},
		{WorkspaceID: testWS, Title: "b", StageAssignments: map[Stage]string{types.StageInProgress: "backend"}},
		{WorkspaceID: testWS, Title: "c", StageAssignments: map[Stage]string{types.StageInProgress: "backend-v2"}},
		{WorkspaceID: "ws-other", Title: "d", StageAssignments: map[Stage]string{types.StageInProgress: "backend"}},
	} {
		if err := store.Create(t.Context(), tk); err != nil {
			t.Fatalf("create %s: %v", tk.Title, err)
		}
	}
	for _, c := range []struct {
		wid   string
		all   bool
		agent string
		want  int
	}{
		{testWS, false, "backend", 2},
		{testWS, false, "reviewer", 1},
		{testWS, false, "nobody", 0},
		{"ws-other", false, "backend", 1},
		{"", true, "backend", 3},
	} {
		got, err := store.CountAssignedTo(t.Context(), c.wid, c.all, c.agent)
		if err != nil {
			t.Fatalf("count %+v: %v", c, err)
		}
		if got != c.want {
			t.Errorf("CountAssignedTo(%q, %v, %q) = %d, want %d", c.wid, c.all, c.agent, got, c.want)
		}
	}
}
