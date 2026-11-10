package codex

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

func TestParseStreamHappyPath(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"th_123","model":"gpt-5-codex"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./..."}}`,
		`{"type":"item.completed","item":{"type":"assistant_message","text":"done, all green"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":40,"cached_input_tokens":10}}`,
	}, "\n")

	evs := collect(stream)

	var init, tool, assistant, result *engine.Event
	for i := range evs {
		switch evs[i].Kind {
		case engine.EventInit:
			init = &evs[i]
		case engine.EventToolUse:
			tool = &evs[i]
		case engine.EventAssistant:
			assistant = &evs[i]
		case engine.EventResult:
			result = &evs[i]
		case engine.EventUnknown, engine.EventRateLimit, engine.EventToolResult, engine.EventUserMessage:
		}
	}

	if init == nil || init.Model != "gpt-5-codex" || init.SessionID != "th_123" {
		t.Fatalf("init event wrong: %+v", init)
	}
	if tool == nil || tool.ToolName != "shell" {
		t.Fatalf("tool event wrong: %+v", tool)
	}
	if assistant == nil {
		t.Fatal("missing EventAssistant (turn count)")
	}
	if result == nil {
		t.Fatal("missing EventResult")
	}
	if result.Result != "done, all green" {
		t.Errorf("result text = %q", result.Result)
	}
	if result.SessionID != "th_123" {
		t.Errorf("result session = %q, want th_123", result.SessionID)
	}
	if result.Usage == nil || result.Usage.InputTokens != 90 ||
		result.Usage.OutputTokens != 40 || result.Usage.CacheRead != 10 {
		t.Errorf("result usage wrong: %+v", result.Usage)
	}
	if result.Outcome != engine.OutcomeOK {
		t.Errorf("outcome = %q, want ok", result.Outcome)
	}
}

func TestParseStreamRealFixture(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019efa0a-0198-79f3-b7d5-1754047d0b6a"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata for ` + "`o4-mini`" + ` not found."}}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Thulu probe OK"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10487,"cached_input_tokens":10368,"output_tokens":107,"reasoning_output_tokens":64}}`,
	}, "\n")

	var result *engine.Event
	for ev := range parseStream(strings.NewReader(stream)) {
		if ev.Kind == engine.EventResult {
			r := ev
			result = &r
		}
	}
	if result == nil {
		t.Fatal("no EventResult from real fixture")
	}
	if result.Result != "Thulu probe OK" {
		t.Errorf("result text = %q, want %q", result.Result, "Thulu probe OK")
	}
	if result.SessionID != "019efa0a-0198-79f3-b7d5-1754047d0b6a" {
		t.Errorf("session id = %q", result.SessionID)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10487-10368 ||
		result.Usage.OutputTokens != 107 || result.Usage.CacheRead != 10368 {
		t.Errorf("usage = %+v", result.Usage)
	}
	if result.IsError {
		t.Error("IsError should be false — the error item is just a metadata warning")
	}
}

func TestParseStreamExactlyOneResult(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t","model":"gpt-5"}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`,
		`{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":7}}`,
		`{"type":"item.completed","item":{"type":"assistant_message","text":"final"}}`,
	}, "\n")

	results := 0
	var last engine.Event
	for ev := range parseStream(strings.NewReader(stream)) {
		if ev.Kind == engine.EventResult {
			results++
			last = ev
		}
	}
	if results != 1 {
		t.Fatalf("want exactly 1 EventResult, got %d", results)
	}
	if last.Usage.InputTokens != 30 || last.Usage.OutputTokens != 12 {
		t.Errorf("summed usage wrong: %+v", last.Usage)
	}
}

func TestParseStreamSkipsGarbageAndEmpty(t *testing.T) {
	stream := "starting codex…\n\nnot json\n{bad json}\n"
	if evs := collect(stream); len(evs) != 0 {
		t.Fatalf("expected no events, got %+v", evs)
	}
}

func TestParseStreamTimestampPrefix(t *testing.T) {
	stream := `2026-06-15T10:00:00Z {"type":"thread.started","thread_id":"x","model":"gpt-5"}`
	evs := collect(stream)
	if len(evs) == 0 || evs[0].Kind != engine.EventInit || evs[0].SessionID != "x" {
		t.Fatalf("timestamp-prefixed init not parsed: %+v", evs)
	}
}

func lastResult(t *testing.T, lines ...string) engine.Event {
	t.Helper()
	var result *engine.Event
	for ev := range parseStream(strings.NewReader(strings.Join(lines, "\n"))) {
		if ev.Kind == engine.EventResult {
			r := ev
			result = &r
		}
	}
	if result == nil {
		t.Fatal("no EventResult")
	}
	return *result
}

func TestParseStreamTurnFailedIsError(t *testing.T) {
	r := lastResult(t,
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.failed","error":{"message":"401 Unauthorized"}}`,
	)
	if r.Outcome != engine.OutcomeError || !r.IsError {
		t.Fatalf("outcome = %q, is_error = %v; want error", r.Outcome, r.IsError)
	}
	if r.Result != "401 Unauthorized" {
		t.Errorf("result = %q, want the failure message", r.Result)
	}
}

func TestParseStreamRecoveredErrorIsOK(t *testing.T) {
	r := lastResult(t,
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"Reconnecting... 1/5"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":1}}`,
	)
	if r.Outcome != engine.OutcomeOK || r.IsError {
		t.Fatalf("outcome = %q, is_error = %v; want ok", r.Outcome, r.IsError)
	}
	if r.Result != "done" {
		t.Errorf("result = %q, want the agent message", r.Result)
	}
}

func TestParseStreamUnfinishedTurnIsError(t *testing.T) {
	r := lastResult(t,
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"make"}}`,
	)
	if r.Outcome != engine.OutcomeError {
		t.Fatalf("outcome = %q, want error", r.Outcome)
	}
}

func TestParseStreamModelComesFromTheSessionLine(t *testing.T) {
	evs := collect(strings.Join([]string{
		`{"type":"octopus.session","mode":"fresh","model":"gpt-5.4-mini"}`,
		`{"type":"thread.started","thread_id":"t1"}`,
	}, "\n"))
	if len(evs) == 0 || evs[0].Kind != engine.EventInit || evs[0].Model != "gpt-5.4-mini" {
		t.Fatalf("init = %+v, want the model: usage prices the run by it", evs)
	}
}

func TestParseStreamSessionLine(t *testing.T) {
	evs := collect(strings.Join([]string{
		`{"type":"octopus.session","mode":"rehydrated"}`,
		`{"type":"thread.started","thread_id":"t1"}`,
	}, "\n"))
	if len(evs) != 2 || evs[0].Kind != engine.EventInit {
		t.Fatalf("want init + result, got %+v", evs)
	}
	if evs[0].SessionMode != engine.SessionRehydrated {
		t.Errorf("session mode = %q, want rehydrated", evs[0].SessionMode)
	}
	if u := evs[1].Usage; u == nil || *u != (engine.Usage{}) {
		t.Errorf("session line must not count as usage: %+v", u)
	}

	if evs := collect(`{"type":"octopus.session","mode":"fresh"}`); len(evs) != 0 {
		t.Fatalf("session line alone yielded %+v", evs)
	}
}
