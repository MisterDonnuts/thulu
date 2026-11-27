package task

import (
	"testing"

	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestMissingAgentFollowsTheTaskType(t *testing.T) {
	reg := stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "build", Label: "Build", Position: 10, Initial: true},
		{ID: "verify", Label: "Verify", Position: 20, DefaultAgent: "checker"},
		{ID: "ship", Label: "Ship", Position: 30},
		{ID: "shipped", Label: "Shipped", Position: 40, Terminal: true},
	})
	// The task types give the ship column different agents: a release task gets shipper, a
	// feature none; verify has a column default a chore clears.
	boards := &fakeBoards{def: "board", regs: map[string]stagepkg.Registry{"board": reg},
		types: map[string][]stagepkg.TaskTypeInfo{"board": {
			{ID: "feature", Label: "Feature", Default: true, Agents: map[types.Stage]string{"ship": ""}},
			{ID: "chore", Label: "Chore", Agents: map[types.Stage]string{"verify": ""}},
			{ID: "bug", Label: "Bug"},
			{ID: "release", Label: "Release", Skips: []types.Stage{"verify"}, Agents: map[types.Stage]string{"ship": "shipper"}},
		}}}
	d, store := newStageTestDispatcher(t, reg)
	d.WithBoards(boards)
	store.WithBoards(boards)
	ctx := t.Context()

	shipping := &Task{WorkspaceID: testWS, Title: "release", BoardID: "board", TypeID: "release",
		StageAssignments: map[Stage]string{"ship": "shipper"}}
	if err := store.Create(ctx, shipping); err != nil {
		t.Fatal(err)
	}
	ship, _ := reg.Get("ship")
	verify, _ := reg.Get("verify")
	cases := []struct {
		name string
		typ  string
		info types.StageInfo
		want bool
	}{
		{"a feature in ship, where another type has an agent", "feature", ship, false},
		{"a release task in ship without its agent", "release", ship, true},
		{"a bug in ship, which only release tasks fill", "bug", ship, false},
		{"a task in a column with a default agent", "", verify, true},
		{"a chore whose type clears the column's agent", "chore", verify, false},
	}
	for _, c := range cases {
		tk := &Task{WorkspaceID: testWS, Title: c.name, BoardID: "board", TypeID: c.typ}
		if err := store.Create(ctx, tk); err != nil {
			t.Fatal(err)
		}
		if got := d.missingAgent(ctx, tk, c.info); got != c.want {
			t.Errorf("%s: missingAgent = %v, want %v", c.name, got, c.want)
		}
	}

	if has, _ := store.BoardStageHasAgent(ctx, testWS, shipping.BoardID, "ship", "feature", ""); has {
		t.Error("the type filter let a release task's agent answer for features")
	}
	if has, _ := store.BoardStageHasAgent(ctx, testWS, shipping.BoardID, "ship", "release", ""); !has {
		t.Error("the type filter hid a release task's agent from release tasks")
	}
}
