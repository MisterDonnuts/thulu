package workshop

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func flowStage(id string, extra map[string]any) map[string]any {
	st := map[string]any{"id": id}
	for k, v := range extra {
		st[k] = v
	}
	return st
}

// A small flow on top of catalogFS: writer runs in_progress, shipper runs ship only for release
// tasks, which skip in_progress.
func flowFS(t *testing.T, flowRev int, stages []map[string]any) fstest.MapFS {
	t.Helper()
	fsys := catalogFS(1, 1, "v1")
	shipper, _ := json.Marshal(map[string]any{
		"revision":   1,
		"definition": map[string]any{"name": "shipper", "description": "ships", "valid_stages": []string{"ship"}},
	})
	fsys["agents/shipper/agent.json"] = &fstest.MapFile{Data: shipper}
	fsys["agents/shipper/prompts/main.md"] = &fstest.MapFile{Data: []byte("ship it")}
	if stages == nil {
		stages = []map[string]any{
			flowStage("backlog", map[string]any{"initial": true}),
			flowStage("in_progress", map[string]any{"default_agent": "writer", "auto_advance": true,
				"labels": map[string]string{"en": "Build", "es": "Construir"}}),
			flowStage("ship", map[string]any{"labels": map[string]string{"en": "Ship", "es": "Envío"}}),
			flowStage("done", map[string]any{"terminal": true}),
		}
	}
	flow, err := json.Marshal(map[string]any{
		"revision": flowRev,
		"tags":     []string{"flow"},
		"label":    "Mini flow",
		"labels":   map[string]string{"es": "Flujo mini"},
		"board":    map[string]any{"id": "mini", "max_kick_backs": 2},
		"stages":   stages,
		"task_types": []map[string]any{
			{"id": "task", "label": "Task", "default": true, "skips": []string{}},
			{"id": "release", "label": "Release", "skips": []string{"in_progress"},
				"agents": map[string]string{"ship": "shipper"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fsys["flows/mini/flow.json"] = &fstest.MapFile{Data: flow}
	return fsys
}

type flowRig struct {
	st     StoreState
	boards *stagepkg.Store
	held   map[string]int
	// Tasks per "board/type/stage"; "board/type/" counts the type on the whole board.
	heldType map[string]int
}

func newFlowRig(t *testing.T) (*flowRig, context.Context) {
	t.Helper()
	db := testpg.New(t)
	agents, err := agentstore.New(db)
	if err != nil {
		t.Fatalf("agent store: %v", err)
	}
	boards, err := stagepkg.NewStore(db)
	if err != nil {
		t.Fatalf("stage store: %v", err)
	}
	return &flowRig{st: StoreState{Store: agents, Tenant: tenant}, boards: boards, held: map[string]int{}, heldType: map[string]int{}}, context.Background()
}

func (r *flowRig) env() Env {
	return Env{Locale: "en", Boards: StageBoards{Store: r.boards, Tenant: r.st.Tenant,
		Count: func(_ context.Context, boardID, stage string) (int, error) { return r.held[boardID+"/"+stage], nil },
		CountType: func(_ context.Context, boardID, typeID, stage string) (int, error) {
			return r.heldType[boardID+"/"+typeID+"/"+stage], nil
		}}}
}

func (r *flowRig) plan(t *testing.T, ctx context.Context, c *Catalog, choices Choices, refs ...Ref) (*Set, *Plan) {
	t.Helper()
	if len(refs) == 0 {
		refs = []Ref{{Kind: KindFlow, Name: "mini"}}
	}
	set, err := c.Select(refs)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	p, err := MakePlan(ctx, r.st, set, SourceWorkshop, choices, r.env())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return set, p
}

func (r *flowRig) apply(t *testing.T, ctx context.Context, set *Set, p *Plan) Result {
	t.Helper()
	res, err := Apply(ctx, r.st.Store, r.st.Tenant, set, p, "tester")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := ApplyFlows(ctx, r.boards, r.st.Tenant, set, p, &res); err != nil {
		t.Fatalf("apply flows: %v", err)
	}
	return res
}

func stageOf(t *testing.T, s *stagepkg.Store, board string, id types.Stage) types.StageInfo {
	t.Helper()
	st, ok := s.Board(board).Get(id)
	if !ok {
		t.Fatalf("board %s has no stage %s", board, id)
	}
	return st
}

func hasNotice(p *Plan, code string) bool {
	return slices.ContainsFunc(p.Notices, func(n Notice) bool { return n.Code == code })
}

func TestFlowSelectBringsAgentsAndSkills(t *testing.T) {
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, err := c.Select([]Ref{{Kind: KindFlow, Name: "mini"}})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(set.Flows) != 1 {
		t.Fatalf("flows = %d, want 1", len(set.Flows))
	}
	for _, name := range []string{"writer", "shipper"} {
		if _, ok := set.Agent(name); !ok {
			t.Errorf("selecting the flow did not bring agent %s", name)
		}
	}
	for _, name := range []string{"style", "tone"} {
		if _, ok := set.Skill(name); !ok {
			t.Errorf("selecting the flow did not bring skill %s", name)
		}
	}
	if _, err := c.Select([]Ref{{Kind: KindFlow, Name: "nope"}}); err == nil {
		t.Error("an unknown flow was selected")
	}
}

func TestFlowCatalogRejectsBrokenFlows(t *testing.T) {
	cases := map[string][]map[string]any{
		"unknown agent": {
			flowStage("backlog", map[string]any{"initial": true}),
			flowStage("in_progress", map[string]any{"default_agent": "ghost"}),
			flowStage("ship", nil), flowStage("done", map[string]any{"terminal": true}),
		},
		"agent outside its stages": {
			flowStage("backlog", map[string]any{"initial": true}),
			flowStage("in_progress", map[string]any{"default_agent": "shipper"}),
			flowStage("ship", nil), flowStage("done", map[string]any{"terminal": true}),
		},
		"no terminal stage": {
			flowStage("backlog", map[string]any{"initial": true}),
			flowStage("in_progress", nil), flowStage("ship", nil),
		},
	}
	for name, stages := range cases {
		if _, err := LoadCatalog(flowFS(t, 1, stages)); err == nil {
			t.Errorf("%s: the catalog loaded", name)
		}
	}
}

func TestFlowInstallCreatesBoardTypesAndDefaults(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, p := r.plan(t, ctx, c, Choices{})
	it := itemOf(t, p, KindFlow, "mini")
	if it.Action != ActionInstall || it.Resolution != ResolveInstall || it.Target != "mini" {
		t.Fatalf("flow item = %+v", it)
	}
	if SortItems(p.Items); p.Items[0].Kind != KindFlow {
		t.Errorf("first item = %s, want the flow", p.Items[0].Kind)
	}
	res := r.apply(t, ctx, set, p)
	if !slices.Contains(res.Installed, Ref{Kind: KindFlow, Name: "mini"}) {
		t.Errorf("installed = %+v", res.Installed)
	}

	info, ok := stagepkg.BoardInfoOf(r.boards, "mini")
	if !ok || info.WorkspaceID != tenant || info.MaxKickBacks != 2 || info.Label != "Mini flow" {
		t.Fatalf("board = %+v", info)
	}
	if info.Flow == nil || info.Flow.Name != "mini" || info.Flow.Revision != 1 || info.Flow.Hash == "" {
		t.Fatalf("board flow = %+v", info.Flow)
	}
	if got := stageOf(t, r.boards, "mini", "in_progress"); got.DefaultAgent != "writer" || got.Label != "Build" || !got.AutoAdvance {
		t.Errorf("in_progress = %+v", got)
	}
	tt := r.boards.BoardTypes("mini")
	if len(tt) != 2 || tt[1].ID != "release" || tt[1].Agents["ship"] != "shipper" ||
		!slices.Equal(tt[1].Skips, []types.Stage{"in_progress"}) {
		t.Fatalf("types = %+v", tt)
	}
	if got := r.boards.BoardFor("mini", "release").Next("backlog"); got != "ship" {
		t.Errorf("release task after backlog goes to %q, want ship", got)
	}
	for _, name := range []string{"writer", "shipper"} {
		if _, ok, _ := r.st.Agent(ctx, name); !ok {
			t.Errorf("agent %s not installed", name)
		}
	}
}

func TestFlowReinstallIsSameAndWritesNothing(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, p := r.plan(t, ctx, c, Choices{})
	r.apply(t, ctx, set, p)

	set, p = r.plan(t, ctx, c, Choices{})
	if it := itemOf(t, p, KindFlow, "mini"); it.Action != ActionSame || it.Target != "mini" || it.Installed != 1 {
		t.Fatalf("second plan flow item = %+v", it)
	}
	if p.Writes != 0 {
		t.Errorf("second plan writes %d items, want 0: %+v", p.Writes, p.Items)
	}
	res := r.apply(t, ctx, set, p)
	if len(res.Installed)+len(res.Updated)+len(res.Replaced)+len(res.Copied) != 0 {
		t.Errorf("reapply wrote %+v", res)
	}
	if n := len(r.boards.BoardList()); n != 2 {
		t.Errorf("boards = %d, want the seeded one and mini", n)
	}
}

func TestUntouchedFlowUpdatesAndKeepsLabels(t *testing.T) {
	r, ctx := newFlowRig(t)
	set, p := r.plan(t, ctx, mustCatalog(t, flowFS(t, 1, nil)), Choices{})
	r.apply(t, ctx, set, p)

	// A renamed column is not an edit: the label stays out of the hash.
	board := r.boards.Board("mini").List()
	board[1].Label = "Coding"
	if err := r.boards.SaveBoard(ctx, "mini", board); err != nil {
		t.Fatal(err)
	}

	v2 := []map[string]any{
		flowStage("backlog", map[string]any{"initial": true}),
		flowStage("in_progress", map[string]any{"default_agent": "writer", "auto_advance": true, "push_branch": true}),
		flowStage("ship", nil),
		flowStage("done", map[string]any{"terminal": true}),
	}
	set, p = r.plan(t, ctx, mustCatalog(t, flowFS(t, 2, v2)), Choices{})
	it := itemOf(t, p, KindFlow, "mini")
	if it.Action != ActionUpdate || it.Resolution != ResolveUpdate || it.Edited {
		t.Fatalf("flow item = %+v", it)
	}
	r.apply(t, ctx, set, p)
	got := stageOf(t, r.boards, "mini", "in_progress")
	if !got.PushBranch || got.Label != "Coding" {
		t.Errorf("in_progress after update = %+v", got)
	}
	if info, _ := stagepkg.BoardInfoOf(r.boards, "mini"); info.Flow.Revision != 2 {
		t.Errorf("revision after update = %d", info.Flow.Revision)
	}
}

func TestEditedFlowBoardIsAConflictThatKeepsByDefault(t *testing.T) {
	r, ctx := newFlowRig(t)
	set, p := r.plan(t, ctx, mustCatalog(t, flowFS(t, 1, nil)), Choices{})
	r.apply(t, ctx, set, p)

	board := r.boards.Board("mini").List()
	board[1].AutoAdvance = false
	if err := r.boards.SaveBoard(ctx, "mini", board); err != nil {
		t.Fatal(err)
	}
	c2 := mustCatalog(t, flowFS(t, 2, nil))
	set, p = r.plan(t, ctx, c2, Choices{})
	it := itemOf(t, p, KindFlow, "mini")
	if it.Action != ActionConflict || !it.Edited || it.Resolution != ResolveKeep {
		t.Fatalf("flow item = %+v", it)
	}
	r.apply(t, ctx, set, p)
	if stageOf(t, r.boards, "mini", "in_progress").AutoAdvance {
		t.Error("keep overwrote the edited board")
	}

	set, p = r.plan(t, ctx, c2, Choices{Resolutions: map[string]Resolution{"flow/mini": ResolveReplace}})
	r.apply(t, ctx, set, p)
	if !stageOf(t, r.boards, "mini", "in_progress").AutoAdvance {
		t.Error("replace left the edit")
	}
	if _, p = r.plan(t, ctx, c2, Choices{}); itemOf(t, p, KindFlow, "mini").Action != ActionSame {
		t.Errorf("after replace the flow reads %s", itemOf(t, p, KindFlow, "mini").Action)
	}
}

func TestFlowCopyAndTakenIDGetSuffixes(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	// Another workspace already holds the id.
	other := StageBoards{Store: r.boards, Tenant: "ws-other"}
	if err := r.boards.CreateBoard(ctx, stagepkg.BoardInfo{ID: "mini", Label: "Theirs", WorkspaceID: other.Tenant},
		stagepkg.Defaults()); err != nil {
		t.Fatal(err)
	}
	set, p := r.plan(t, ctx, c, Choices{})
	it := itemOf(t, p, KindFlow, "mini")
	if it.Target != "mini_ws_test" || !hasNotice(p, NoticeFlowBoardRenamed) {
		t.Fatalf("flow item = %+v, notices %+v", it, p.Notices)
	}
	r.apply(t, ctx, set, p)

	// Copy is offered on an update or a conflict, never on a board that matches the flow.
	board := r.boards.Board("mini_ws_test").List()
	board[1].AutoAdvance = false
	if err := r.boards.SaveBoard(ctx, "mini_ws_test", board); err != nil {
		t.Fatal(err)
	}
	set, p = r.plan(t, ctx, c, Choices{Resolutions: map[string]Resolution{"flow/mini": ResolveCopy}})
	if it := itemOf(t, p, KindFlow, "mini"); it.Target != "mini_ws_test_2" {
		t.Fatalf("copy target = %q", it.Target)
	}
	r.apply(t, ctx, set, p)
	if !r.boards.HasBoard("mini_ws_test_2") {
		t.Fatal("copy board missing")
	}
	if _, p = r.plan(t, ctx, c, Choices{}); itemOf(t, p, KindFlow, "mini").Target != "mini_ws_test_2" {
		t.Errorf("an update would target %q, want the newest board", itemOf(t, p, KindFlow, "mini").Target)
	}
}

func TestFlowWithSkippedAgentWarnsAndLeavesTheColumnEmpty(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, p := r.plan(t, ctx, c, Choices{Resolutions: map[string]Resolution{"agent/shipper": ResolveSkip}})
	if !hasNotice(p, NoticeFlowMissingAgent) {
		t.Fatalf("no %s notice: %+v", NoticeFlowMissingAgent, p.Notices)
	}
	r.apply(t, ctx, set, p)
	tt := r.boards.BoardTypes("mini")
	if _, ok := tt[1].Agents["ship"]; ok {
		t.Errorf("release type still names the skipped agent: %+v", tt[1].Agents)
	}
	if stageOf(t, r.boards, "mini", "in_progress").DefaultAgent != "writer" {
		t.Error("the installed agent lost its column")
	}
}

// The board an install wrote reads as installed, not as edited, even though it differs from the
// catalog's flow: the person skipped one of its agents.
func TestFlowInstalledWithoutAnAgentReadsAsInstalled(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, p := r.plan(t, ctx, c, Choices{Resolutions: map[string]Resolution{"agent/shipper": ResolveSkip}})
	r.apply(t, ctx, set, p)

	_, p = r.plan(t, ctx, c, Choices{})
	if it := itemOf(t, p, KindFlow, "mini"); it.Action != ActionSame || it.Edited {
		t.Fatalf("flow after an install without shipper = %+v", it)
	}
	entries, err := List(ctx, r.st, r.env().Boards, c, "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == KindFlow && e.Status != StatusInstalled {
			t.Errorf("flow lists as %s right after its install", e.Status)
		}
	}
	// A later revision still reads as an update, and an edit still as a conflict.
	if _, p = r.plan(t, ctx, mustCatalog(t, flowFS(t, 2, nil)), Choices{}); itemOf(t, p, KindFlow, "mini").Action != ActionUpdate {
		t.Errorf("revision 2 reads %s", itemOf(t, p, KindFlow, "mini").Action)
	}
	board := r.boards.Board("mini").List()
	board[1].AutoAdvance = false
	if err := r.boards.SaveBoard(ctx, "mini", board); err != nil {
		t.Fatal(err)
	}
	if _, p = r.plan(t, ctx, c, Choices{}); itemOf(t, p, KindFlow, "mini").Action != ActionConflict {
		t.Errorf("an edited board reads %s", itemOf(t, p, KindFlow, "mini").Action)
	}
}

// A replace must not strand tasks the board's own type editor would refuse to strand: a type the
// flow does not have, or a column a type skips again.
func TestFlowReplaceBlockedByTypeWithTasks(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, p := r.plan(t, ctx, c, Choices{})
	r.apply(t, ctx, set, p)

	tt := r.boards.BoardTypes("mini")
	tt[1].Skips = []types.Stage{} // release tasks now walk in_progress
	tt = append(tt, stagepkg.TaskTypeInfo{ID: "spike", Label: "Spike", Skips: []types.Stage{}})
	if err := r.boards.SaveBoardTypes(ctx, "mini", tt); err != nil {
		t.Fatal(err)
	}
	replace := Choices{Resolutions: map[string]Resolution{"flow/mini": ResolveReplace}}

	r.heldType["mini/spike/"] = 1
	r.heldType["mini/release/in_progress"] = 2
	r.heldType["mini/task/in_progress"] = 5 // task never skips in_progress: not stranded
	set, p = r.plan(t, ctx, c, replace)
	var refs []string
	for _, n := range p.Notices {
		if n.Code == NoticeFlowTypeHasTasks {
			refs = n.Refs
		}
	}
	if !p.Blocked || !slices.Equal(refs, []string{"spike", "release/in_progress"}) {
		t.Fatalf("blocked=%v refs=%v notices=%+v", p.Blocked, refs, p.Notices)
	}
	if err := ApplyFlows(ctx, r.boards, tenant, set, p, &Result{}); err == nil {
		t.Error("a blocked flow plan applied")
	}

	r.heldType = map[string]int{}
	set, p = r.plan(t, ctx, c, replace)
	if p.Blocked {
		t.Fatalf("blocked without tasks: %+v", p.Notices)
	}
	r.apply(t, ctx, set, p)
	if got := r.boards.BoardTypes("mini"); len(got) != 2 {
		t.Errorf("types after replace = %+v", got)
	}
}

func TestFlowAgentCopyIsAssignedByNewName(t *testing.T) {
	r, ctx := newFlowRig(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	set, p := r.plan(t, ctx, c, Choices{}, Ref{Kind: KindAgent, Name: "writer"})
	r.apply(t, ctx, set, p)
	time.Sleep(5 * time.Millisecond)
	if _, err := r.st.Store.PutFile(ctx, tenant, agentstore.KindAgent, "writer", "prompts/main.md", []byte("mine"), "person"); err != nil {
		t.Fatal(err)
	}
	set, p = r.plan(t, ctx, c, Choices{Resolutions: map[string]Resolution{"agent/writer": ResolveCopy}})
	r.apply(t, ctx, set, p)
	if got := stageOf(t, r.boards, "mini", "in_progress").DefaultAgent; got != "writer-2" {
		t.Errorf("in_progress default agent = %q, want the copy writer-2", got)
	}
}

func TestFlowUpdateBlockedByStageWithTasks(t *testing.T) {
	r, ctx := newFlowRig(t)
	set, p := r.plan(t, ctx, mustCatalog(t, flowFS(t, 1, nil)), Choices{})
	r.apply(t, ctx, set, p)
	r.held["mini/ship"] = 3

	noShip := []map[string]any{
		flowStage("backlog", map[string]any{"initial": true}),
		flowStage("in_progress", map[string]any{"default_agent": "writer", "auto_advance": true}),
		flowStage("done", map[string]any{"terminal": true}),
	}
	fsys := flowFS(t, 2, noShip)
	// The release type's skip and agent live on ship; without it the type keeps nothing to skip.
	raw := fsys["flows/mini/flow.json"].Data
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["task_types"] = []map[string]any{{"id": "task", "label": "Task", "default": true, "skips": []string{}}}
	raw, _ = json.Marshal(m)
	fsys["flows/mini/flow.json"] = &fstest.MapFile{Data: raw}

	set, p = r.plan(t, ctx, mustCatalog(t, fsys), Choices{})
	if !p.Blocked || !hasNotice(p, NoticeFlowStageHasTasks) {
		t.Fatalf("plan not blocked: %+v", p.Notices)
	}
	if err := ApplyFlows(ctx, r.boards, tenant, set, p, &Result{}); err == nil {
		t.Error("a blocked flow plan applied")
	}
	r.held["mini/ship"] = 0
	set, p = r.plan(t, ctx, mustCatalog(t, fsys), Choices{})
	r.apply(t, ctx, set, p)
	if r.boards.Board("mini").Valid("ship") {
		t.Error("the emptied stage survived the update")
	}
}

func TestFlowStagesCountAsKnown(t *testing.T) {
	r, ctx := newFlowRig(t)
	set, err := mustCatalog(t, flowFS(t, 1, nil)).Select([]Ref{{Kind: KindFlow, Name: "mini"}})
	if err != nil {
		t.Fatal(err)
	}
	env := r.env()
	env.StageKnown = func(string) bool { return false }
	p, err := MakePlan(ctx, r.st, set, SourceWorkshop, Choices{}, env)
	if err != nil {
		t.Fatal(err)
	}
	if hasNotice(p, NoticeUnknownStage) {
		t.Errorf("the flow's own stages read as unknown: %+v", p.Notices)
	}
}

func TestFlowWithoutBoardsListsButDoesNotInstall(t *testing.T) {
	st, ctx := newState(t)
	c := mustCatalog(t, flowFS(t, 1, nil))
	entries, err := List(ctx, st, nil, c, "es")
	if err != nil {
		t.Fatal(err)
	}
	var flow Entry
	for _, e := range entries {
		if e.Kind == KindFlow {
			flow = e
		}
	}
	if flow.Status != StatusAvailable || flow.Label != "Flujo mini" ||
		!slices.Equal(flow.Agents, []string{"writer", "shipper"}) {
		t.Errorf("flow entry = %+v", flow)
	}
	set, _ := c.Select([]Ref{{Kind: KindFlow, Name: "mini"}})
	if _, err := MakePlan(ctx, st, set, SourceWorkshop, Choices{}, Env{}); err == nil {
		t.Error("a flow planned without a board store")
	}
}

func TestFlowHashIgnoresLabelsAndColors(t *testing.T) {
	stages := []types.StageInfo{
		{ID: "backlog", Label: "Backlog", Initial: true, Color: "#000"},
		{ID: "done", Label: "Done", Terminal: true},
	}
	tt := []stagepkg.TaskTypeInfo{{ID: "task", Label: "Task", Icon: "bug", Skips: []types.Stage{}}}
	base := FlowHash(2, stages, tt)
	relabeled := []types.StageInfo{stages[0], stages[1]}
	relabeled[0].Label, relabeled[0].Color, relabeled[0].Position = "Inbox", "#fff", 40
	tt2 := []stagepkg.TaskTypeInfo{{ID: "task", Label: "Chore", Icon: "chore"}}
	if FlowHash(2, relabeled, tt2) != base {
		t.Error("a label, color or icon changed the hash")
	}
	if FlowHash(3, stages, tt) == base {
		t.Error("the kick-back cap is not in the hash")
	}
	flagged := []types.StageInfo{stages[0], stages[1]}
	flagged[1].DefaultAgent = "writer"
	if FlowHash(2, flagged, tt) == base {
		t.Error("a default agent is not in the hash")
	}
}

// The shipped flow, checked against the plan's table: eleven columns, their flags and agents, the
// kick-back cap and the four task types with what each skips.
func TestThuluFlowMatchesThePlan(t *testing.T) {
	c, err := LoadCatalog(builtin.Workshop())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	f, ok := c.Flow("thulu")
	if !ok {
		t.Fatal("the catalog has no thulu flow")
	}
	spec := f.Spec("es", nil, nil)
	want := []types.Stage{"backlog", "refinement", "test_design", "in_progress", "verification",
		"ai_review", "testing", "readiness", "review", "release", "done"}
	got := make([]types.Stage, len(spec.Stages))
	byID := map[types.Stage]types.StageInfo{}
	for i, st := range spec.Stages {
		got[i] = st.ID
		byID[st.ID] = st
	}
	if !slices.Equal(got, want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}
	if spec.Info.MaxKickBacks != 2 || spec.Info.ID != "thulu" || spec.Info.Label != "Flujo Thulu" {
		t.Errorf("board = %+v", spec.Info)
	}
	agents := map[types.Stage]string{
		"refinement": "thulu-refiner", "test_design": "thulu-test-designer", "in_progress": "thulu-developer",
		"verification": "thulu-verifier", "ai_review": "thulu-reviewer", "testing": "thulu-agentic-tester",
		"readiness": "thulu-readiness",
	}
	for _, id := range want {
		if byID[id].DefaultAgent != agents[id] {
			t.Errorf("%s default agent = %q, want %q", id, byID[id].DefaultAgent, agents[id])
		}
	}
	checks := map[string]bool{
		"backlog initial":                byID["backlog"].Initial,
		"refinement auto+ingest":         byID["refinement"].AutoAdvance && byID["refinement"].IngestFlags,
		"test_design auto+push+lock":     byID["test_design"].AutoAdvance && byID["test_design"].PushBranch && byID["test_design"].LockTests,
		"in_progress verdict+push+work":  byID["in_progress"].AgentVerdict && byID["in_progress"].PushBranch && byID["in_progress"].WipGroup == types.WipGroupWork,
		"verification verdict+draft":     byID["verification"].AgentVerdict && byID["verification"].PRDraft && byID["verification"].WipGroup == types.WipGroupWork,
		"ai_review verdict+work":         byID["ai_review"].AgentVerdict && byID["ai_review"].AutoAdvance && byID["ai_review"].WipGroup == types.WipGroupWork,
		"testing verdict, never skipped": byID["testing"].AgentVerdict && len(byID["testing"].SkipUnless) == 0 && byID["testing"].WipGroup == types.WipGroupWork,
		"readiness verdict+auto+work":    byID["readiness"].AgentVerdict && byID["readiness"].AutoAdvance && byID["readiness"].WipGroup == types.WipGroupWork && !byID["readiness"].PushBranch,
		"readiness label":                byID["readiness"].Label == "Production Readiness",
		"review ready+kick back":         byID["review"].PRReady && byID["review"].OnChangesRequested == types.ChangesKickBack && byID["review"].ChangesKickBackTo == "in_progress",
		"review counts as to review":     byID["review"].WipGroup == types.WipGroupReview,
		"review not auto":                !byID["review"].AutoAdvance,
		"release merge+release":          byID["release"].MergeTarget && byID["release"].ReleaseResources,
		"release advances on a verdict":  byID["release"].AutoAdvance && byID["release"].AgentVerdict,
		"done terminal":                  byID["done"].Terminal,
		"labels in the locale":           byID["in_progress"].Label == "Desarrollo" && byID["test_design"].Label == "Diseño de pruebas",
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s does not hold", name)
		}
	}
	approvals := map[types.Stage][]types.ApprovalRequirement{
		"refinement": byID["refinement"].RequiredApprovals, "review": byID["review"].RequiredApprovals,
	}
	if a := approvals["refinement"]; len(a) != 1 || a[0].Key != "definition_ready" || !slices.Equal(a[0].TaskTypes, []string{"feature"}) {
		t.Errorf("refinement approvals = %+v", a)
	}
	if a := approvals["review"]; len(a) != 1 || a[0].Key != "merge" || len(a[0].TaskTypes) != 0 {
		t.Errorf("review approvals = %+v", a)
	}

	skips := map[string][]types.Stage{}
	typeAgents := map[string]map[types.Stage]string{}
	for _, tt := range spec.Types {
		skips[tt.ID] = tt.Skips
		typeAgents[tt.ID] = tt.Agents
	}
	if !slices.Equal(skips["chore"], []types.Stage{"test_design", "testing"}) || len(skips["feature"]) != 0 || len(skips["bug"]) != 0 {
		t.Errorf("skips = %v", skips)
	}
	release := stagepkg.NewSnapshot(stagepkg.FilterStages(spec.Stages, skips["release"]))
	if next := release.Next("backlog"); next != "release" {
		t.Errorf("a release task goes from backlog to %q", next)
	}
	if typeAgents["release"]["release"] != "thulu-release" || len(typeAgents["feature"]) != 0 {
		t.Errorf("type agents = %v", typeAgents)
	}
	if def := stagepkg.DefaultTaskType(spec.Types); def != "feature" {
		t.Errorf("default type = %q", def)
	}

	// Per-task skips: the reviews are marked never skipped, and what a task may skip is exactly the
	// four work columns the refiner's prompt and spec-writing talk about.
	if !byID["ai_review"].NoSkip || !byID["review"].NoSkip {
		t.Errorf("no_skip: ai_review %v, review %v; both must be never skipped", byID["ai_review"].NoSkip, byID["review"].NoSkip)
	}
	var skippable []types.Stage
	for _, id := range want {
		if stagepkg.SkippableReason(byID[id]) == "" {
			skippable = append(skippable, id)
		}
	}
	if w := []types.Stage{"test_design", "in_progress", "testing", "readiness"}; !slices.Equal(skippable, w) {
		t.Errorf("skippable columns = %v, want %v", skippable, w)
	}
}

// The setup's "minimal start": the factory board's four columns (stage.DefaultsIn), one generic
// agent in In progress and none in Review, which is for a person. In progress does not push: the
// agent commits and the person decides about the branch and the PR.
func TestMinimalFlowIsTheFactoryBoardWithOneAgent(t *testing.T) {
	c, err := LoadCatalog(builtin.Workshop())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	f, ok := c.Flow("thulu-minimal")
	if !ok {
		t.Fatal("the catalog has no thulu-minimal flow")
	}
	spec := f.Spec("es", nil, nil)
	factory := stagepkg.DefaultsIn("es")
	if len(spec.Stages) != len(factory) {
		t.Fatalf("columns = %d, want the %d of the factory board", len(spec.Stages), len(factory))
	}
	for i, st := range spec.Stages {
		want := factory[i]
		if st.ID != want.ID || st.Label != want.Label || st.Initial != want.Initial || st.Terminal != want.Terminal {
			t.Errorf("column %d = %s %q, want %s %q", i, st.ID, st.Label, want.ID, want.Label)
		}
		agent := ""
		if st.ID == types.StageInProgress {
			agent = "thulu-agent"
		}
		if st.DefaultAgent != agent {
			t.Errorf("%s default agent = %q, want %q", st.ID, st.DefaultAgent, agent)
		}
	}
	work := spec.Stages[1]
	if !work.AutoAdvance || !work.AgentVerdict || work.PushBranch || work.OpenPR || work.WipGroup != types.WipGroupWork {
		t.Errorf("in_progress = %+v, want auto-advance on a verdict, no push and no PR", work)
	}
	if review := spec.Stages[2]; review.AutoAdvance || review.WipGroup != types.WipGroupReview {
		t.Errorf("review = %+v, want a column a person moves on", review)
	}
	if spec.Info.ID != "thulu_minimal" || spec.Info.Label != "Arranque mínimo" || len(spec.Types) != 0 {
		t.Errorf("board = %+v, types = %v", spec.Info, spec.Types)
	}
	set, err := c.Select([]Ref{{Kind: KindFlow, Name: "thulu-minimal"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Agents) != 1 || set.Agents[0].Def.Name != "thulu-agent" || len(set.Skills) != 0 {
		t.Errorf("the flow brings agents %v and %d skills, want thulu-agent alone", agentNames(set), len(set.Skills))
	}
}

func agentNames(set *Set) []string {
	out := make([]string, len(set.Agents))
	for i := range set.Agents {
		out[i] = set.Agents[i].Def.Name
	}
	return out
}
