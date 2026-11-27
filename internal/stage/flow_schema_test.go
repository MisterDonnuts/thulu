package stage

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

// A board shaped like the Thulu flow, trimmed to the stages the new flags need.
func flowBoard() []types.StageInfo {
	return []types.StageInfo{
		{ID: types.StageBacklog, Label: "Backlog", Position: 10, Initial: true},
		{ID: types.StageRefinement, Label: "Refinement", Position: 20, AgentVerdict: true},
		{ID: "test_design", Label: "Test design", Position: 30, LockTests: true, PushBranch: true},
		{ID: types.StageInProgress, Label: "In progress", Position: 40, WipGroup: types.WipGroupWork,
			PRDraft: true, PushBranch: true, DefaultAgent: "coder"},
		{ID: "testing", Label: "Testing", Position: 50, SkipUnless: []string{"ui", "api"}, AgentVerdict: true},
		{ID: types.StageReview, Label: "Review", Position: 60, PRReady: true, MergeTarget: true,
			ChangesKickBackTo: types.StageInProgress, NoSkip: true},
		{ID: "release", Label: "Release", Position: 70, ReleaseResources: true},
		{ID: types.StageDone, Label: "Done", Position: 80, Terminal: true},
	}
}

func TestValidateFlowBoardAccepted(t *testing.T) {
	if err := ValidateBoard(flowBoard()); err != nil {
		t.Fatalf("flow board must validate: %v", err)
	}
	b := flowBoard()
	b[5].RequiredApprovals = []types.ApprovalRequirement{{Key: "merge", Label: "Merge", RequiredChecks: true}}
	if err := ValidateBoard(b); err != nil {
		t.Fatalf("required checks after the PR opens must validate: %v", err)
	}
}

func TestValidateFlowFlagsMatrix(t *testing.T) {
	at := func(b []types.StageInfo, id types.Stage) *types.StageInfo {
		for i := range b {
			if b[i].ID == id {
				return &b[i]
			}
		}
		t.Fatalf("no stage %q", id)
		return nil
	}
	cases := []struct {
		name    string
		mutate  func([]types.StageInfo)
		wantErr string
	}{
		{"verdict on initial", func(b []types.StageInfo) { at(b, types.StageBacklog).AgentVerdict = true }, "first or last stage"},
		{"lock tests on terminal", func(b []types.StageInfo) { at(b, types.StageDone).LockTests = true }, "first or last stage"},
		{"merge target on initial", func(b []types.StageInfo) { at(b, types.StageBacklog).MergeTarget = true }, "first or last stage"},
		{"pr ready on initial", func(b []types.StageInfo) { at(b, types.StageBacklog).PRReady = true }, "first or last stage"},
		{"bad default agent", func(b []types.StageInfo) { at(b, types.StageInProgress).DefaultAgent = "Coder!" }, "not a valid agent name"},
		{"bad skip value", func(b []types.StageInfo) { at(b, "testing").SkipUnless = []string{"UI"} }, "skip_unless value"},
		{"duplicate skip value", func(b []types.StageInfo) { at(b, "testing").SkipUnless = []string{"ui", "ui"} }, "twice"},
		{"too many skip values", func(b []types.StageInfo) {
			at(b, "testing").SkipUnless = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		}, "max 8"},
		{"no_skip with skip_unless", func(b []types.StageInfo) { at(b, "testing").NoSkip = true }, "no_skip"},
		{"skippable with approvals", func(b []types.StageInfo) {
			at(b, "testing").RequiredApprovals = []types.ApprovalRequirement{{Key: "qa", Label: "QA"}}
		}, "required_approvals"},
		{"skippable merge target", func(b []types.StageInfo) {
			at(b, types.StageReview).MergeTarget = false
			at(b, "testing").MergeTarget = true
		}, "merge_target"},
		{"two merge targets", func(b []types.StageInfo) { at(b, "release").MergeTarget = true }, "at most one"},
		{"merge target before any PR", func(b []types.StageInfo) {
			at(b, types.StageInProgress).PRDraft = false
		}, "no stage before it opens a pull request"},
		{"push after release", func(b []types.StageInfo) {
			b = append(b[:7], types.StageInfo{ID: "late", Label: "Late", Position: 75, PushBranch: true}, b[7])
			if err := ValidateBoard(b); err == nil || !strings.Contains(err.Error(), "already freed") {
				t.Errorf("push after release_resources: err = %v", err)
			}
		}, ""},
		{"release with kick back", func(b []types.StageInfo) {
			at(b, "release").ChangesKickBackTo = types.StageInProgress
		}, "cannot send changes back"},
		{"release kicks back on changes requested", func(b []types.StageInfo) {
			at(b, "release").OnChangesRequested = types.ChangesKickBack
		}, "cannot send changes back"},
		{"required checks before any PR", func(b []types.StageInfo) {
			at(b, types.StageRefinement).RequiredApprovals = []types.ApprovalRequirement{{Key: "merge", Label: "Merge", RequiredChecks: true}}
		}, "required checks"},
		{"kick back to skippable", func(b []types.StageInfo) {
			at(b, types.StageReview).ChangesKickBackTo = "testing"
		}, "may be skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := flowBoard()
			c.mutate(b)
			if c.wantErr == "" {
				return
			}
			err := ValidateBoard(b)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, c.wantErr)
			}
		})
	}
}

func TestValidateBoardInfoFlowSettings(t *testing.T) {
	ok := BoardInfo{ID: "flow", Label: "Flow", WorkspaceID: FallbackWorkspaceID,
		MaxKickBacks: 5, Flow: &BoardFlow{Name: "thulu", Revision: 3, Hash: "ab12"}}
	if err := ValidateBoardInfo(ok); err != nil {
		t.Fatalf("valid flow settings rejected: %v", err)
	}
	for name, mutate := range map[string]func(*BoardInfo){
		"negative cap":  func(b *BoardInfo) { b.MaxKickBacks = -1 },
		"cap too high":  func(b *BoardInfo) { b.MaxKickBacks = MaxKickBacksLimit + 1 },
		"bad flow name": func(b *BoardInfo) { b.Flow = &BoardFlow{Name: "Thulu Flow"} },
		"empty flow":    func(b *BoardInfo) { b.Flow = &BoardFlow{} },
		"bad revision":  func(b *BoardInfo) { b.Flow = &BoardFlow{Name: "thulu", Revision: -2} },
		"bad hash":      func(b *BoardInfo) { b.Flow = &BoardFlow{Name: "thulu", Hash: "XYZ"} },
	} {
		b := ok
		mutate(&b)
		if err := ValidateBoardInfo(b); err == nil {
			t.Errorf("%s: accepted %+v", name, b)
		}
	}
}

func TestFlowFlagsRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	info := BoardInfo{ID: "flow", Label: "Flow", WorkspaceID: FallbackWorkspaceID,
		MaxKickBacks: 4, Flow: &BoardFlow{Name: "thulu", Revision: 2, Hash: "deadbeef"}}
	if err := store.CreateBoard(ctx, info, flowBoard()); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	reopened, err := NewStore(store.db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got := reopened.Board("flow").List()
	want := flowBoard()
	if len(got) != len(want) {
		t.Fatalf("stages = %d, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.AgentVerdict != w.AgentVerdict || g.LockTests != w.LockTests ||
			!reflect.DeepEqual(g.SkipUnless, w.SkipUnless) || g.ReleaseResources != w.ReleaseResources ||
			g.MergeTarget != w.MergeTarget || g.PRDraft != w.PRDraft || g.PRReady != w.PRReady ||
			g.DefaultAgent != w.DefaultAgent || g.NoSkip != w.NoSkip {
			t.Errorf("stage %s round trip:\n got  %+v\n want %+v", w.ID, g, w)
		}
	}
	bi, ok := BoardInfoOf(reopened, "flow")
	if !ok {
		t.Fatal("board flow missing after reload")
	}
	if bi.MaxKickBacks != 4 || !reflect.DeepEqual(bi.Flow, info.Flow) {
		t.Errorf("board settings round trip = %+v", bi)
	}

	bi.MaxKickBacks = 0
	if err := reopened.UpdateBoard(ctx, bi); err != nil {
		t.Fatalf("UpdateBoard: %v", err)
	}
	if bi, _ = BoardInfoOf(reopened, "flow"); bi.MaxKickBacks != 0 || bi.Flow == nil || bi.Flow.Name != "thulu" {
		t.Errorf("after UpdateBoard = %+v", bi)
	}

	// default_agent is per board: the catalog row carries the flags, not the agent.
	if def, _ := reopened.Board(DefaultBoardID).Get(types.StageInProgress); def.DefaultAgent != "" {
		t.Errorf("default board picked up flow's default agent %q", def.DefaultAgent)
	}
}

func TestTypeAgentsRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateBoard(ctx, BoardInfo{ID: "flow", Label: "Flow", WorkspaceID: FallbackWorkspaceID}, flowBoard()); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	tt := []TaskTypeInfo{{
		ID: "bug", Label: "Bug", WorkspaceID: FallbackWorkspaceID,
		Skips:  []types.Stage{"test_design"},
		Agents: map[types.Stage]string{types.StageInProgress: "fixer", types.StageReview: "reviewer"},
	}}
	if err := store.SaveBoardTypes(ctx, "flow", tt); err != nil {
		t.Fatalf("SaveBoardTypes: %v", err)
	}
	reopened, err := NewStore(store.db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got := reopened.BoardTypes("flow")
	if len(got) != 1 || !reflect.DeepEqual(got[0].Agents, tt[0].Agents) {
		t.Fatalf("type agents round trip = %+v", got)
	}

	// A stage leaving the board takes its type agents with it.
	trimmed := flowBoard()
	for i := range trimmed {
		if trimmed[i].ID == types.StageReview {
			trimmed = append(trimmed[:i], trimmed[i+1:]...)
			break
		}
	}
	if err := reopened.SaveBoard(ctx, "flow", trimmed); err != nil {
		t.Fatalf("SaveBoard: %v", err)
	}
	got = reopened.BoardTypes("flow")
	if want := map[types.Stage]string{types.StageInProgress: "fixer"}; !reflect.DeepEqual(got[0].Agents, want) {
		t.Errorf("after removing review, agents = %v, want %v", got[0].Agents, want)
	}
}

func TestValidateTypeAgents(t *testing.T) {
	board := flowBoard()
	for name, c := range map[string]struct {
		tt      TaskTypeInfo
		wantErr string
	}{
		"unknown stage": {TaskTypeInfo{ID: "bug", Label: "Bug", Agents: map[types.Stage]string{"nowhere": "x"}}, "not on the board"},
		"skipped stage": {TaskTypeInfo{ID: "bug", Label: "Bug", Skips: []types.Stage{"testing"},
			Agents: map[types.Stage]string{"testing": "qa"}}, "a stage it skips"},
		"edge stage": {TaskTypeInfo{ID: "bug", Label: "Bug", Agents: map[types.Stage]string{types.StageBacklog: "x"}}, "first or last stage"},
		"bad name":   {TaskTypeInfo{ID: "bug", Label: "Bug", Agents: map[types.Stage]string{types.StageReview: "Bad Name"}}, "not a valid agent name"},
	} {
		err := ValidateBoardTypes(board, []TaskTypeInfo{c.tt})
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", name, err, c.wantErr)
		}
	}
}
