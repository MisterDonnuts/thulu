package workshop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"sort"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A flow installs a board: its columns with their flags, the task types with the stages each
// skips, and the agent of each column and of each type. The agents and skills it names install
// with it. The board remembers the flow, its revision and the hash of what was written, so a later
// catalog revision reads as an update and a board edited since reads as a conflict, the same as
// agents and skills.

const KindFlow = "flow"

const flowFile = "flow.json"

type FlowItem struct {
	Name         string
	Revision     int
	Tags         []string
	Label        string
	Labels       map[string]string
	Description  string
	Descriptions map[string]string
	Board        FlowBoardDef
	Stages       []FlowStage
	Types        []FlowType
	// flow.json as shipped, for the Workshop's detail view.
	Raw []byte
}

type FlowBoardDef struct {
	ID           string `json:"id"`
	MaxKickBacks int    `json:"max_kick_backs,omitempty"`
}

// A column. The label comes from labels[locale], then labels["en"], then the stage catalog
// (stagepkg.labels.<id>), so ids the daemon already names need no labels here.
type FlowStage struct {
	types.StageInfo
	Labels map[string]string `json:"labels,omitempty"`
	// Shadows StageInfo's field so each approval can carry its labels per locale.
	RequiredApprovals []FlowApproval `json:"required_approvals,omitempty"`
}

// Not types.ApprovalRequirement: its UnmarshalJSON would swallow the labels.
type FlowApproval struct {
	Key            string            `json:"key"`
	Label          string            `json:"label"`
	Labels         map[string]string `json:"labels,omitempty"`
	TaskTypes      []string          `json:"task_types,omitempty"`
	ResetOnChange  *bool             `json:"reset_on_change,omitempty"`
	RequiredChecks bool              `json:"required_checks,omitempty"`
}

type FlowType struct {
	stagepkg.TaskTypeInfo
	Labels map[string]string `json:"labels,omitempty"`
}

type flowManifest struct {
	Revision     int               `json:"revision"`
	Tags         []string          `json:"tags"`
	Label        string            `json:"label"`
	Labels       map[string]string `json:"labels"`
	Description  string            `json:"description"`
	Descriptions map[string]string `json:"descriptions"`
	Board        FlowBoardDef      `json:"board"`
	Stages       []FlowStage       `json:"stages"`
	TaskTypes    []FlowType        `json:"task_types"`
}

func loadFlow(fsys fs.FS, name string) (FlowItem, error) {
	broken := apperr.Invalid("workshop.errors.catalog_flow", "name", name)
	raw, err := fs.ReadFile(fsys, path.Join("flows", name, flowFile))
	if err != nil {
		return FlowItem{}, broken
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m flowManifest
	if dec.Decode(&m) != nil || m.Revision < 1 || m.Label == "" {
		return FlowItem{}, broken
	}
	f := FlowItem{
		Name: name, Revision: m.Revision, Tags: m.Tags, Label: m.Label, Labels: m.Labels,
		Description: m.Description, Descriptions: m.Descriptions, Board: m.Board,
		Stages: m.Stages, Types: m.TaskTypes, Raw: raw,
	}
	if err := f.validate(); err != nil {
		return FlowItem{}, apperr.Wrap(err, "workshop.errors.catalog_flow", "name", name)
	}
	return f, nil
}

// The shape only; whether the agents exist is checked against the whole catalog (checkFlows).
func (f *FlowItem) validate() error {
	spec := f.Spec(i18n.SourceLocale, nil, nil)
	info := spec.Info
	info.WorkspaceID = "catalog"
	if err := stagepkg.ValidateBoardInfo(info); err != nil {
		return err
	}
	if err := stagepkg.ValidateBoard(spec.Stages); err != nil {
		return err
	}
	return stagepkg.ValidateBoardTypes(spec.Stages, spec.Types)
}

// AgentNames lists every agent the flow assigns, columns first, without repeats.
func (f *FlowItem) AgentNames() []string {
	var out []string
	add := func(name string) {
		if name != "" && !contains(out, name) {
			out = append(out, name)
		}
	}
	for i := range f.Stages {
		add(f.Stages[i].DefaultAgent)
	}
	for i := range f.Types {
		for _, st := range sortedStages(f.Types[i].Agents) {
			add(f.Types[i].Agents[st])
		}
	}
	return out
}

func (f *FlowItem) StageIDs() []string {
	out := make([]string, len(f.Stages))
	for i := range f.Stages {
		out[i] = string(f.Stages[i].ID)
	}
	return out
}

// Every agent a flow assigns must ship in the catalog and accept the stage it gets.
func (s *Set) checkFlows() error {
	for i := range s.Flows {
		f := &s.Flows[i]
		for _, use := range f.agentUses() {
			a, ok := s.Agent(use.agent)
			if !ok {
				return apperr.Invalid("workshop.errors.flow_unknown_agent", "flow", f.Name, "agent", use.agent)
			}
			if len(a.Def.ValidStages) > 0 && !a.Def.SupportsStage(use.stage) {
				return apperr.Invalid("workshop.errors.flow_agent_stage", "flow", f.Name, "agent", use.agent, "stage", use.stage)
			}
		}
	}
	return nil
}

type agentUse struct {
	agent string
	stage types.Stage
}

func (f *FlowItem) agentUses() []agentUse {
	var out []agentUse
	for i := range f.Stages {
		if a := f.Stages[i].DefaultAgent; a != "" {
			out = append(out, agentUse{a, f.Stages[i].ID})
		}
	}
	for i := range f.Types {
		for _, st := range sortedStages(f.Types[i].Agents) {
			if a := f.Types[i].Agents[st]; a != "" {
				out = append(out, agentUse{a, st})
			}
		}
	}
	return out
}

func sortedStages(m map[types.Stage]string) []types.Stage {
	out := make([]types.Stage, 0, len(m))
	for st := range m {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FlowSpec is the board a flow writes, in one locale.
type FlowSpec struct {
	Info   stagepkg.BoardInfo
	Stages []types.StageInfo
	Types  []stagepkg.TaskTypeInfo
}

// Spec renders the flow in locale. renames maps a catalog agent to the name it was installed as
// (a copy); an agent in missing is not in the workspace, so its columns and types get none.
func (f *FlowItem) Spec(locale string, renames map[string]string, missing map[string]bool) FlowSpec {
	agent := func(name string) string {
		if missing[name] {
			return ""
		}
		if to, ok := renames[name]; ok {
			return to
		}
		return name
	}
	spec := FlowSpec{Info: stagepkg.BoardInfo{
		ID: f.Board.ID, Label: pick(f.Labels, locale, f.Label),
		Description: pick(f.Descriptions, locale, f.Description), MaxKickBacks: f.Board.MaxKickBacks,
		Flow: &stagepkg.BoardFlow{Name: f.Name, Revision: f.Revision},
	}}
	for i := range f.Stages {
		st := f.Stages[i].render(locale, agent)
		st.Position = (i + 1) * 10
		spec.Stages = append(spec.Stages, st)
	}
	for i := range f.Types {
		spec.Types = append(spec.Types, f.Types[i].render(locale, agent, missing))
	}
	spec.Info.Flow.Hash = FlowHash(spec.Info.MaxKickBacks, spec.Stages, spec.Types)
	return spec
}

func (fst *FlowStage) render(locale string, agent func(string) string) types.StageInfo {
	st := fst.StageInfo
	st.Label = stageLabel(fst.Labels, locale, st.ID)
	st.DefaultAgent = agent(st.DefaultAgent)
	st.RequiredApprovals = nil
	for _, a := range fst.RequiredApprovals {
		req := types.ApprovalRequirement{
			Key: a.Key, Label: pick(a.Labels, locale, a.Label), TaskTypes: a.TaskTypes, ResetOnChange: true,
			RequiredChecks: a.RequiredChecks,
		}
		if a.ResetOnChange != nil {
			req.ResetOnChange = *a.ResetOnChange
		}
		st.RequiredApprovals = append(st.RequiredApprovals, req)
	}
	return st
}

func (ft *FlowType) render(locale string, agent func(string) string, missing map[string]bool) stagepkg.TaskTypeInfo {
	t := ft.TaskTypeInfo
	t.Label = pick(ft.Labels, locale, t.Label)
	t.Skips = append([]types.Stage{}, t.Skips...)
	t.Agents = nil
	for st, name := range ft.Agents {
		// A type that names a missing agent leaves the column to its default rather than
		// clearing it: the person skipped installing the agent, not the column.
		if name != "" && missing[name] {
			continue
		}
		if t.Agents == nil {
			t.Agents = map[types.Stage]string{}
		}
		t.Agents[st] = agent(name)
	}
	return t
}

func pick(byLocale map[string]string, locale, fallback string) string {
	if v := byLocale[locale]; v != "" {
		return v
	}
	if v := byLocale[i18n.SourceLocale]; v != "" {
		return v
	}
	return fallback
}

func stageLabel(byLocale map[string]string, locale string, id types.Stage) string {
	if v := pick(byLocale, locale, ""); v != "" {
		return v
	}
	if v, ok := stagepkg.SeededName(locale, id); ok {
		return v
	}
	return string(id)
}

// FlowHash covers what decides how tasks flow: column ids and order, flags, approvals, default
// agents, the kick-back cap, task types with their skips and agents. Labels, colors and icons are
// left out, so renaming a column is not an edit that blocks the next update.
func FlowHash(maxKickBacks int, stages []types.StageInfo, tt []stagepkg.TaskTypeInfo) string {
	type typeShape struct {
		ID      string                 `json:"id"`
		Default bool                   `json:"default,omitempty"`
		Skips   []string               `json:"skips,omitempty"`
		Agents  map[types.Stage]string `json:"agents,omitempty"`
	}
	shape := struct {
		MaxKickBacks int               `json:"max_kick_backs"`
		Stages       []types.StageInfo `json:"stages"`
		Types        []typeShape       `json:"types"`
	}{MaxKickBacks: maxKickBacks, Stages: []types.StageInfo{}, Types: []typeShape{}}
	for i := range stages {
		shape.Stages = append(shape.Stages, stageShape(stages[i]))
	}
	for i := range tt {
		ty := &tt[i]
		skips := make([]string, 0, len(ty.Skips))
		for _, sk := range ty.Skips {
			skips = append(skips, string(sk))
		}
		sort.Strings(skips)
		agents := ty.Agents
		if len(agents) == 0 {
			agents = nil
		}
		shape.Types = append(shape.Types, typeShape{ID: ty.ID, Default: ty.Default, Skips: skips, Agents: agents})
	}
	// Same fallback as defHash: these types always marshal.
	raw, err := json.Marshal(shape)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// What of a column decides how tasks flow: the column without its label, color and position.
func stageShape(st types.StageInfo) types.StageInfo {
	st.Label, st.Color, st.Position, st.Virtual = "", "", 0, false
	approvals := make([]types.ApprovalRequirement, 0, len(st.RequiredApprovals))
	for _, a := range st.RequiredApprovals {
		a.Label = ""
		approvals = append(approvals, a)
	}
	st.RequiredApprovals = approvals
	return st
}

func (c *Catalog) Flows() []FlowItem { return c.set.Flows }

func (c *Catalog) Flow(name string) (FlowItem, bool) { return c.set.Flow(name) }

func (s *Set) Flow(name string) (FlowItem, bool) {
	for i := range s.Flows {
		if s.Flows[i].Name == name {
			return s.Flows[i], true
		}
	}
	return FlowItem{}, false
}
