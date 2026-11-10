package claudecode

import (
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestParse_AttributionFields(t *testing.T) {
	stream := strings.Join([]string{
		`2026-10-20T14:04:55.123456789Z {"type":"assistant","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"abcd","signature":"SIGNATURE-NOT-COUNTED"}],"usage":{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":50,"output_tokens":1,"cache_creation":{"ephemeral_1h_input_tokens":50}}}}`,
		`2026-10-20T14:04:56Z {"type":"assistant","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/a.go"}}],"usage":{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":50,"output_tokens":1}}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"0123456789"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","is_error":true,"content":[{"type":"text","text":"boom\noctopus-delegate: id=dlg_00ff model=m"},{"type":"image","source":{"data":"AAAA"}}]}]}}`,
		`{"type":"user","isSynthetic":true,"message":{"content":[{"type":"text","text":"skill body"}]}}`,
		`{"type":"user","message":{"content":"steer me"}}`,
		`{"type":"assistant","parent_tool_use_id":"toolu_9","message":{"id":"msg_2","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"result","subtype":"success","total_cost_usd":1.5,"num_turns":2,"usage":{"input_tokens":1,"output_tokens":9},"modelUsage":{"claude-opus-5-5":{"inputTokens":4,"outputTokens":9,"cacheReadInputTokens":100,"cacheCreationInputTokens":50,"costUSD":1.4},"claude-haiku-4-5":{"inputTokens":1,"outputTokens":1,"costUSD":0.1}}}`,
	}, "\n")
	evs := collect(stream)
	kinds := make([]engine.EventKind, len(evs))
	for i := range evs {
		kinds[i] = evs[i].Kind
	}
	want := []engine.EventKind{
		engine.EventAssistant, engine.EventAssistant, engine.EventToolUse,
		engine.EventToolResult, engine.EventToolResult, engine.EventUserMessage, engine.EventUserMessage,
		engine.EventAssistant, engine.EventResult,
	}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}

	think := evs[0]
	if think.MessageID != "msg_1" || think.Model != "claude-opus-5-5" || think.OutputBytes != 4 {
		t.Errorf("thinking block = %+v", think)
	}
	if think.Usage.CacheCreation1h != 50 {
		t.Errorf("1h cache creation = %d, want 50", think.Usage.CacheCreation1h)
	}
	if want := time.Date(2026, 10, 20, 14, 4, 55, 123456789, time.UTC); !think.TS.Equal(want) {
		t.Errorf("TS = %v, want %v", think.TS, want)
	}
	if evs[1].OutputBytes != len("Read")+len(`{"file_path":"/a.go"}`) {
		t.Errorf("tool_use block bytes = %d", evs[1].OutputBytes)
	}
	if tu := evs[2]; tu.ToolUseID != "toolu_1" || tu.MessageID != "msg_1" || tu.TS.IsZero() {
		t.Errorf("tool use = %+v", tu)
	}
	if r := evs[3]; r.ToolUseID != "toolu_1" || r.ResultBytes != 10 || r.IsError {
		t.Errorf("string result = %+v", r)
	}
	r := evs[4]
	if r.ToolUseID != "toolu_2" || !r.IsError || r.ResultBytes != len("boom\noctopus-delegate: id=dlg_00ff model=m")+imageBytes {
		t.Errorf("block result = %+v", r)
	}
	if !strings.Contains(r.ResultTail, "id=dlg_00ff") {
		t.Errorf("result tail lost the delegation line: %q", r.ResultTail)
	}
	if m := evs[5]; !m.Synthetic || m.ResultBytes != len("skill body") {
		t.Errorf("synthetic message = %+v", m)
	}
	if m := evs[6]; m.Synthetic || m.ResultBytes != len("steer me") {
		t.Errorf("user message = %+v", m)
	}
	if sub := evs[7]; sub.ParentToolUseID != "toolu_9" {
		t.Errorf("subagent event parent = %q", sub.ParentToolUseID)
	}
	mu := evs[8].ModelUsage
	if len(mu) != 2 || mu[0].Model != "claude-haiku-4-5" || mu[1].CacheRead != 100 || mu[1].CostUSD != 1.4 {
		t.Errorf("model usage = %+v", mu)
	}
}

func TestLastBytes_CutsOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("é", 10)
	got := lastBytes(s, 5)
	if got != "éé" {
		t.Errorf("lastBytes = %q, want %q", got, "éé")
	}
}

func TestParse_ResultCostIsWhatEachResultAdded(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"result","session_id":"s1","total_cost_usd":0.6}`,
		`{"type":"result","session_id":"s1","total_cost_usd":0.75}`,
		`{"type":"result","session_id":"s1","total_cost_usd":0.75}`,
		`{"type":"result","session_id":"s2","total_cost_usd":0.2}`,
		`{"type":"result","session_id":"s1","total_cost_usd":0.1}`,
	}, "\n")
	var got []float64
	for _, ev := range collect(stream) {
		if ev.Kind == engine.EventResult {
			got = append(got, ev.CostUSD)
		}
	}
	want := []float64{0.6, 0.15, 0, 0.2, 0.1}
	if len(got) != len(want) {
		t.Fatalf("results = %v, want %v", got, want)
	}
	for i := range want {
		if d := got[i] - want[i]; d > 1e-9 || d < -1e-9 {
			t.Errorf("result %d cost = %v, want %v", i, got[i], want[i])
		}
	}
}
