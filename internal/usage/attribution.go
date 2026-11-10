package usage

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

// Raise when the algorithm changes: sessions attributed by an older version are recomputed from
// their logs at startup (ReattributeSessions).
const AttributionVersion = 2

// Persisted in session_cost_breakdown.category; ByTokenOptimizer's SQL names 'tools' and
// 'delegation' literally.
const (
	CategoryStartup      = "startup"
	CategoryReasoning    = "reasoning"
	CategoryUserInput    = "user_input"
	CategoryTools        = "tools"
	CategorySubagents    = "subagents"
	CategoryCompaction   = "compaction"
	CategoryDelegation   = "delegation"
	CategoryUnattributed = "unattributed"
)

var Categories = []string{
	CategoryStartup, CategoryReasoning, CategoryUserInput, CategoryTools,
	CategorySubagents, CategoryCompaction, CategoryDelegation, CategoryUnattributed,
}

// Must match the stderr line octopus-delegate prints after every call.
var delegationMarker = regexp.MustCompile(`octopus-delegate: id=(dlg_[0-9a-f]+)`) //i18n:ignore machine-read marker

type PriceLookup func(model string) (Pricing, bool)

type AttributionInput struct {
	Events  []engine.Event
	Model   string
	CostUSD float64
	// Per-call output figures in the stream are placeholders printed before the model finished
	// writing; use the result events' total.
	OutputTokens int
	Prices       PriceLookup
}

type Share struct {
	WeightedTokens float64 `json:"weighted_tokens"`
	CostUSD        float64 `json:"cost_usd"`
}

type Call struct {
	Idx             int
	MessageID       string
	Model           string
	ParentToolUseID string
	Usage           engine.Usage
	PromptSize      int
	Compaction      bool
	TS              time.Time
}

type ToolCost struct {
	ToolUseID       string
	ParentToolUseID string
	ResultBytes     int
	ResultTokens    int
	ArgsTokens      int
	CarriedCalls    int
	Share
	IsError      bool
	DelegationID string
}

type Attribution struct {
	Calls           []Call
	Tools           map[string]*ToolCost
	Categories      map[string]Share
	BaseTokens      int
	Compactions     int
	UnattributedUSD float64
}

type owner struct {
	tool     string
	category string
}

type segment struct {
	owner
	tokens int
}

type pendingPiece struct {
	owner
	bytes int
}

type weights struct{ in, out, read, write5m, write1h float64 }

// Anthropic's pricing ratios relative to input.
var defaultWeights = weights{in: 1, out: 5, read: 0.1, write5m: 1.25, write1h: 2}

func Attribute(in AttributionInput) Attribution {
	a := attributor{
		in:        in,
		out:       Attribution{Tools: map[string]*ToolCost{}, Categories: map[string]Share{}},
		baseIn:    1,
		callByID:  map[string]int{},
		outBlocks: map[int][]pendingPiece{},
	}
	if p, ok := a.price(in.Model); ok && p.InPerMTok > 0 {
		a.baseIn = p.InPerMTok
	}
	a.walk()
	a.charge()
	a.reconcile()
	return a.out
}

type attributor struct {
	in     AttributionInput
	out    Attribution
	baseIn float64

	callByID  map[string]int
	segs      []segment
	pending   []pendingPiece
	lastMain  int
	outBlocks map[int][]pendingPiece
	callSegs  map[int][]segment
	equiv     map[owner]float64
}

func (a *attributor) price(model string) (Pricing, bool) {
	if a.in.Prices == nil || model == "" {
		return Pricing{}, false
	}
	return a.in.Prices(model)
}

func (a *attributor) weightsFor(model string) weights {
	p, ok := a.price(model)
	if !ok || p.InPerMTok <= 0 {
		if model == "" || model == a.in.Model {
			return defaultWeights
		}
		return a.weightsFor(a.in.Model)
	}
	w := weights{
		in:      p.InPerMTok / a.baseIn,
		out:     p.OutPerMTok / a.baseIn,
		read:    p.CacheReadPerMTok / a.baseIn,
		write5m: p.CacheCreatePerMTok / a.baseIn,
	}
	// model_pricing has only the five-minute cache-write rate; the one-hour write is derived (2x vs
	// 1.25x input).
	w.write1h = w.write5m * defaultWeights.write1h / defaultWeights.write5m
	if w.out == 0 {
		w.out = w.in * defaultWeights.out
	}
	return w
}

func (a *attributor) walk() {
	a.lastMain = -1
	a.callSegs = map[int][]segment{}
	for i := range a.in.Events {
		ev := &a.in.Events[i]
		switch ev.Kind {
		case engine.EventAssistant:
			a.onAssistant(ev)
		case engine.EventToolUse:
			a.onToolUse(ev)
		case engine.EventToolResult:
			a.onToolResult(ev)
		case engine.EventUserMessage:
			if ev.ParentToolUseID == "" {
				a.pending = append(a.pending, pendingPiece{owner: a.userMessageOwner(ev), bytes: ev.ResultBytes})
			}
		case engine.EventUnknown, engine.EventInit, engine.EventRateLimit, engine.EventResult:
		}
	}
}

func (a *attributor) onAssistant(ev *engine.Event) {
	if ev.Usage == nil {
		return
	}
	k := a.callFor(ev)
	if ev.ParentToolUseID == "" {
		a.outBlocks[k] = append(a.outBlocks[k], pendingPiece{owner: owner{category: CategoryReasoning}, bytes: ev.OutputBytes})
	}
}

func (a *attributor) onToolUse(ev *engine.Event) {
	if ev.ToolUseID == "" {
		return
	}
	a.tool(ev.ToolUseID).ParentToolUseID = ev.ParentToolUseID
	if ev.ParentToolUseID != "" || ev.MessageID == "" {
		return
	}
	// The stream emits the EventAssistant (counted as reasoning) just before its tool_use; move
	// that block's bytes to the tool.
	k, ok := a.callByID[ev.MessageID]
	if !ok {
		return
	}
	blocks := a.outBlocks[k]
	if n := len(blocks); n > 0 && blocks[n-1].category == CategoryReasoning {
		blocks[n-1].owner = owner{tool: ev.ToolUseID}
	}
}

func (a *attributor) onToolResult(ev *engine.Event) {
	if ev.ToolUseID == "" {
		return
	}
	tc := a.tool(ev.ToolUseID)
	tc.ResultBytes += ev.ResultBytes
	tc.IsError = tc.IsError || ev.IsError
	if m := delegationMarker.FindStringSubmatch(ev.ResultTail); m != nil {
		tc.DelegationID = m[1]
	}
	if ev.ParentToolUseID == "" {
		a.pending = append(a.pending, pendingPiece{owner: owner{tool: ev.ToolUseID}, bytes: ev.ResultBytes})
	}
}

// Claude Code answers a Skill call with a one-line result followed by the skill's body as a
// synthetic user message (often tens of KB); charge it to the tool.
func (a *attributor) userMessageOwner(ev *engine.Event) owner {
	if ev.Synthetic {
		for i := len(a.pending) - 1; i >= 0; i-- {
			if a.pending[i].tool != "" {
				return owner{tool: a.pending[i].tool}
			}
		}
		return owner{category: CategoryReasoning}
	}
	return owner{category: CategoryUserInput}
}

func (a *attributor) tool(id string) *ToolCost {
	tc, ok := a.out.Tools[id]
	if !ok {
		tc = &ToolCost{ToolUseID: id}
		a.out.Tools[id] = tc
	}
	return tc
}

func (a *attributor) callFor(ev *engine.Event) int {
	if ev.MessageID != "" {
		if k, ok := a.callByID[ev.MessageID]; ok {
			return k
		}
	}
	k := len(a.out.Calls)
	u := *ev.Usage
	model := ev.Model
	if model == "" {
		model = a.in.Model
	}
	c := Call{
		Idx: k, MessageID: ev.MessageID, Model: model, ParentToolUseID: ev.ParentToolUseID,
		Usage: u, PromptSize: u.PromptSize(), TS: ev.TS,
	}
	a.out.Calls = append(a.out.Calls, c)
	if ev.MessageID != "" {
		a.callByID[ev.MessageID] = k
	}
	if ev.ParentToolUseID == "" {
		a.openMainCall(k)
	}
	return k
}

func (a *attributor) openMainCall(k int) {
	prompt := a.out.Calls[k].PromptSize
	switch {
	case a.lastMain < 0:
		a.out.BaseTokens = prompt
		a.segs = []segment{{owner: owner{category: CategoryStartup}, tokens: prompt}}
	default:
		prev := a.out.Calls[a.lastMain].PromptSize
		growth := prompt - prev
		pieces := append(append([]pendingPiece(nil), a.outBlocks[a.lastMain]...), a.pending...)
		if growth < 0 {
			a.out.Calls[k].Compaction = true
			a.out.Compactions++
			a.segs = []segment{{owner: owner{category: CategoryCompaction}, tokens: prompt}}
		} else {
			a.segs = append(a.segs, splitTokens(growth, pieces)...)
		}
	}
	a.pending = nil
	a.lastMain = k
	a.callSegs[k] = append([]segment(nil), a.segs...)
}

// Unexplained growth goes to reasoning: redacted thinking the stream omits, and reminders the CLI
// injects.
func splitTokens(n int, pieces []pendingPiece) []segment {
	total := 0
	for _, p := range pieces {
		total += p.bytes
	}
	if n == 0 {
		return nil
	}
	if total == 0 {
		return []segment{{owner: owner{category: CategoryReasoning}, tokens: n}}
	}
	out := make([]segment, 0, len(pieces))
	given := 0
	for i, p := range pieces {
		t := n * p.bytes / total
		if i == len(pieces)-1 {
			t = n - given
		}
		given += t
		if t > 0 {
			out = append(out, segment{owner: p.owner, tokens: t})
		}
	}
	return out
}

func (a *attributor) charge() {
	a.equiv = map[owner]float64{}
	for k := range a.out.Calls {
		c := &a.out.Calls[k]
		w := a.weightsFor(c.Model)
		if c.ParentToolUseID != "" {
			u := c.Usage
			fresh := freshWeight(u, w)
			e := float64(u.CacheRead)*w.read + float64(u.InputTokens+u.CacheCreation)*fresh + float64(u.OutputTokens)*w.out
			a.equiv[owner{tool: c.ParentToolUseID}] += e
			continue
		}
		a.chargePrompt(k, w)
	}
	a.chargeOutput()
}

func freshWeight(u engine.Usage, w weights) float64 {
	fresh := u.InputTokens + u.CacheCreation
	if fresh == 0 {
		return w.in
	}
	w1h := u.CacheCreation1h
	w5m := u.CacheCreation - w1h
	return (float64(u.InputTokens)*w.in + float64(w5m)*w.write5m + float64(w1h)*w.write1h) / float64(fresh)
}

// The cache is a prefix: the first cache_read tokens are the oldest segments, the rest was written
// fresh.
func (a *attributor) chargePrompt(k int, w weights) {
	u := a.out.Calls[k].Usage
	read := u.CacheRead
	fresh := freshWeight(u, w)
	carried := map[string]bool{}
	for _, s := range a.callSegs[k] {
		r := min(read, s.tokens)
		read -= r
		e := float64(r)*w.read + float64(s.tokens-r)*fresh
		a.equiv[s.owner] += e
		if s.tool != "" && !carried[s.tool] {
			carried[s.tool] = true
			a.tool(s.tool).CarriedCalls++
		}
	}
}

func (a *attributor) chargeOutput() {
	totalBytes := 0
	for k, blocks := range a.outBlocks {
		if a.out.Calls[k].ParentToolUseID != "" {
			continue
		}
		for _, b := range blocks {
			totalBytes += b.bytes
		}
	}
	if a.in.OutputTokens == 0 {
		return
	}
	w := a.weightsFor(a.in.Model)
	if totalBytes == 0 {
		a.equiv[owner{category: CategoryReasoning}] += float64(a.in.OutputTokens) * w.out
		return
	}
	for _, blocks := range a.outBlocks {
		for _, b := range blocks {
			t := float64(a.in.OutputTokens) * float64(b.bytes) / float64(totalBytes)
			a.equiv[b.owner] += t * w.out
			if b.tool != "" {
				a.tool(b.tool).ArgsTokens += int(t + 0.5)
			}
		}
	}
}

func (a *attributor) reconcile() {
	a.measureResults()

	var total float64
	for _, e := range a.equiv {
		total += e
	}
	computedUSD := total * a.baseIn / 1_000_000
	cost := a.in.CostUSD
	scale := 0.0
	switch {
	case total == 0:
		a.out.UnattributedUSD = cost
	case cost == 0:
	case computedUSD > 0 && computedUSD < cost:
		scale = a.baseIn / 1_000_000
		a.out.UnattributedUSD = cost - computedUSD
	default:
		scale = cost / total
	}
	cats := map[string]Share{}
	for o, e := range a.equiv {
		usd := e * scale
		if o.tool != "" {
			tc := a.tool(o.tool)
			tc.WeightedTokens += e
			tc.CostUSD += usd
			cat := CategoryTools
			if tc.ParentToolUseID != "" {
				continue
			}
			if a.isAgentCall(o.tool) {
				cat = CategorySubagents
			}
			s := cats[cat]
			s.WeightedTokens += e
			s.CostUSD += usd
			cats[cat] = s
			continue
		}
		s := cats[o.category]
		s.WeightedTokens += e
		s.CostUSD += usd
		cats[o.category] = s
	}
	if a.out.UnattributedUSD > 0 {
		s := cats[CategoryUnattributed]
		s.CostUSD += a.out.UnattributedUSD
		cats[CategoryUnattributed] = s
	}
	for k, v := range cats {
		if v.WeightedTokens != 0 || v.CostUSD != 0 {
			a.out.Categories[k] = v
		}
	}
}

func (a *attributor) isAgentCall(id string) bool {
	for i := range a.out.Calls {
		if a.out.Calls[i].ParentToolUseID == id {
			return true
		}
	}
	return false
}

func (a *attributor) measureResults() {
	seen := map[string]bool{}
	keys := make([]int, 0, len(a.callSegs))
	for k := range a.callSegs {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		for _, s := range a.callSegs[k] {
			if s.tool == "" || seen[s.tool] {
				continue
			}
			a.tool(s.tool).ResultTokens += s.tokens
		}
		for _, s := range a.callSegs[k] {
			if s.tool != "" {
				seen[s.tool] = true
			}
		}
	}
}

func InputSummary(name string, input map[string]any) string {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := input[k].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	var s string
	switch name {
	case "Read", "Write", "Edit", "NotebookEdit": //i18n:ignore engine tool names
		s = pick("file_path", "notebook_path", "path")
	case "Bash": //i18n:ignore engine tool name
		s = pick("command")
	case "Grep", "Glob": //i18n:ignore engine tool names
		s = pick("pattern")
		if p := pick("path"); p != "" && s != "" {
			s += " in " + p //i18n:ignore joins two values, no prose
		}
	case "WebFetch": //i18n:ignore engine tool name
		s = pick("url")
	case "WebSearch": //i18n:ignore engine tool name
		s = pick("query")
	case "Skill": //i18n:ignore engine tool name
		s = SkillFromInput(input)
	case "Agent", "Task": //i18n:ignore engine tool names
		s = pick("description", "subagent_type")
	default:
		s = pick("file_path", "path", "command", "query", "url", "name", "id", "task_id")
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const maxLen = 160
	if len(s) > maxLen {
		s = strings.ToValidUTF8(s[:maxLen], "") + "…"
	}
	return s
}

func MCPServer(tool string) string {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return ""
	}
	server, _, _ := strings.Cut(rest, "__")
	return server
}
