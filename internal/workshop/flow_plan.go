package workshop

import (
	"context"
	"slices"
	"sort"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// Notice codes for flows; the dashboard translates them like the others.
const (
	// An update or replace would drop a column that still holds tasks.
	NoticeFlowStageHasTasks = "flow_stage_has_tasks"
	// The flow assigns an agent the plan neither installs nor finds: its columns get no agent.
	NoticeFlowMissingAgent = "flow_missing_agent"
	// The flow's board id was taken, so the board installs under another.
	NoticeFlowBoardRenamed = "flow_board_renamed"
	// An update or replace would drop a task type that still has tasks, or make a type skip a
	// column where tasks of that type sit.
	NoticeFlowTypeHasTasks = "flow_type_has_tasks"
)

// A board installed from a flow, as the workspace has it now.
type FlowBoard struct {
	ID       string
	Revision int
	// What the Workshop wrote; CurrentHash is the board as it is. They differ once someone edits
	// the board.
	Hash        string
	CurrentHash string
	Stages      []string
	// The board's columns as they are now, for the plan's list of what an update changes.
	Columns []types.StageInfo
	// The board's task types as they are now, for the tasks an update would strand.
	Types []stagepkg.TaskTypeInfo
}

// BoardState answers the plan's questions about the workspace's boards.
type BoardState interface {
	// Oldest first.
	FlowBoards(ctx context.Context, flow string) ([]FlowBoard, error)
	StageTasks(ctx context.Context, boardID, stage string) (int, error)
	// Tasks of a type on the board; stage "" counts every stage.
	TypeTasks(ctx context.Context, boardID, typeID, stage string) (int, error)
	FreeBoardID(ctx context.Context, base string) (string, error)
}

// BoardWriter is what ApplyFlows writes boards with; *stage.Store implements it.
type BoardWriter interface {
	InstallFlowBoard(ctx context.Context, info stagepkg.BoardInfo, stages []types.StageInfo, tt []stagepkg.TaskTypeInfo) error
	UpdateFlowBoard(ctx context.Context, info stagepkg.BoardInfo, stages []types.StageInfo, tt []stagepkg.TaskTypeInfo) error
}

// StageBoards reads a workspace's boards from the stage store. Count answers how many tasks sit in
// a board's stage and CountType how many of one type (stage "" for any); nil reads them as empty.
type StageBoards struct {
	Store     *stagepkg.Store
	Tenant    string
	Count     func(ctx context.Context, boardID, stage string) (int, error)
	CountType func(ctx context.Context, boardID, typeID, stage string) (int, error)
}

var _ BoardState = StageBoards{}

func (b StageBoards) FlowBoards(_ context.Context, flow string) ([]FlowBoard, error) {
	var out []FlowBoard
	for _, info := range b.Store.BoardList() {
		if info.WorkspaceID != b.Tenant || info.Flow == nil || info.Flow.Name != flow {
			continue
		}
		stages := b.Store.Board(info.ID).List()
		ids := make([]string, len(stages))
		for i := range stages {
			ids[i] = string(stages[i].ID)
		}
		tt := b.Store.BoardTypes(info.ID)
		out = append(out, FlowBoard{
			ID: info.ID, Revision: info.Flow.Revision, Hash: info.Flow.Hash, Stages: ids, Columns: stages,
			Types: tt, CurrentHash: FlowHash(info.MaxKickBacks, stages, tt),
		})
	}
	return out, nil
}

func (b StageBoards) StageTasks(ctx context.Context, boardID, stage string) (int, error) {
	if b.Count == nil {
		return 0, nil
	}
	return b.Count(ctx, boardID, stage)
}

func (b StageBoards) TypeTasks(ctx context.Context, boardID, typeID, stage string) (int, error) {
	if b.CountType == nil {
		return 0, nil
	}
	return b.CountType(ctx, boardID, typeID, stage)
}

func (b StageBoards) FreeBoardID(_ context.Context, base string) (string, error) {
	return b.Store.FreeBoardID(base, b.Tenant)
}

// With no boards the flow reads as not installed; it still lists, but MakePlan refuses to write it.
func classifyFlow(ctx context.Context, boards BoardState, f *FlowItem) (Item, []string, error) {
	it := Item{Kind: KindFlow, Name: f.Name, Target: f.Board.ID, Revision: f.Revision, Action: ActionInstall}
	if boards == nil {
		return it, nil, nil
	}
	list, err := boards.FlowBoards(ctx, f.Name)
	if err != nil || len(list) == 0 {
		return it, nil, err
	}
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	// The newest board is the one an update targets; the rest are older installs or copies.
	b := list[len(list)-1]
	it.Target, it.Installed, it.Edited = b.ID, b.Revision, b.Hash != b.CurrentHash
	it.UsedBy = ids[:len(ids)-1]
	// An untouched board of this revision is the flow as installed, even when it differs from the
	// catalog's spec: the install left out an agent the person skipped, or named an agent copy.
	switch want := f.Spec(i18n.SourceLocale, nil, nil).Info.Flow.Hash; {
	case want == b.CurrentHash, !it.Edited && f.Revision <= b.Revision:
		it.Action = ActionSame
	case !it.Edited:
		it.Action = ActionUpdate
	default:
		it.Action = ActionConflict
	}
	return it, ids, nil
}

func (p *Plan) classifyFlows(ctx context.Context, set *Set, env Env) error {
	for i := range set.Flows {
		it, _, err := classifyFlow(ctx, env.Boards, &set.Flows[i])
		if err != nil {
			return err
		}
		p.Items = append(p.Items, it)
	}
	return nil
}

// An install or a copy gets a free board id: the flow's own, or the name the person typed.
func (p *Plan) flowTargets(ctx context.Context, set *Set, env Env, names map[string]string) error {
	for i := range p.Items {
		it := &p.Items[i]
		if it.Kind != KindFlow || (it.Resolution != ResolveInstall && it.Resolution != ResolveCopy) {
			continue
		}
		if env.Boards == nil {
			return apperr.Unavailable("workshop.errors.flow_no_boards", "name", it.Name)
		}
		f, _ := set.Flow(it.Name)
		base := f.Board.ID
		if typed := names[it.Key()]; typed != "" {
			if !stagepkg.ValidBoardID(typed) {
				return apperr.Invalid("workshop.errors.flow_board_id_invalid", "name", it.Name, "target", typed)
			}
			base = typed
		}
		target, err := env.Boards.FreeBoardID(ctx, base)
		if err != nil {
			return err
		}
		it.Target = target
		if target != base {
			p.notice(Notice{Code: NoticeFlowBoardRenamed, Severity: SeverityWarning, Kind: KindFlow, Name: it.Name, Refs: []string{target}})
		}
	}
	return nil
}

func (p *Plan) checkFlows(ctx context.Context, set *Set, env Env) error {
	missing := p.missingAgents()
	for i := range set.Flows {
		f := &set.Flows[i]
		it, _ := p.item(KindFlow, f.Name)
		if !writes(it.Resolution) {
			continue
		}
		var lost []string
		for _, name := range f.AgentNames() {
			if missing[name] {
				lost = append(lost, name)
			}
		}
		if len(lost) > 0 {
			p.notice(Notice{Code: NoticeFlowMissingAgent, Severity: SeverityWarning, Kind: KindFlow, Name: f.Name, Refs: lost})
		}
		if it.Resolution != ResolveUpdate && it.Resolution != ResolveReplace {
			continue
		}
		if err := p.checkDroppedStages(ctx, env.Boards, f, it.Target); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plan) checkDroppedStages(ctx context.Context, boards BoardState, f *FlowItem, boardID string) error {
	list, err := boards.FlowBoards(ctx, f.Name)
	if err != nil {
		return err
	}
	keep := f.StageIDs()
	var held []string
	for i := range list {
		if list[i].ID != boardID {
			continue
		}
		for _, st := range list[i].Stages {
			if contains(keep, st) {
				continue
			}
			n, err := boards.StageTasks(ctx, boardID, st)
			if err != nil {
				return err
			}
			if n > 0 {
				held = append(held, st)
			}
		}
		stranded, err := strandedTypes(ctx, boards, f, &list[i], keep)
		if err != nil {
			return err
		}
		if len(stranded) > 0 {
			p.notice(Notice{Code: NoticeFlowTypeHasTasks, Severity: SeverityError, Kind: KindFlow, Name: f.Name, Refs: stranded})
		}
	}
	if len(held) > 0 {
		p.notice(Notice{Code: NoticeFlowStageHasTasks, Severity: SeverityError, Kind: KindFlow, Name: f.Name, Refs: held})
	}
	return nil
}

// strandedTypes lists what an update would leave tasks without: a type the flow drops that still
// has tasks ("spike"), and a column a type newly skips while tasks of that type sit there
// ("chore/testing"). The board's own type editor refuses both; the flow must too. Columns the flow
// drops are left to the stage check.
func strandedTypes(ctx context.Context, boards BoardState, f *FlowItem, b *FlowBoard, keep []string) ([]string, error) {
	dropped, err := droppedTypesWithTasks(ctx, boards, f, b)
	if err != nil {
		return nil, err
	}
	had := make(map[string][]types.Stage, len(b.Types))
	for i := range b.Types {
		had[b.Types[i].ID] = b.Types[i].Skips
	}
	out := dropped
	for i := range f.Types {
		id := f.Types[i].ID
		for _, st := range f.Types[i].Skips {
			if slices.Contains(had[id], st) || !contains(b.Stages, string(st)) || !contains(keep, string(st)) {
				continue
			}
			n, err := boards.TypeTasks(ctx, b.ID, id, string(st))
			if err != nil {
				return nil, err
			}
			if n > 0 {
				out = append(out, id+"/"+string(st))
			}
		}
	}
	return out, nil
}

func droppedTypesWithTasks(ctx context.Context, boards BoardState, f *FlowItem, b *FlowBoard) ([]string, error) {
	var out []string
	for i := range b.Types {
		id := b.Types[i].ID
		if slices.ContainsFunc(f.Types, func(ft FlowType) bool { return ft.ID == id }) {
			continue
		}
		n, err := boards.TypeTasks(ctx, b.ID, id, "")
		if err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, id)
		}
	}
	return out, nil
}

// Agents the workspace will not have after the plan: not there now, and skipped.
func (p *Plan) missingAgents() map[string]bool {
	out := map[string]bool{}
	for i := range p.Items {
		it := &p.Items[i]
		if it.Kind == KindAgent && it.Action == ActionInstall && it.Resolution == ResolveSkip {
			out[it.Name] = true
		}
	}
	return out
}

// Catalog agent → the name a copy installs it under.
func (p *Plan) agentCopies() map[string]string {
	out := map[string]string{}
	for i := range p.Items {
		it := &p.Items[i]
		if it.Kind == KindAgent && it.Resolution == ResolveCopy {
			out[it.Name] = it.Target
		}
	}
	return out
}

// keptCopies finds, for each flow the plan updates or replaces, the agent copies an earlier install
// named: a column (or a type's agent) of the untouched board that holds another agent than the
// flow's, when that agent has no Workshop or import origin. Copies record none, so a catalog agent
// the flow stopped using does not count, and a board someone edited keeps its own choices out.
func (p *Plan) keptCopies(ctx context.Context, st State, set *Set, env Env) error {
	for i := range p.Items {
		it := &p.Items[i]
		if it.Kind != KindFlow || it.Edited || (it.Resolution != ResolveUpdate && it.Resolution != ResolveReplace) {
			continue
		}
		f, _ := set.Flow(it.Name)
		kept, err := boardCopies(ctx, st, env.Boards, &f, it.Target)
		if err != nil {
			return err
		}
		if len(kept) == 0 {
			continue
		}
		if p.kept == nil {
			p.kept = map[string]map[string]string{}
		}
		p.kept[f.Name] = kept
	}
	return nil
}

// boardCopies is flow agent → the copy board boardID holds in its place; nil when the board is gone.
func boardCopies(ctx context.Context, st State, boards BoardState, f *FlowItem, boardID string) (map[string]string, error) {
	board, err := flowBoard(ctx, boards, f.Name, boardID)
	if err != nil || board == nil {
		return nil, err
	}
	kept := map[string]string{}
	for flowAgent, boardAgent := range boardAgentPairs(f, board) {
		copied, err := isCopy(ctx, st, boardAgent)
		if err != nil {
			return nil, err
		}
		if copied {
			kept[flowAgent] = boardAgent
		}
	}
	return kept, nil
}

func flowBoard(ctx context.Context, boards BoardState, flow, boardID string) (*FlowBoard, error) {
	if boards == nil {
		return nil, nil
	}
	list, err := boards.FlowBoards(ctx, flow)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == boardID {
			return &list[i], nil
		}
	}
	return nil, nil
}

// boardAgentPairs maps each agent the flow names to the different one the board holds in the same
// place (a column's default agent, or a type's agent for a column). A name the board uses for two
// flow agents, or two names for one, is ambiguous and left out.
func boardAgentPairs(f *FlowItem, b *FlowBoard) map[string]string {
	pairs := map[string]string{}
	bad := map[string]bool{}
	add := func(flowAgent, boardAgent string) {
		if flowAgent == "" || boardAgent == "" || flowAgent == boardAgent {
			return
		}
		if have, ok := pairs[flowAgent]; ok && have != boardAgent {
			bad[flowAgent] = true
		}
		pairs[flowAgent] = boardAgent
	}
	columns := make(map[types.Stage]string, len(b.Columns))
	for j := range b.Columns {
		columns[b.Columns[j].ID] = b.Columns[j].DefaultAgent
	}
	for i := range f.Stages {
		if agent, ok := columns[f.Stages[i].ID]; ok {
			add(f.Stages[i].DefaultAgent, agent)
		}
	}
	typeAgents := make(map[string]map[types.Stage]string, len(b.Types))
	for j := range b.Types {
		typeAgents[b.Types[j].ID] = b.Types[j].Agents
	}
	for i := range f.Types {
		held := typeAgents[f.Types[i].ID]
		for stage, name := range f.Types[i].Agents {
			add(name, held[stage])
		}
	}
	for name := range bad {
		delete(pairs, name)
	}
	return pairs
}

func isCopy(ctx context.Context, st State, name string) (bool, error) {
	if _, found, err := st.Agent(ctx, name); err != nil || !found {
		return false, err
	}
	origin, found, err := st.Origin(ctx, KindAgent, name)
	if err != nil {
		return false, err
	}
	return !found || (origin.Origin != SourceWorkshop && origin.Origin != SourceImport), nil
}

// flowAgents is how a flow's spec names its agents in this plan: the copies its board already uses,
// then the copies this plan makes, which win.
func (p *Plan) flowAgents(flow string, copies map[string]string) map[string]string {
	if len(p.kept[flow]) == 0 {
		return copies
	}
	out := make(map[string]string, len(copies)+len(p.kept[flow]))
	for from, to := range p.kept[flow] {
		out[from] = to
	}
	for from, to := range copies {
		out[from] = to
	}
	return out
}

// The stages of a flow being written count as known, or every agent of the flow would warn about
// stages its own board brings.
func flowStagesKnown(known func(string) bool, set *Set, p *Plan) func(string) bool {
	if known == nil {
		return nil
	}
	ours := map[string]bool{}
	for i := range set.Flows {
		if it, _ := p.item(KindFlow, set.Flows[i].Name); writes(it.Resolution) {
			for _, id := range set.Flows[i].StageIDs() {
				ours[id] = true
			}
		}
	}
	if len(ours) == 0 {
		return known
	}
	return func(id string) bool { return ours[id] || known(id) }
}

// ApplyFlows writes the boards of the plan's flows, after Apply wrote their agents and skills. The
// two stores commit apart: if a board fails, the agents stay installed and a retry reads them as
// installed.
func ApplyFlows(ctx context.Context, w BoardWriter, tenant string, set *Set, p *Plan, res *Result) error {
	if p.Blocked {
		return ErrBlocked
	}
	copies, missing := p.agentCopies(), p.missingAgents()
	for i := range set.Flows {
		f := &set.Flows[i]
		it, _ := p.item(KindFlow, f.Name)
		if !writes(it.Resolution) {
			res.note(it)
			continue
		}
		if w == nil {
			return apperr.Unavailable("workshop.errors.flow_no_boards", "name", f.Name)
		}
		spec := f.Spec(p.locale, p.flowAgents(f.Name, copies), missing)
		spec.Info.ID, spec.Info.WorkspaceID = it.Target, tenant
		var err error
		if it.Resolution == ResolveInstall || it.Resolution == ResolveCopy {
			err = w.InstallFlowBoard(ctx, spec.Info, spec.Stages, spec.Types)
		} else {
			err = w.UpdateFlowBoard(ctx, spec.Info, spec.Stages, spec.Types)
		}
		if err != nil {
			return err
		}
		res.note(it)
	}
	return nil
}

func flowEntry(ctx context.Context, boards BoardState, f *FlowItem, locale string) (Entry, error) {
	it, ids, err := classifyFlow(ctx, boards, f)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Kind: KindFlow, Name: f.Name, Label: localized(f.Label, f.Labels, locale),
		Description: localized(f.Description, f.Descriptions, locale),
		Tags:        nonNil(f.Tags), Revision: f.Revision,
		Status: statusOf(it.Action), Installed: it.Installed, Edited: it.Edited,
		Agents: f.AgentNames(), Stages: f.StageIDs(), Boards: ids,
	}, nil
}

var kindRank = map[string]int{KindFlow: 0, KindAgent: 1, KindSkill: 2}

func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return kindRank[items[i].Kind] < kindRank[items[j].Kind]
		}
		return items[i].Name < items[j].Name
	})
}
