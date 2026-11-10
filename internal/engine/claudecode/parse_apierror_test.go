package claudecode

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestParse_ResultAPIError(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantAPIErr bool
		wantStatus int
	}{
		{
			"revoked oauth token",
			`{"type":"result","subtype":"success","is_error":true,"duration_ms":1890,"num_turns":1,"terminal_reason":"api_error","api_error_status":401,"result":"Failed to authenticate. API Error: 401 OAuth access token has been revoked.","session_id":"s1","total_cost_usd":0}`,
			true, 401,
		},
		{
			"status as a string",
			`{"type":"result","is_error":true,"terminal_reason":"api_error","api_error_status":"429","result":"rate limited"}`,
			true, 429,
		},
		{
			"older cli, status in the text",
			`{"type":"result","subtype":"success","is_error":true,"result":"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"}`,
			true, 529,
		},
		{
			"api error without a status",
			`{"type":"result","is_error":true,"terminal_reason":"api_error","api_error_status":null,"result":"Connection error."}`,
			true, 0,
		},
		{"plain failure", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`, false, 0},
		{"success", `{"type":"result","subtype":"success","is_error":false,"result":"API Error: 401 is what the user asked about"}`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collect(tc.line + "\n")
			if len(got) != 1 || got[0].Kind != engine.EventResult {
				t.Fatalf("expected one EventResult, got %+v", got)
			}
			if got[0].APIError != tc.wantAPIErr || got[0].APIErrorStatus != tc.wantStatus {
				t.Errorf("api error = %v/%d, want %v/%d", got[0].APIError, got[0].APIErrorStatus, tc.wantAPIErr, tc.wantStatus)
			}
		})
	}
}
