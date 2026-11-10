package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
)

func TestRunHitMaxTurns(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want bool
	}{
		{
			name: "claude out of turns",
			log: `{"type":"system","subtype":"init","model":"claude-sonnet-4-6","session_id":"s"}
{"type":"result","subtype":"error_max_turns","is_error":false,"num_turns":20,"session_id":"s"}
============================================
Run finished: exit 1
`,
			want: true,
		},
		{
			name: "claude failed otherwise",
			log:  `{"type":"result","subtype":"error_during_execution","is_error":true}` + "\n",
			want: false,
		},
		{
			name: "the words alone",
			log: `{"type":"assistant","message":{"content":[{"type":"text","text":"if it hits \"subtype\":\"error_max_turns\" we retry"}],"usage":{"input_tokens":1,"output_tokens":1}}}
{"type":"result","subtype":"success","is_error":false}
`,
			want: false,
		},
		{
			name: "codex never runs out of turns",
			log: `{"type":"thread.started","thread_id":"t"}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"boom"}}
`,
			want: false,
		},
		{name: "no result", log: "Engine: claude-code\n", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "session.log"), []byte(tc.log), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := runHitMaxTurns(dir); got != tc.want {
				t.Errorf("runHitMaxTurns = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunHitMaxTurnsReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	filler := strings.Repeat(`{"type":"assistant","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n", int(sessionLogTail/40))
	body := filler + `{"type":"result","subtype":"error_max_turns","is_error":false}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.log"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !runHitMaxTurns(dir) {
		t.Error("runHitMaxTurns = false, want true")
	}
}

func TestRunHitMaxTurnsMissingLog(t *testing.T) {
	if runHitMaxTurns(t.TempDir()) {
		t.Error("no session.log must answer false")
	}
}
