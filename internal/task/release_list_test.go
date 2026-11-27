package task

import (
	"slices"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/listpage"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A release agent lists one column of its board for one project. Board, stage and project all
// narrow the SQL, and the workspace still bounds it.
func TestListPage_FiltersByBoardStageAndProject(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	mk := func(ws, title string, stage Stage, projects ...string) *Task {
		t.Helper()
		tk := &Task{WorkspaceID: ws, Title: title, ProjectIDs: projects}
		if err := s.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if stage != "" {
			if _, err := s.Update(ctx, tk.ID, Patch{Stage: &stage}); err != nil {
				t.Fatal(err)
			}
		}
		return tk
	}
	want := mk(testWS, "shop, in progress", types.StageInProgress, "shop")
	mk(testWS, "shop, backlog", "", "shop")
	mk(testWS, "api, in progress", types.StageInProgress, "api")
	both := mk(testWS, "both, in progress", types.StageInProgress, "api", "shop")
	mk("other-ws", "shop elsewhere", types.StageInProgress, "shop")

	f := CardFilter{Workspace: testWS, Board: want.BoardID, Stage: string(types.StageInProgress), Project: "shop", TopLevel: true}
	rows, _, err := s.ListPage(ctx, f, listpage.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range rows {
		got = append(got, rows[i].ID)
	}
	slices.Sort(got)
	exp := []string{want.ID, both.ID}
	slices.Sort(exp)
	if !slices.Equal(got, exp) {
		t.Fatalf("board+stage+project = %v, want %v", got, exp)
	}

	// The stage narrows a board listing too; before, a board made it ignore the stage.
	f = CardFilter{Workspace: testWS, Board: want.BoardID, Stage: string(types.StageBacklog), TopLevel: true}
	if rows, _, err = s.ListPage(ctx, f, listpage.Request{}); err != nil || len(rows) != 1 || rows[0].Title != "shop, backlog" {
		t.Fatalf("board+stage = %d rows, err %v", len(rows), err)
	}
}

func TestJumpedRequirements(t *testing.T) {
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true},
		{ID: "review", Label: "Review", Position: 20,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "merge", Label: "Merge"}}},
		{ID: "design", Label: "Design", Position: 30,
			RequiredApprovals: []types.ApprovalRequirement{{Key: "ux", Label: "UX", TaskTypes: []string{"feature"}}}},
		{ID: "release", Label: "Release", Position: 40},
		{ID: "done", Label: "Done", Position: 50, Terminal: true},
	})
	cases := []struct {
		name     string
		typeID   string
		from, to Stage
		want     []string
	}{
		{"adjacent", "", "release", "done", nil},
		{"over review", "bug", "build", "release", []string{"Merge"}},
		{"type-scoped requirement applies", "feature", "build", "release", []string{"Merge", "UX"}},
		{"from's own approvals are the gate's", "", "review", "design", nil},
		{"unknown stage", "", "build", "nowhere", nil},
	}
	for _, c := range cases {
		if got := JumpedRequirements(reg, c.typeID, c.from, c.to); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
