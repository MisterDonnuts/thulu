package watcher

import (
	"testing"

	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
)

func TestStopReasonFromLog(t *testing.T) {
	cases := []struct {
		name, tail, engine, want string
	}{
		{
			name:   "out of turns, followed by entrypoint lines",
			tail:   `{"type":"result","subtype":"error_max_turns","is_error":false}` + "\n====\nRun finished: exit 1\n",
			engine: "claude-code",
			want:   "max_turns",
		},
		{
			name:   "label missing",
			tail:   `{"type":"result","subtype":"error_max_turns","is_error":false}` + "\n",
			engine: "",
			want:   "max_turns",
		},
		{
			name:   "success",
			tail:   `{"type":"result","subtype":"success","is_error":false}` + "\n",
			engine: "claude-code",
			want:   "completed",
		},
		{
			name:   "the words alone",
			tail:   "max_turns 20\nReached max? no\n",
			engine: "claude-code",
			want:   "completed",
		},
		{
			name:   "codex failure",
			tail:   `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"turn.failed","error":{"message":"x"}}` + "\n",
			engine: "codex",
			want:   "completed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stopReasonFromLog(tc.tail, tc.engine); got != tc.want {
				t.Errorf("stopReasonFromLog = %q, want %q", got, tc.want)
			}
		})
	}
}
