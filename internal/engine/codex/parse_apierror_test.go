package codex

import "testing"

func TestParseStreamAPIErrorStatus(t *testing.T) {
	r := lastResult(t,
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Incorrect API key provided"}}`,
	)
	if !r.APIError || r.APIErrorStatus != 401 {
		t.Fatalf("api error = %v/%d, want true/401", r.APIError, r.APIErrorStatus)
	}
	ok := lastResult(t,
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"unexpected status 401 explained"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":1}}`,
	)
	if ok.APIError || ok.APIErrorStatus != 0 {
		t.Fatalf("a clean run reports api error %v/%d", ok.APIError, ok.APIErrorStatus)
	}
}

// The provider refuses with no status in the text: "exit 1" alone tells the person nothing.
func TestParseStreamRefusalWithoutStatus(t *testing.T) {
	const msg = "stream disconnected before completion: You have no credits remaining."
	r := lastResult(t,
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"Reconnecting... 2/5 (`+msg+`)"}`,
		`{"type":"error","message":"`+msg+`"}`,
		`{"type":"turn.failed","error":{"message":"`+msg+`"}}`,
	)
	if !r.IsError || !r.APIError || r.APIErrorStatus != 0 || r.Result != msg {
		t.Fatalf("result = %+v", r)
	}

	killed := lastResult(t, `{"type":"thread.started","thread_id":"t1"}`, `{"type":"turn.started"}`)
	if !killed.IsError || killed.APIError {
		t.Fatalf("a turn cut short is not the provider's refusal: %+v", killed)
	}
}
