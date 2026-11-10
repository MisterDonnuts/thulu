package claudecode

import (
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func collect(s string) []engine.Event {
	var out []engine.Event
	for ev := range parseStream(strings.NewReader(s)) {
		out = append(out, ev)
	}
	return out
}

func TestParse_EmptyInput(t *testing.T) {
	if got := collect(""); len(got) != 0 {
		t.Fatalf("empty input: got %d events, want 0", len(got))
	}
}

func TestParse_SystemInit(t *testing.T) {
	line := `{"type":"system","subtype":"init","model":"claude-sonnet-4-5"}`
	got := collect(line + "\n")
	if len(got) != 1 || got[0].Kind != engine.EventInit {
		t.Fatalf("expected single EventInit, got %+v", got)
	}
	if got[0].Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q, want claude-sonnet-4-5", got[0].Model)
	}
}

func TestParse_SystemInit_RequiresModel(t *testing.T) {
	line := `{"type":"system","subtype":"init"}`
	if got := collect(line + "\n"); len(got) != 0 {
		t.Fatalf("init without model should be skipped, got %+v", got)
	}
}

func TestParse_Assistant(t *testing.T) {
	line := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":20,"cache_creation_input_tokens":10}}}`
	got := collect(line + "\n")
	if len(got) != 1 || got[0].Kind != engine.EventAssistant {
		t.Fatalf("expected EventAssistant, got %+v", got)
	}
	u := got[0].Usage
	if u == nil {
		t.Fatal("usage is nil")
	}
	if u.InputTokens != 100 || u.OutputTokens != 50 || u.CacheRead != 20 || u.CacheCreation != 10 {
		t.Errorf("usage = %+v, want {100,50,20,10}", u)
	}
	if got := u.PromptSize(); got != 130 {
		t.Errorf("PromptSize = %d, want 130", got)
	}
}

func TestParse_RateLimit(t *testing.T) {
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"warning","resetsAt":1700000000,"rateLimitType":"5h","utilization":0.42,"isUsingOverage":true}}`
	got := collect(line + "\n")
	if len(got) != 1 || got[0].Kind != engine.EventRateLimit {
		t.Fatalf("expected EventRateLimit, got %+v", got)
	}
	rl := got[0].RateLimit
	if rl == nil {
		t.Fatal("rate limit is nil")
	}
	if rl.Status != "warning" || rl.ResetsAt != 1700000000 || rl.RateLimitType != "5h" {
		t.Errorf("rate limit = %+v, mismatched core fields", rl)
	}
	if !rl.IsOverage || rl.Utilization != 0.42 {
		t.Errorf("rate limit util/overage = %v/%v", rl.Utilization, rl.IsOverage)
	}
}

func TestParse_Result(t *testing.T) {
	line := `{"type":"result","result":"done","is_error":false,"duration_ms":4321,"num_turns":3,"total_cost_usd":0.12,"stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":200,"cache_read_input_tokens":50,"cache_creation_input_tokens":25}}`
	got := collect(line + "\n")
	if len(got) != 1 || got[0].Kind != engine.EventResult {
		t.Fatalf("expected EventResult, got %+v", got)
	}
	r := got[0]
	if r.Result != "done" || r.IsError {
		t.Errorf("result text/iserror = %q/%v", r.Result, r.IsError)
	}
	if r.DurationMs != 4321 || r.NumTurns != 3 || r.CostUSD != 0.12 || r.StopReason != "end_turn" {
		t.Errorf("result fields mismatch: %+v", r)
	}
	if r.Usage == nil || r.Usage.InputTokens != 1000 || r.Usage.OutputTokens != 200 {
		t.Errorf("result usage = %+v", r.Usage)
	}
}

func TestParse_Result_IsError(t *testing.T) {
	line := `{"type":"result","result":"boom","is_error":true}`
	got := collect(line + "\n")
	if len(got) != 1 || !got[0].IsError {
		t.Fatalf("expected is_error=true, got %+v", got)
	}
}

func TestParse_DockerTimestampPrefix(t *testing.T) {
	line := `2026-05-20T10:00:00Z {"type":"assistant","message":{"usage":{"input_tokens":42,"output_tokens":7,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`
	got := collect(line + "\n")
	if len(got) != 1 || got[0].Kind != engine.EventAssistant {
		t.Fatalf("expected EventAssistant despite ts prefix, got %+v", got)
	}
	if got[0].Usage.InputTokens != 42 {
		t.Errorf("usage.input_tokens = %d, want 42", got[0].Usage.InputTokens)
	}
}

func TestParse_SkipsMalformed(t *testing.T) {
	lines := strings.Join([]string{
		`not json at all`,
		``,
		`{"type":"assistant","message":{"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{ this is broken json`,
		`{"type":"result","result":"ok","is_error":false}`,
	}, "\n")
	got := collect(lines)
	if len(got) != 2 {
		t.Fatalf("expected 2 events (assistant + result), got %d: %+v", len(got), got)
	}
	if got[0].Kind != engine.EventAssistant || got[1].Kind != engine.EventResult {
		t.Errorf("event kinds = %v/%v, want assistant/result", got[0].Kind, got[1].Kind)
	}
}

func TestParse_SkipsUnknownTypes(t *testing.T) {
	line := `{"type":"some_new_event_we_dont_know","foo":"bar"}`
	if got := collect(line + "\n"); len(got) != 0 {
		t.Fatalf("expected unknown type to be dropped, got %+v", got)
	}
}

func TestParse_TopLevelToolUse(t *testing.T) {
	line := `{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}`
	got := collect(line + "\n")
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(got), got)
	}
	if got[0].Kind != engine.EventToolUse || got[0].ToolName != "Read" {
		t.Errorf("event = %+v, want tool_use Read", got[0])
	}
	if got[0].ToolInput["file_path"] != "/x" {
		t.Errorf("tool input = %v, want file_path=/x", got[0].ToolInput)
	}
}

func TestParse_AssistantWithEmbeddedToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"service_tier":"standard"},"content":[{"type":"text","text":"thinking..."},{"type":"tool_use","name":"Skill","input":{"skill":"frontend-design"}},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`
	got := collect(line + "\n")
	if len(got) != 3 {
		t.Fatalf("expected 3 events (assistant + 2 tool_use), got %d: %+v", len(got), got)
	}
	if got[0].Kind != engine.EventAssistant || got[0].Usage.ServiceTier != "standard" {
		t.Errorf("event[0] = %+v, want assistant tier=standard", got[0])
	}
	if got[1].Kind != engine.EventToolUse || got[1].ToolName != "Skill" {
		t.Errorf("event[1] = %+v, want tool_use Skill", got[1])
	}
	if got[1].ToolInput["skill"] != "frontend-design" {
		t.Errorf("event[1] skill = %v, want frontend-design", got[1].ToolInput["skill"])
	}
	if got[2].Kind != engine.EventToolUse || got[2].ToolName != "Bash" {
		t.Errorf("event[2] = %+v, want tool_use Bash", got[2])
	}
}

func TestParse_MultipleAssistantsThenResult(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-opus-4-7"}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":200,"output_tokens":80,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}`,
		`{"type":"result","result":"final","is_error":false,"num_turns":2,"usage":{"input_tokens":300,"output_tokens":130,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}`,
	}, "\n")
	got := collect(lines)
	if len(got) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != engine.EventInit || got[0].Model != "claude-opus-4-7" {
		t.Errorf("event[0] = %+v, want init opus", got[0])
	}
	if got[3].Kind != engine.EventResult || got[3].NumTurns != 2 {
		t.Errorf("event[3] = %+v, want result num_turns=2", got[3])
	}
}

func TestParse_AssistantWithoutUsage_Dropped(t *testing.T) {
	line := `{"type":"assistant","message":{"content":"hi"}}`
	if got := collect(line + "\n"); len(got) != 0 {
		t.Fatalf("expected no events, got %+v", got)
	}
}

func TestParse_ResultOutcome(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{"success", `{"type":"result","subtype":"success","is_error":false,"result":"ok"}`, engine.OutcomeOK},
		{"max turns", `{"type":"result","subtype":"error_max_turns","is_error":false,"num_turns":21}`, engine.OutcomeMaxTurns},
		{"max turns flagged", `{"type":"result","subtype":"error_max_turns","is_error":true}`, engine.OutcomeMaxTurns},
		{"during execution", `{"type":"result","subtype":"error_during_execution","is_error":true}`, engine.OutcomeError},
		{"api error", `{"type":"result","subtype":"success","is_error":true,"result":"API Error: 529"}`, engine.OutcomeError},
		{"no subtype", `{"type":"result","result":"done"}`, engine.OutcomeOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collect(tc.line + "\n")
			if len(got) != 1 || got[0].Outcome != tc.want {
				t.Fatalf("outcome = %+v, want %q", got, tc.want)
			}
		})
	}
}

func TestParse_SessionLine(t *testing.T) {
	lines := strings.Join([]string{
		`{"type":"octopus.session","mode":"native"}`,
		`{"type":"system","subtype":"init","model":"claude-sonnet-4-6","session_id":"s1"}`,
		`{"type":"result","subtype":"success","result":"ok","session_id":"s1"}`,
	}, "\n")
	got := collect(lines)
	if len(got) != 2 {
		t.Fatalf("want init + result, got %+v", got)
	}
	if got[0].Kind != engine.EventInit || got[0].SessionMode != engine.SessionNative {
		t.Errorf("init = %+v, want session mode native", got[0])
	}
	if got := collect(`{"type":"octopus.session","mode":"fresh"}` + "\n"); len(got) != 0 {
		t.Errorf("session line alone yielded %+v", got)
	}
}
