package usage

import (
	"math"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

var unitPrices = func(string) (Pricing, bool) {
	return Pricing{InPerMTok: 1, OutPerMTok: 5, CacheReadPerMTok: 0.1, CacheCreatePerMTok: 1.25}, true
}

func call(id string, in, read, create, out int) engine.Event {
	return engine.Event{Kind: engine.EventAssistant, MessageID: id,
		Usage: &engine.Usage{InputTokens: in, CacheRead: read, CacheCreation: create, OutputTokens: out}}
}

func toolUse(msg, id, name string) engine.Event {
	return engine.Event{Kind: engine.EventToolUse, MessageID: msg, ToolUseID: id, ToolName: name}
}

func result(id string, bytes int) engine.Event {
	return engine.Event{Kind: engine.EventToolResult, ToolUseID: id, ResultBytes: bytes}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func sumCategories(a Attribution) float64 {
	var s float64
	for _, c := range a.Categories {
		s += c.CostUSD
	}
	return s
}

func TestAttribute_ResultIsWhatThePromptGrew(t *testing.T) {
	evs := []engine.Event{
		call("m1", 0, 0, 1000, 0),
		toolUse("m1", "t1", "Read"),
		result("t1", 4000),
		call("m2", 0, 1000, 800, 0),
	}
	att := Attribute(AttributionInput{Events: evs, Model: "x", Prices: unitPrices})
	if att.BaseTokens != 1000 || len(att.Calls) != 2 {
		t.Fatalf("base=%d calls=%d", att.BaseTokens, len(att.Calls))
	}
	tc := att.Tools["t1"]
	if tc.ResultTokens != 800 || tc.CarriedCalls != 1 || tc.ResultBytes != 4000 {
		t.Fatalf("tool = %+v", tc)
	}
	if got := att.Categories[CategoryStartup].WeightedTokens; !near(got, 1250+100) {
		t.Errorf("startup weighted = %v", got)
	}
	if !near(tc.WeightedTokens, 800*1.25) {
		t.Errorf("tool weighted = %v", tc.WeightedTokens)
	}
	if tc.CostUSD != 0 || att.UnattributedUSD != 0 {
		t.Errorf("unpriced run got dollars: %+v", tc)
	}
}

func TestAttribute_DedupesByMessageID(t *testing.T) {
	evs := []engine.Event{
		call("m1", 0, 0, 1000, 0),
		call("m1", 0, 0, 1000, 0),
		toolUse("m1", "t1", "Bash"),
	}
	att := Attribute(AttributionInput{Events: evs, Model: "x", CostUSD: 1, Prices: unitPrices})
	if len(att.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(att.Calls))
	}
	if !near(sumCategories(att), 1) {
		t.Errorf("categories add up to %v, want the run's cost", sumCategories(att))
	}
}

func TestAttribute_SharesAddUpToTheCost(t *testing.T) {
	evs := []engine.Event{
		call("m1", 0, 0, 1000, 0),
		toolUse("m1", "t1", "Read"),
		result("t1", 1000),
		call("m2", 0, 1000, 1000, 0),
		toolUse("m2", "t2", "mcp__octopus-api__task_get"),
		result("t2", 1000),
		call("m3", 0, 2000, 1000, 0),
	}
	att := Attribute(AttributionInput{Events: evs, Model: "x", CostUSD: 0.01, OutputTokens: 300, Prices: unitPrices})
	if !near(sumCategories(att), 0.01) {
		t.Fatalf("categories add up to %v", sumCategories(att))
	}
	var tools float64
	for _, tc := range att.Tools {
		tools += tc.CostUSD
	}
	if !near(tools, att.Categories[CategoryTools].CostUSD) {
		t.Errorf("tool rows %v vs tools category %v", tools, att.Categories[CategoryTools].CostUSD)
	}
	if att.Tools["t1"].CarriedCalls != 2 || att.Tools["t2"].CarriedCalls != 1 {
		t.Errorf("carried: t1=%d t2=%d", att.Tools["t1"].CarriedCalls, att.Tools["t2"].CarriedCalls)
	}
	if att.Tools["t1"].CostUSD <= att.Tools["t2"].CostUSD {
		t.Errorf("the older result should cost more: %v vs %v", att.Tools["t1"].CostUSD, att.Tools["t2"].CostUSD)
	}
}

func TestAttribute_GapIsUnattributed(t *testing.T) {
	evs := []engine.Event{call("m1", 1000, 0, 0, 0)}
	att := Attribute(AttributionInput{Events: evs, Model: "x", CostUSD: 0.002, Prices: unitPrices})
	if !near(att.UnattributedUSD, 0.001) || !near(att.Categories[CategoryStartup].CostUSD, 0.001) {
		t.Errorf("unattributed=%v startup=%v", att.UnattributedUSD, att.Categories[CategoryStartup].CostUSD)
	}
}

func TestAttribute_NoCalls(t *testing.T) {
	att := Attribute(AttributionInput{Model: "x", CostUSD: 0.5, Prices: unitPrices})
	if !near(att.UnattributedUSD, 0.5) || !near(att.Categories[CategoryUnattributed].CostUSD, 0.5) {
		t.Errorf("att = %+v", att)
	}
}

func TestAttribute_Subagent(t *testing.T) {
	sub := call("s1", 500, 0, 0, 0)
	sub.ParentToolUseID = "agent1"
	evs := []engine.Event{
		call("m1", 0, 0, 1000, 0),
		toolUse("m1", "agent1", "Agent"),
		sub,
		result("agent1", 400),
		call("m2", 0, 1000, 100, 0),
	}
	att := Attribute(AttributionInput{Events: evs, Model: "x", CostUSD: 1, Prices: unitPrices})
	if att.Categories[CategorySubagents].CostUSD == 0 {
		t.Fatalf("no subagent share: %+v", att.Categories)
	}
	if _, ok := att.Categories[CategoryTools]; ok {
		t.Errorf("the Agent call went to tools: %+v", att.Categories)
	}
	if att.Tools["agent1"].WeightedTokens < 500 {
		t.Errorf("Agent call weight = %v, want at least the subagent's 500", att.Tools["agent1"].WeightedTokens)
	}
	if !near(sumCategories(att), 1) {
		t.Errorf("categories add up to %v", sumCategories(att))
	}
}

func TestAttribute_Compaction(t *testing.T) {
	evs := []engine.Event{
		call("m1", 0, 0, 5000, 0),
		toolUse("m1", "t1", "Read"),
		result("t1", 9000),
		call("m2", 0, 5000, 3000, 0),
		call("m3", 0, 0, 2000, 0),
	}
	att := Attribute(AttributionInput{Events: evs, Model: "x", CostUSD: 1, Prices: unitPrices})
	if att.Compactions != 1 || !att.Calls[2].Compaction {
		t.Fatalf("compactions = %d", att.Compactions)
	}
	if att.Categories[CategoryCompaction].WeightedTokens != 2000*1.25 {
		t.Errorf("compaction weighted = %v", att.Categories[CategoryCompaction].WeightedTokens)
	}
	if att.Tools["t1"].CarriedCalls != 1 {
		t.Errorf("the result outlived the compaction: carried %d", att.Tools["t1"].CarriedCalls)
	}
}

func TestAttribute_SkillBodyGoesToTheSkillCall(t *testing.T) {
	evs := []engine.Event{
		call("m1", 0, 0, 1000, 0),
		toolUse("m1", "t1", "Skill"),
		result("t1", 30),
		{Kind: engine.EventUserMessage, ResultBytes: 9970, Synthetic: true},
		{Kind: engine.EventUserMessage, ResultBytes: 1000},
		call("m2", 0, 1000, 1100, 0),
	}
	att := Attribute(AttributionInput{Events: evs, Model: "x", Prices: unitPrices})
	if got := att.Tools["t1"].ResultTokens; got != 1000 {
		t.Errorf("skill call result tokens = %d, want 1000", got)
	}
	if got := att.Categories[CategoryUserInput].WeightedTokens; !near(got, 100*1.25) {
		t.Errorf("user input weighted = %v", got)
	}
}

func TestAttribute_OutputFollowsTheBytes(t *testing.T) {
	text := call("m1", 0, 0, 1000, 0)
	text.OutputBytes = 300
	use := call("m1", 0, 0, 1000, 0)
	use.OutputBytes = 100
	evs := []engine.Event{text, use, toolUse("m1", "t1", "Edit")}
	att := Attribute(AttributionInput{Events: evs, Model: "x", OutputTokens: 400, Prices: unitPrices})
	if got := att.Tools["t1"].ArgsTokens; got != 100 {
		t.Errorf("args tokens = %d, want 100", got)
	}
	if got := att.Categories[CategoryReasoning].WeightedTokens; !near(got, 300*5) {
		t.Errorf("reasoning weighted = %v, want 1500", got)
	}
}

func TestAttribute_DelegationMarker(t *testing.T) {
	r := result("t1", 100)
	r.ResultTail = "summary…\noctopus-delegate: id=dlg_0123456789abcdef model=m in=1 out=2 cost=0.001\n"
	evs := []engine.Event{call("m1", 0, 0, 10, 0), toolUse("m1", "t1", "Bash"), r}
	att := Attribute(AttributionInput{Events: evs, Model: "x", Prices: unitPrices})
	if got := att.Tools["t1"].DelegationID; got != "dlg_0123456789abcdef" {
		t.Errorf("delegation id = %q", got)
	}
}

func TestAttribute_OneHourCacheWrite(t *testing.T) {
	c := call("m1", 0, 0, 1000, 0)
	c.Usage.CacheCreation1h = 1000
	att := Attribute(AttributionInput{Events: []engine.Event{c}, Model: "x", Prices: unitPrices})
	if got := att.Categories[CategoryStartup].WeightedTokens; !near(got, 2000) {
		t.Errorf("1h write weighted = %v, want 2000", got)
	}
}

func TestInputSummary(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"Read", map[string]any{"file_path": "/src/a.go", "offset": 3}, "/src/a.go"},
		{"Bash", map[string]any{"command": "go test ./...\necho done"}, "go test ./..."},
		{"Grep", map[string]any{"pattern": "TODO", "path": "internal"}, "TODO in internal"},
		{"Skill", map[string]any{"skill": "octopus-run"}, "octopus-run"},
		{"mcp__octopus-api__task_get", map[string]any{"task_id": "abc"}, "abc"},
	}
	for _, c := range cases {
		if got := InputSummary(c.name, c.input); got != c.want {
			t.Errorf("InputSummary(%s) = %q, want %q", c.name, got, c.want)
		}
	}
	long := InputSummary("Bash", map[string]any{"command": string(make([]byte, 400))})
	if len(long) > 164 {
		t.Errorf("summary not cut: %d bytes", len(long))
	}
}

func TestMCPServer(t *testing.T) {
	if got := MCPServer("mcp__thulu-browser__evidence_publish"); got != "thulu-browser" {
		t.Errorf("MCPServer = %q", got)
	}
	if got := MCPServer("Read"); got != "" {
		t.Errorf("MCPServer(Read) = %q", got)
	}
}

func TestTokenOptimizerLevel(t *testing.T) {
	logs := "2026-10-20T00:00:00Z {\"type\":\"octopus.session\",\"mode\":\"native\"}\n" +
		"2026-10-20T00:00:01Z {\"type\":\"octopus.token_optimizer\",\"level\":\"enforce\",\"threshold_bytes\":24576}\n"
	if got := TokenOptimizerLevel(logs); got != "enforce" {
		t.Errorf("level = %q", got)
	}
	if got := TokenOptimizerLevel("{}"); got != "" {
		t.Errorf("level without the line = %q", got)
	}
}
