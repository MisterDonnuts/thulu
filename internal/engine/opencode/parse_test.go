package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func collect(stream string) []engine.Event {
	var out []engine.Event
	for ev := range parseStream(strings.NewReader(stream)) {
		out = append(out, ev)
	}
	return out
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "opencode", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func last(evs []engine.Event, kind engine.EventKind) (engine.Event, int) {
	var got engine.Event
	n := 0
	for _, ev := range evs {
		if ev.Kind == kind {
			got = ev
			n++
		}
	}
	return got, n
}

func TestParseStream_OK(t *testing.T) {
	evs := collect(fixture(t, "ok"))
	if len(evs) == 0 || evs[0].Kind != engine.EventInit {
		t.Fatalf("first event = %+v, want EventInit", evs)
	}
	init := evs[0]
	if !strings.HasPrefix(init.SessionID, "ses_") || init.SessionMode != "fresh" || init.Model != "mock-coder" {
		t.Errorf("init = %+v", init)
	}
	tool, n := last(evs, engine.EventToolUse)
	if n != 1 || tool.ToolName != "bash" || tool.ToolInput["command"] != "echo hello" {
		t.Errorf("tool use = %+v (%d)", tool, n)
	}
	if _, n := last(evs, engine.EventAssistant); n != 2 {
		t.Errorf("assistant events = %d, want one per step", n)
	}
	res, n := last(evs, engine.EventResult)
	if n != 1 {
		t.Fatalf("results = %d", n)
	}
	if res.Outcome != engine.OutcomeOK || res.IsError {
		t.Errorf("outcome = %q, IsError = %v", res.Outcome, res.IsError)
	}
	if res.Result != "Done: the command printed hello." {
		t.Errorf("Result = %q", res.Result)
	}
	if res.SessionID != init.SessionID || res.NumTurns != 2 || res.StopReason != "stop" {
		t.Errorf("result = %+v", res)
	}
	u := res.Usage
	if u == nil || u.InputTokens != 300 || u.OutputTokens != 52 || u.CacheRead != 2200 || u.CacheCreation != 0 {
		t.Errorf("usage = %+v", u)
	}
	if res.DurationMs <= 0 {
		t.Errorf("DurationMs = %d", res.DurationMs)
	}
}

func TestParseStream_APIError(t *testing.T) {
	res, n := last(collect(fixture(t, "error")), engine.EventResult)
	if n != 1 {
		t.Fatalf("results = %d", n)
	}
	if res.Outcome != engine.OutcomeError || !res.IsError || !res.APIError || res.APIErrorStatus != 401 {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(res.Result, "api key is invalid") {
		t.Errorf("Result = %q, want the provider's message", res.Result)
	}
}

func TestParseStream_Resume(t *testing.T) {
	evs := collect(fixture(t, "resume"))
	if evs[0].SessionMode != "native" {
		t.Errorf("init = %+v", evs[0])
	}
	if res, _ := last(evs, engine.EventResult); res.Outcome != engine.OutcomeOK {
		t.Errorf("result = %+v", res)
	}
}

const sid = `"sessionID":"ses_1"`

func TestParseStream_Shapes(t *testing.T) {
	cases := []struct {
		name    string
		stream  string
		outcome string
		result  string
		output  int
	}{
		{
			name: "reasoning tokens count as output",
			stream: `{"type":"step_start",` + sid + `}
{"type":"text",` + sid + `,"part":{"text":"hi"}}
{"type":"step_finish",` + sid + `,"part":{"reason":"stop","tokens":{"input":5,"output":3,"reasoning":7,"cache":{"read":0,"write":0}},"cost":0.5}}`,
			outcome: engine.OutcomeOK, result: "hi", output: 10,
		},
		{
			name: "a step that finished after an error recovered from it",
			stream: `{"type":"error",` + sid + `,"error":{"name":"UnknownError","data":{"message":"blip"}}}
{"type":"step_start",` + sid + `}
{"type":"text",` + sid + `,"part":{"text":"ok after all"}}
{"type":"step_finish",` + sid + `,"part":{"reason":"stop"}}`,
			outcome: engine.OutcomeOK, result: "ok after all",
		},
		{
			name: "a step that never finished is a failed run",
			stream: `{"type":"step_start",` + sid + `}
{"type":"text",` + sid + `,"part":{"text":"half"}}`,
			outcome: engine.OutcomeError,
		},
		{
			name: "the answer is the last step that wrote text",
			stream: `{"type":"step_start",` + sid + `}
{"type":"text",` + sid + `,"part":{"text":"plan"}}
{"type":"step_finish",` + sid + `,"part":{"reason":"tool-calls"}}
{"type":"step_start",` + sid + `}
{"type":"text",` + sid + `,"part":{"text":"one"}}
{"type":"text",` + sid + `,"part":{"text":"two"}}
{"type":"step_finish",` + sid + `,"part":{"reason":"stop"}}
{"type":"step_start",` + sid + `}
{"type":"step_finish",` + sid + `,"part":{"reason":"stop"}}`,
			outcome: engine.OutcomeOK, result: "one\n\ntwo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, n := last(collect(tc.stream), engine.EventResult)
			if n != 1 {
				t.Fatalf("results = %d", n)
			}
			if res.Outcome != tc.outcome || (tc.result != "" && res.Result != tc.result) {
				t.Errorf("outcome = %q result = %q", res.Outcome, res.Result)
			}
			if tc.output > 0 && (res.Usage == nil || res.Usage.OutputTokens != tc.output) {
				t.Errorf("usage = %+v", res.Usage)
			}
		})
	}
	res, _ := last(collect(`{"type":"step_finish",`+sid+`,"part":{"cost":0.25}}
{"type":"step_finish",`+sid+`,"part":{"cost":0.5}}`), engine.EventResult)
	if res.CostUSD != 0.75 {
		t.Errorf("CostUSD = %v, want the steps summed", res.CostUSD)
	}
}

func TestParseStream_IgnoresOtherEngines(t *testing.T) {
	foreign := `{"type":"octopus.session","mode":"fresh"}
{"type":"system","subtype":"init","session_id":"S"}
{"type":"thread.started","thread_id":"T"}
{"type":"error","message":"codex error"}
{"type":"result","subtype":"success","is_error":false,"num_turns":1}
not json`
	if evs := collect(foreign); len(evs) != 0 {
		t.Errorf("events = %+v, want none", evs)
	}
}
