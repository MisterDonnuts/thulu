package workshop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	SourceWorkshop = "workshop"
	SourceImport   = "import"
)

type Action string

const (
	ActionInstall  Action = "install"
	ActionSame     Action = "same"
	ActionUpdate   Action = "update"
	ActionConflict Action = "conflict"
	ActionReserved Action = "reserved"
	ActionRejected Action = "rejected"
)

type Resolution string

const (
	ResolveInstall Resolution = "install"
	ResolveKeep    Resolution = "keep"
	ResolveUpdate  Resolution = "update"
	ResolveReplace Resolution = "replace"
	ResolveCopy    Resolution = "copy"
	ResolveSkip    Resolution = "skip"
)

type Item struct {
	Kind         string            `json:"kind"`
	Name         string            `json:"name"`
	Action       Action            `json:"action"`
	Resolution   Resolution        `json:"resolution"`
	Options      []Resolution      `json:"options"`
	Target       string            `json:"target"`
	Revision     int               `json:"revision,omitempty"`
	Installed    int               `json:"installed_revision,omitempty"`
	Edited       bool              `json:"edited,omitempty"`
	Disabled     bool              `json:"disabled,omitempty"`
	UsedBy       []string          `json:"used_by,omitempty"`
	AsCopy       bool              `json:"as_copy,omitempty"`
	SkillRenames map[string]string `json:"skill_renames,omitempty"`
}

func (it Item) Key() string { return it.Kind + "/" + it.Name }

const (
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Notice codes are keys the dashboard translates; renaming one needs the dashboard changed too.
const (
	NoticeMissingSkill = "missing_skill"
	NoticeMissingMCP   = "missing_mcp"
	NoticeUnknownStage = "unknown_stage"
	NoticeImage        = "image"
	NoticeImageSaaS    = "image_saas"
	NoticeImageImport  = "image_import"
	NoticeToolsSkipped = "tools_skipped"
	NoticeReserved     = "reserved"
	NoticeKeptSkill    = "kept_skill"
	NoticeSecret       = "secret"
)

type Notice struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Kind     string   `json:"kind,omitempty"`
	Name     string   `json:"name,omitempty"`
	Refs     []string `json:"refs,omitempty"`
}

type Plan struct {
	Source  string   `json:"source"`
	Items   []Item   `json:"items"`
	Notices []Notice `json:"notices"`
	Blocked bool     `json:"blocked"`
	Writes  int      `json:"writes"`

	locale  string
	dropped map[string][]string
}

type State interface {
	Agent(ctx context.Context, name string) (agentstore.Agent, bool, error)
	Files(ctx context.Context, kind, name string) (map[string][]byte, error)
	Origin(ctx context.Context, kind, name string) (agentstore.Origin, bool, error)
	ChangedSince(ctx context.Context, kind, name string, since time.Time) (bool, error)
	IsSystemSkill(ctx context.Context, name string) (bool, error)
}

type Env struct {
	KnownMCP   map[string]bool
	StageKnown func(id string) bool
	SaaS       bool
	Locale     string
}

const baseSkill = "_base"

const systemSkillPrefix = "octopus-"

const maxCopySuffix = 99

type Choices struct {
	Resolutions map[string]Resolution
	Names       map[string]string
}

// A missing or invalid choice falls back to Options[0], which must never overwrite a workspace
// edit.
func MakePlan(ctx context.Context, st State, set *Set, source string, choices Choices, env Env) (*Plan, error) {
	p := &Plan{Source: source, Items: []Item{}, Notices: []Notice{}, locale: env.Locale}
	// Agents first: the skill copies they keep decide what each skill is compared with.
	agents := make([]Item, 0, len(set.Agents))
	for i := range set.Agents {
		it, err := classifyAgent(ctx, st, &set.Agents[i], source)
		if err != nil {
			return nil, err
		}
		agents = append(agents, it)
	}
	for i := range set.Skills {
		sk := &set.Skills[i]
		var users []string
		for j := range set.Agents {
			if contains(SkillRefs(set.Agents[j].Def), sk.Name) {
				users = append(users, set.Agents[j].Def.Name)
			}
		}
		it, err := classifySkill(ctx, st, sk, source, keptCopy(agents, users, sk.Name))
		if err != nil {
			return nil, err
		}
		it.UsedBy = users
		p.Items = append(p.Items, it)
	}
	p.Items = append(p.Items, agents...)
	for _, r := range set.Rejected {
		p.Items = append(p.Items, Item{Kind: r.Kind, Name: r.Name, Target: r.Name, Action: ActionRejected})
	}
	for i := range p.Items {
		choose(&p.Items[i], choices.Resolutions[p.Items[i].Key()])
	}
	if err := assignTargets(ctx, st, set, p, choices.Names); err != nil {
		return nil, err
	}
	if err := p.check(ctx, st, set, env); err != nil {
		return nil, err
	}
	for i := range p.Items {
		if writes(p.Items[i].Resolution) {
			p.Writes++
		}
	}
	return p, nil
}

func classifySkill(ctx context.Context, st State, sk *SkillItem, source string, kept keptSkill) (Item, error) {
	it := Item{Kind: KindSkill, Name: sk.Name, Target: sk.Name, Revision: sk.Revision}
	if kept.to != "" {
		as, ok, err := classifyAsCopy(ctx, st, it, sk, kept)
		if err != nil || ok {
			return as, err
		}
	}
	system, err := st.IsSystemSkill(ctx, sk.Name)
	if err != nil {
		return it, err
	}
	if system || sk.Name == baseSkill || strings.HasPrefix(sk.Name, systemSkillPrefix) {
		it.Action = ActionReserved
		return it, nil
	}
	have, err := st.Files(ctx, agentstore.KindSkill, sk.Name)
	if err != nil {
		return it, err
	}
	if len(have) == 0 {
		it.Action = ActionInstall
		return it, nil
	}
	origin, found, err := st.Origin(ctx, it.Kind, it.Name)
	if err != nil {
		return it, err
	}
	same, err := sameSkill(have, sk.Files, sk.Name, sk.Meta.Descriptions)
	if err != nil {
		return it, err
	}
	return classify(ctx, st, it, origin, found, same, filesHash(have), source)
}

func classifyAgent(ctx context.Context, st State, a *AgentItem, source string) (Item, error) {
	it := Item{Kind: KindAgent, Name: a.Def.Name, Target: a.Def.Name, Revision: a.Revision}
	ag, exists, err := st.Agent(ctx, a.Def.Name)
	if err != nil {
		return it, err
	}
	if !exists || ag.Disabled() {
		it.Action = ActionInstall
		it.Disabled = exists
		if exists {
			return keepDeletedRenames(ctx, st, it)
		}
		return it, nil
	}
	origin, found, err := st.Origin(ctx, it.Kind, it.Name)
	if err != nil {
		return it, err
	}
	have, err := st.Files(ctx, agentstore.KindAgent, a.Def.Name)
	if err != nil {
		return it, err
	}
	want := a.Def
	if found && len(origin.SkillRenames) > 0 {
		it.SkillRenames = origin.SkillRenames
		want = renamedAgent(a.Def, a.Def.Name, origin.SkillRenames)
	}
	// Compare against what Apply would write, or an identical file that names an image always reads
	// as an update.
	want = writtenDef(want, source)
	drop, err := unprovidedReserved(ctx, st, SkillRefs(want))
	if err != nil {
		return it, err
	}
	want = dropSkillRefs(want, drop)
	// The installed description is in the installer's locale; hash the source one so reading the
	// catalog in another locale does not look like an edit.
	cur := agentHash(sourceDescription(ag.AgentType, a), have)
	return classify(ctx, st, it, origin, found, cur == agentHash(want, a.Files), cur, source)
}

type keptSkill struct {
	to      string
	renames map[string]string
}

func keptCopy(agents []Item, users []string, name string) keptSkill {
	var k keptSkill
	for i := range agents {
		it := &agents[i]
		if !contains(users, it.Name) {
			continue
		}
		to := it.SkillRenames[name]
		if to == "" || (k.to != "" && to != k.to) {
			return keptSkill{}
		}
		k.to = to
		if k.renames == nil {
			k.renames = map[string]string{}
		}
		for from, copied := range it.SkillRenames {
			k.renames[from] = copied
		}
	}
	return k
}

func classifyAsCopy(ctx context.Context, st State, it Item, sk *SkillItem, k keptSkill) (Item, bool, error) {
	have, err := st.Files(ctx, agentstore.KindSkill, k.to)
	if err != nil || len(have) == 0 {
		return it, false, err
	}
	meta, err := renameSkillMeta(sk.Files[agentstore.SkillMetaFile], k.to, k.renames)
	if err != nil {
		return it, false, err
	}
	it.Target = k.to
	it.AsCopy = true
	it.Action = ActionConflict
	same, err := sameSkill(have, withFile(sk.Files, agentstore.SkillMetaFile, meta), k.to, sk.Meta.Descriptions)
	if err != nil {
		return it, false, err
	}
	if same {
		it.Action = ActionSame
	}
	return it, true, nil
}

func keepDeletedRenames(ctx context.Context, st State, it Item) (Item, error) {
	origin, found, err := st.Origin(ctx, it.Kind, it.Name)
	if err != nil || !found {
		return it, err
	}
	for from, to := range origin.SkillRenames {
		have, err := st.Files(ctx, agentstore.KindSkill, to)
		if err != nil {
			return it, err
		}
		if len(have) == 0 {
			continue
		}
		if it.SkillRenames == nil {
			it.SkillRenames = map[string]string{}
		}
		it.SkillRenames[from] = to
	}
	return it, nil
}

func sourceDescription(def types.AgentType, a *AgentItem) types.AgentType {
	for _, d := range a.Descriptions {
		if d != "" && def.Description == d {
			def.Description = a.Def.Description
			return def
		}
	}
	return def
}

// An agent written still naming an unprovided octopus- skill would silently pick up whatever system
// skill Thulu ships under that name later.
func unprovidedReserved(ctx context.Context, st State, refs []string) ([]string, error) {
	var out []string
	for _, ref := range refs {
		if !strings.HasPrefix(ref, systemSkillPrefix) {
			continue
		}
		sys, err := st.IsSystemSkill(ctx, ref)
		if err != nil {
			return nil, err
		}
		if !sys {
			out = append(out, ref)
		}
	}
	return out, nil
}

func dropSkillRefs(def types.AgentType, drop []string) types.AgentType {
	if len(drop) == 0 {
		return def
	}
	def.SkillsByStage = withoutRefs(def.SkillsByStage, drop)
	if def.Auditor != nil {
		aud := *def.Auditor
		aud.SkillsByStage = withoutRefs(aud.SkillsByStage, drop)
		def.Auditor = &aud
	}
	return def
}

func withoutRefs(byStage map[string][]string, drop []string) map[string][]string {
	if byStage == nil {
		return nil
	}
	out := make(map[string][]string, len(byStage))
	for stage, names := range byStage {
		var list []string
		for _, n := range names {
			if !contains(drop, strings.TrimSpace(n)) {
				list = append(list, n)
			}
		}
		if len(list) > 0 {
			out[stage] = list
		}
	}
	return out
}

// Imports update whenever the file differs: files carry no revision anyone keeps.
func classify(ctx context.Context, st State, it Item, origin agentstore.Origin, found, same bool, cur, source string) (Item, error) {
	tracked := found && (origin.Origin == SourceWorkshop || origin.Origin == SourceImport)
	if tracked {
		if origin.Origin == SourceWorkshop {
			it.Installed = origin.Revision
		}
		edited, err := editedSince(ctx, st, it.Kind, it.Name, origin, cur)
		if err != nil {
			return it, err
		}
		it.Edited = edited
	}
	switch {
	case same:
		it.Action = ActionSame
	case tracked && origin.Origin == source && !it.Edited &&
		(source == SourceImport || it.Revision > it.Installed):
		it.Action = ActionUpdate
	default:
		it.Action = ActionConflict
	}
	return it, nil
}

// Origins recorded before Hash existed have it empty; fall back to write timestamps.
func editedSince(ctx context.Context, st State, kind, name string, origin agentstore.Origin, cur string) (bool, error) {
	if origin.Hash != "" {
		return origin.Hash != cur, nil
	}
	return st.ChangedSince(ctx, kind, name, origin.CopiedAt)
}

func choose(it *Item, picked Resolution) {
	switch it.Action {
	case ActionInstall:
		it.Options = []Resolution{ResolveInstall, ResolveSkip}
	case ActionSame:
		it.Options = []Resolution{ResolveKeep}
	case ActionUpdate:
		it.Options = []Resolution{ResolveUpdate, ResolveKeep, ResolveCopy}
	case ActionConflict:
		it.Options = []Resolution{ResolveKeep, ResolveReplace, ResolveCopy}
		if it.AsCopy {
			// A second copy would leave the agents on this copy.
			it.Options = []Resolution{ResolveKeep, ResolveReplace}
		}
	case ActionReserved:
		it.Options = []Resolution{ResolveSkip, ResolveCopy}
	case ActionRejected:
		it.Options = []Resolution{ResolveSkip}
	}
	it.Resolution = it.Options[0]
	for _, o := range it.Options {
		if o == picked {
			it.Resolution = picked
		}
	}
}

func writes(r Resolution) bool {
	return r == ResolveInstall || r == ResolveUpdate || r == ResolveReplace || r == ResolveCopy
}

func assignTargets(ctx context.Context, st State, set *Set, p *Plan, names map[string]string) error {
	taken := map[string]bool{}
	for i := range set.Agents {
		taken[KindAgent+"/"+set.Agents[i].Def.Name] = true
	}
	for i := range set.Skills {
		taken[KindSkill+"/"+set.Skills[i].Name] = true
	}
	for i := range p.Items {
		it := &p.Items[i]
		if it.Resolution != ResolveCopy {
			continue
		}
		if typed := strings.TrimSpace(names[it.Key()]); typed != "" && typed != it.Name {
			if err := checkCopyName(ctx, st, it, typed, taken); err != nil {
				return err
			}
			it.Target = typed
			taken[it.Kind+"/"+typed] = true
			continue
		}
		target, err := freeCopyName(ctx, st, it, taken)
		if err != nil {
			return err
		}
		it.Target = target
		taken[it.Kind+"/"+target] = true
	}
	return nil
}

func copyNameCandidates(it *Item) []string {
	var cands []string
	if it.Action == ActionReserved {
		if base := strings.TrimPrefix(it.Name, systemSkillPrefix); base != "" && base != it.Name {
			cands = append(cands, base)
		}
	}
	stem := it.Name
	if len(cands) > 0 {
		stem = cands[0]
	}
	for n := 2; n <= maxCopySuffix; n++ {
		cands = append(cands, fmt.Sprintf("%s-%d", stem, n))
	}
	return cands
}

func freeCopyName(ctx context.Context, st State, it *Item, taken map[string]bool) (string, error) {
	for _, cand := range copyNameCandidates(it) {
		if len(cand) > 64 || taken[it.Kind+"/"+cand] || reservedSkill(it.Kind, cand) {
			continue
		}
		free, err := nameFree(ctx, st, it.Kind, cand)
		if err != nil {
			return "", err
		}
		if free {
			return cand, nil
		}
	}
	return "", apperr.Conflict(kindKey(it.Kind, "workshop.errors.agent_no_free_name", "workshop.errors.skill_no_free_name"), "name", it.Name)
}

func checkCopyName(ctx context.Context, st State, it *Item, name string, taken map[string]bool) error {
	valid := agentstore.ValidateName(name)
	if it.Kind == KindAgent {
		valid = agentstore.ValidateAgentName(name)
	}
	if valid != nil || reservedSkill(it.Kind, name) {
		return apperr.Invalid(
			kindKey(it.Kind, "workshop.errors.agent_copy_name_invalid", "workshop.errors.skill_copy_name_invalid"),
			"name", it.Name, "target", name)
	}
	free := !taken[it.Kind+"/"+name]
	if free {
		var err error
		if free, err = nameFree(ctx, st, it.Kind, name); err != nil {
			return err
		}
	}
	if it.Kind == KindSkill && free {
		sys, err := st.IsSystemSkill(ctx, name)
		if err != nil {
			return err
		}
		free = !sys
	}
	if !free {
		return apperr.Conflict(kindKey(it.Kind, "workshop.errors.agent_copy_name_taken", "workshop.errors.skill_copy_name_taken"), "target", name)
	}
	return nil
}

func reservedSkill(kind, name string) bool {
	return kind == KindSkill && (name == baseSkill || strings.HasPrefix(name, systemSkillPrefix))
}

// A disabled agent still holds its name: tasks keep pointing at it.
func nameFree(ctx context.Context, st State, kind, name string) (bool, error) {
	if kind == KindAgent {
		_, exists, err := st.Agent(ctx, name)
		return !exists, err
	}
	have, err := st.Files(ctx, agentstore.KindSkill, name)
	return len(have) == 0, err
}

func (p *Plan) renames() map[string]string {
	out := map[string]string{}
	for i := range p.Items {
		it := &p.Items[i]
		if it.Kind == KindSkill && (it.Resolution == ResolveCopy || it.AsCopy) {
			out[it.Name] = it.Target
		}
	}
	return out
}

func (p *Plan) item(kind, name string) (Item, bool) {
	for i := range p.Items {
		if it := &p.Items[i]; it.Kind == kind && it.Name == name {
			return *it, true
		}
	}
	return Item{}, false
}

func (p *Plan) check(ctx context.Context, st State, set *Set, env Env) error {
	if set.SkippedTools > 0 {
		p.notice(Notice{Code: NoticeToolsSkipped, Severity: SeverityWarning, Refs: []string{fmt.Sprint(set.SkippedTools)}})
	}
	for i := range p.Items {
		if it := &p.Items[i]; it.Action == ActionReserved && it.Resolution == ResolveSkip {
			p.notice(Notice{Code: NoticeReserved, Severity: SeverityWarning, Kind: KindSkill, Name: it.Name})
		}
	}
	for _, r := range set.Rejected {
		p.notice(Notice{Code: NoticeSecret, Severity: SeverityWarning, Kind: r.Kind, Name: r.Name, Refs: []string{r.Path}})
	}
	copies := p.renames()
	for i := range set.Skills {
		sk := &set.Skills[i]
		if it, _ := p.item(KindSkill, sk.Name); !writes(it.Resolution) {
			continue
		}
		if err := p.checkSkills(ctx, st, set, KindSkill, sk.Name, sk.Meta.Requires, copies); err != nil {
			return err
		}
	}
	for i := range set.Agents {
		if err := p.checkAgent(ctx, st, set, &set.Agents[i], copies, env); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plan) checkAgent(ctx context.Context, st State, set *Set, a *AgentItem, copies map[string]string, env Env) error {
	it, _ := p.item(KindAgent, a.Def.Name)
	if !writes(it.Resolution) {
		return nil
	}
	renames := agentRenames(it, copies)
	if err := p.checkSkills(ctx, st, set, KindAgent, a.Def.Name, SkillRefs(a.Def), renames); err != nil {
		return err
	}
	drop, err := unprovidedReserved(ctx, st, SkillRefs(renamedAgent(a.Def, a.Def.Name, renames)))
	if err != nil {
		return err
	}
	if len(drop) > 0 {
		if p.dropped == nil {
			p.dropped = map[string][]string{}
		}
		p.dropped[a.Def.Name] = drop
	}
	p.checkKeptSkills(a, renames)
	p.checkMCP(&a.Def, env)
	p.checkStages(&a.Def, env)
	p.checkImage(&a.Def, env)
	return nil
}

func agentRenames(it Item, copies map[string]string) map[string]string {
	out := map[string]string{}
	for from, to := range it.SkillRenames {
		out[from] = to
	}
	for from, to := range copies {
		out[from] = to
	}
	return out
}

func (p *Plan) checkSkills(ctx context.Context, st State, set *Set, kind, name string, refs []string, renames map[string]string) error {
	var out []string
	for _, ref := range refs {
		ok, err := p.skillProvided(ctx, st, set, ref, renames)
		if err != nil {
			return err
		}
		if !ok {
			out = append(out, ref)
		}
	}
	if len(out) > 0 {
		p.notice(Notice{Code: NoticeMissingSkill, Severity: SeverityWarning, Kind: kind, Name: name, Refs: out})
	}
	return nil
}

func (p *Plan) skillProvided(ctx context.Context, st State, set *Set, ref string, renames map[string]string) (bool, error) {
	if ref == baseSkill {
		return true, nil
	}
	target := ref
	if to, ok := renames[ref]; ok {
		target = to
	}
	if _, ok := set.Skill(ref); ok {
		if it, _ := p.item(KindSkill, ref); writes(it.Resolution) && it.Target == target {
			return true, nil
		}
	}
	if sys, err := st.IsSystemSkill(ctx, target); err != nil || sys {
		return sys, err
	}
	have, err := st.Files(ctx, agentstore.KindSkill, target)
	return len(have) > 0, err
}

func (p *Plan) checkKeptSkills(a *AgentItem, renames map[string]string) {
	for _, ref := range SkillRefs(a.Def) {
		if _, renamed := renames[ref]; renamed {
			continue
		}
		it, ok := p.item(KindSkill, ref)
		if !ok || it.Resolution != ResolveKeep || (it.Action != ActionConflict && it.Action != ActionUpdate) {
			continue
		}
		p.notice(Notice{Code: NoticeKeptSkill, Severity: SeverityWarning, Kind: KindAgent, Name: a.Def.Name, Refs: []string{ref}})
	}
}

func (p *Plan) checkMCP(def *types.AgentType, env Env) {
	if env.KnownMCP == nil {
		return
	}
	var out []string
	for _, m := range MCPRefs(*def) {
		if !env.KnownMCP[m] {
			out = append(out, m)
		}
	}
	if len(out) > 0 {
		p.notice(Notice{Code: NoticeMissingMCP, Severity: SeverityWarning, Kind: KindAgent, Name: def.Name, Refs: out})
	}
}

func (p *Plan) checkStages(def *types.AgentType, env Env) {
	if env.StageKnown == nil {
		return
	}
	var out []string
	for _, s := range StageRefs(*def) {
		if !isVirtualStage(s) && !env.StageKnown(s) {
			out = append(out, s)
		}
	}
	if len(out) > 0 {
		p.notice(Notice{Code: NoticeUnknownStage, Severity: SeverityWarning, Kind: KindAgent, Name: def.Name, Refs: out})
	}
}

// The image is what the daemon starts on its own Docker host and no HTTP route lets a workspace set
// it, so imports drop it (writtenDef). Catalog images are refused in SaaS.
func (p *Plan) checkImage(def *types.AgentType, env Env) {
	if def.Image == "" {
		return
	}
	n := Notice{Code: NoticeImage, Severity: SeverityWarning, Kind: KindAgent, Name: def.Name, Refs: []string{def.Image}}
	switch {
	case p.Source != SourceWorkshop:
		n.Code = NoticeImageImport
	case env.SaaS:
		n.Code, n.Severity = NoticeImageSaaS, SeverityError
	}
	p.notice(n)
}

func isVirtualStage(id string) bool {
	return id == string(types.StageMention) || id == string(types.StageChat)
}

func (p *Plan) notice(n Notice) {
	p.Notices = append(p.Notices, n)
	if n.Severity == SeverityError {
		p.Blocked = true
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

type Result struct {
	Installed []Ref    `json:"installed"`
	Updated   []Ref    `json:"updated"`
	Replaced  []Ref    `json:"replaced"`
	Copied    []Copied `json:"copied"`
	Kept      []Ref    `json:"kept"`
	Skipped   []Ref    `json:"skipped"`
}

type Copied struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
}

var ErrBlocked error = apperr.Unprocessable("workshop.errors.plan_blocked")

func Apply(ctx context.Context, store *agentstore.Store, tenant string, set *Set, p *Plan, actor string) (Result, error) {
	w := &applier{ctx: ctx, store: store, tenant: tenant, plan: p, actor: actor, renames: p.renames(),
		res: Result{Installed: []Ref{}, Updated: []Ref{}, Replaced: []Ref{}, Copied: []Copied{}, Kept: []Ref{}, Skipped: []Ref{}}}
	if p.Blocked {
		return w.res, ErrBlocked
	}
	err := store.InTx(ctx, func(tx *agentstore.Tx) error {
		for i := range set.Skills {
			if err := w.skill(tx, &set.Skills[i]); err != nil {
				return err
			}
		}
		for i := range set.Agents {
			if err := w.agent(tx, &set.Agents[i]); err != nil {
				return err
			}
		}
		return nil
	})
	for _, r := range set.Rejected {
		w.res.Skipped = append(w.res.Skipped, Ref{Kind: r.Kind, Name: r.Name})
	}
	return w.res, err
}

type applier struct {
	ctx     context.Context
	store   *agentstore.Store
	tenant  string
	plan    *Plan
	actor   string
	renames map[string]string
	res     Result
}

func (w *applier) skill(tx *agentstore.Tx, sk *SkillItem) error {
	it, _ := w.plan.item(KindSkill, sk.Name)
	if !writes(it.Resolution) {
		w.res.note(it)
		return nil
	}
	files := sk.Files
	if it.Target != sk.Name || needsRequiresRewrite(sk.Meta.Requires, w.renames) {
		meta, err := renameSkillMeta(files[agentstore.SkillMetaFile], it.Target, w.renames)
		if err != nil {
			return err
		}
		files = withFile(files, agentstore.SkillMetaFile, meta)
	}
	// The origin hash is of the localized files actually written; sameSkill matches that in any
	// locale the skill carries.
	files, err := skillFilesIn(files, it.Target, w.plan.locale)
	if err != nil {
		return err
	}
	if err := writeOwner(w.ctx, w.store, tx, w.tenant, agentstore.KindSkill, it.Target, files, w.actor); err != nil {
		return err
	}
	if err := recordOrigin(tx, w.tenant, agentstore.KindSkill, it, w.plan.Source, filesHash(files), nil); err != nil {
		return err
	}
	w.res.note(it)
	return nil
}

func (w *applier) agent(tx *agentstore.Tx, a *AgentItem) error {
	it, _ := w.plan.item(KindAgent, a.Def.Name)
	if !writes(it.Resolution) {
		w.res.note(it)
		return nil
	}
	renames := usedRenames(a.Def, agentRenames(it, w.renames))
	def := dropSkillRefs(writtenDef(renamedAgent(a.Def, it.Target, renames), w.plan.Source),
		w.plan.dropped[a.Def.Name])
	// Hash with the source description (see sourceDescription), then localize the row.
	hash := agentHash(def, a.Files)
	def.Description = localized(def.Description, a.Descriptions, w.plan.locale)
	if err := tx.SnapshotAgent(w.tenant, it.Target, agentstore.DefReasonWorkshop, w.actor); err != nil {
		return err
	}
	if _, err := tx.PutAgent(w.tenant, agentstore.Agent{AgentType: def}, w.actor); err != nil {
		return err
	}
	if err := writeOwner(w.ctx, w.store, tx, w.tenant, agentstore.KindAgent, it.Target, a.Files, w.actor); err != nil {
		return err
	}
	if err := recordOrigin(tx, w.tenant, agentstore.KindAgent, it, w.plan.Source, hash, renames); err != nil {
		return err
	}
	w.res.note(it)
	return nil
}

func usedRenames(def types.AgentType, renames map[string]string) map[string]string {
	var out map[string]string
	for _, ref := range SkillRefs(def) {
		if to, ok := renames[ref]; ok {
			if out == nil {
				out = map[string]string{}
			}
			out[ref] = to
		}
	}
	return out
}

func (r *Result) note(it Item) {
	ref := Ref{Kind: it.Kind, Name: it.Target}
	switch it.Resolution {
	case ResolveInstall:
		r.Installed = append(r.Installed, ref)
	case ResolveUpdate:
		r.Updated = append(r.Updated, ref)
	case ResolveReplace:
		r.Replaced = append(r.Replaced, ref)
	case ResolveCopy:
		r.Copied = append(r.Copied, Copied{Kind: it.Kind, From: it.Name, To: it.Target})
	case ResolveKeep:
		r.Kept = append(r.Kept, Ref{Kind: it.Kind, Name: it.Name})
	case ResolveSkip:
		r.Skipped = append(r.Skipped, Ref{Kind: it.Kind, Name: it.Name})
	}
}

func writeOwner(ctx context.Context, store *agentstore.Store, tx *agentstore.Tx,
	tenant, kind, owner string, files map[string][]byte, actor string,
) error {
	have, err := store.ListOwner(ctx, tenant, kind, owner)
	if err != nil {
		return err
	}
	for i := range have {
		f := &have[i]
		if _, keep := files[f.Path]; keep {
			continue
		}
		if err := tx.DeleteFile(tenant, kind, owner, f.Path, actor); err != nil && !errors.Is(err, agentstore.ErrNotFound) {
			return err
		}
	}
	for _, path := range sortedPaths(files) {
		if _, err := tx.PutFile(tenant, kind, owner, path, files[path], actor); err != nil {
			return apperr.Wrap(err, kindKey(kind, "workshop.errors.agent_write_file", "workshop.errors.skill_write_file"),
				"name", owner, "path", path)
		}
	}
	return nil
}

func recordOrigin(tx *agentstore.Tx, tenant, kind string, it Item, source, hash string, renames map[string]string) error {
	if it.Resolution == ResolveCopy || it.AsCopy {
		return tx.ForgetOrigin(tenant, kind, it.Target)
	}
	// Stamped after the writes so their revisions fall at or before CopiedAt (ChangedSince relies
	// on it).
	return tx.SetOrigin(tenant, kind, it.Target, agentstore.Origin{
		Origin: source, Revision: it.Revision, CopiedAt: time.Now().UTC(),
		Hash: hash, SkillRenames: renames,
	})
}

func needsRequiresRewrite(requires []string, renames map[string]string) bool {
	for _, r := range requires {
		if _, ok := renames[r]; ok {
			return true
		}
	}
	return false
}

func withFile(files map[string][]byte, path string, body []byte) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for k, v := range files {
		out[k] = v
	}
	out[path] = body
	return out
}

// A file must not choose what image the daemon starts; see checkImage.
func writtenDef(def types.AgentType, source string) types.AgentType {
	if source != SourceWorkshop {
		def.Image = ""
	}
	return def
}

func renamedAgent(def types.AgentType, target string, renames map[string]string) types.AgentType {
	if def.Type == def.Name {
		def.Type = target
	}
	def.Name = target
	def.SkillsByStage = renameRefs(def.SkillsByStage, renames)
	if def.Auditor != nil {
		aud := *def.Auditor
		aud.SkillsByStage = renameRefs(aud.SkillsByStage, renames)
		def.Auditor = &aud
	}
	return def
}

func renameRefs(byStage map[string][]string, renames map[string]string) map[string][]string {
	if len(renames) == 0 || byStage == nil {
		return byStage
	}
	out := make(map[string][]string, len(byStage))
	for stage, names := range byStage {
		list := make([]string, len(names))
		for i, n := range names {
			if to, ok := renames[strings.TrimSpace(n)]; ok {
				list[i] = to
			} else {
				list[i] = n
			}
		}
		out[stage] = list
	}
	return out
}

func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind == KindAgent
		}
		return items[i].Name < items[j].Name
	})
}
