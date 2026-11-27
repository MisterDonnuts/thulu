package chatruntime

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/engine"
)

const effectsStream = `{"type":"system","subtype":"init","model":"claude-opus-5-5","session_id":"sess-9"}
{"type":"assistant","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":6,"cache_read_input_tokens":30000,"cache_creation_input_tokens":2000,"output_tokens":400},"content":[{"type":"tool_use","id":"t1","name":"mcp__octopus-api__task_create_on_board","input":{"board_id":"acme","title":"Nueva"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"{\"id\":\"b55fb42c0359724c\",\"title\":\"Nueva\"}"}]}}
{"type":"assistant","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":8,"cache_read_input_tokens":32000,"cache_creation_input_tokens":500,"output_tokens":120},"content":[{"type":"tool_use","id":"t2","name":"mcp__octopus-api__task_update","input":{"id":"e9dc8c3469b15e9c","stage":"plan"}},{"type":"tool_use","id":"t3","name":"mcp__octopus-api__task_add_dependency","input":{"id":"e9dc8c3469b15e9c","depends_on_task_id":"b55fb42c0359724c","kind":"soft"}},{"type":"tool_use","id":"t4","name":"mcp__octopus-api__task_get","input":{"id":"e9dc8c3469b15e9c"}},{"type":"tool_use","id":"t5","name":"mcp__octopus-api__task_add_comment","input":{"id":"e9dc8c3469b15e9c","body":"hola"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"},{"type":"tool_result","tool_use_id":"t3","content":"ok"},{"type":"tool_result","tool_use_id":"t4","content":"{}"},{"type":"tool_result","tool_use_id":"t5","is_error":true,"content":"403: the key lacks tasks:write"}]}}
{"type":"result","result":"Listo.","is_error":false,"num_turns":3,"total_cost_usd":0.42,"stop_reason":"end_turn","usage":{"input_tokens":14,"output_tokens":520,"cache_read_input_tokens":62000,"cache_creation_input_tokens":2500},"session_id":"sess-9"}
`

func TestCollectTurnReadsEffectsAndCost(t *testing.T) {
	eng, err := engine.Get("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	res := collectTurn(eng.ParseStream(strings.NewReader(effectsStream)))
	if res.Text != "Listo." || res.SessionID != "sess-9" {
		t.Fatalf("text/session = %q/%q", res.Text, res.SessionID)
	}
	if res.Model != "claude-opus-5-5" || res.NumTurns != 3 || res.CostUSD != 0.42 || res.StopReason != "end_turn" {
		t.Errorf("summary = %+v", res)
	}
	if res.OutputTokens != 520 || res.CacheRead != 62000 || res.CacheCreation != 2500 || res.InputTokens != 14 {
		t.Errorf("usage = in %d out %d cr %d cc %d", res.InputTokens, res.OutputTokens, res.CacheRead, res.CacheCreation)
	}
	// The last request's prompt plus its answer: 8 + 32000 + 500 + 120.
	if res.ContextTokens != 32628 {
		t.Errorf("ContextTokens = %d, want 32628", res.ContextTokens)
	}
	want := []chat.Effect{
		{Action: chat.EffectCreated, TaskID: "b55fb42c0359724c"},
		{Action: chat.EffectMoved, TaskID: "e9dc8c3469b15e9c", Detail: "plan"},
		{Action: chat.EffectLinked, TaskID: "e9dc8c3469b15e9c", OtherTaskID: "b55fb42c0359724c", Detail: "soft"},
		{Action: chat.EffectCommented, TaskID: "e9dc8c3469b15e9c", Error: true, Detail: "403: the key lacks tasks:write"},
	}
	if !slices.Equal(res.Effects, want) {
		t.Errorf("Effects =\n%+v\nwant\n%+v", res.Effects, want)
	}
	if len(res.Tools) != 5 {
		t.Errorf("Tools = %d, want 5", len(res.Tools))
	}
}

func TestThuluToolNames(t *testing.T) {
	for name, want := range map[string]string{
		"mcp__octopus-api__task_update": "task_update",
		"octopus-api.task_update":       "task_update",
		"octopus-api_task_update":       "task_update",
		"mcp__monday-read__task_update": "",
		"Bash":                          "",
	} {
		got, ok := thuluTool(name)
		if got != want || ok != (want != "") {
			t.Errorf("thuluTool(%q) = %q, %v", name, got, ok)
		}
	}
}

func TestLiveTurnFollowsThenReadsTranscript(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	m.BeginLive("chat-1", "msg-1")
	lt := m.liveTurn("chat-1", "msg-1")
	lt.add(`{"type":"system"}`)

	got := make(chan []string, 1)
	go func() {
		var lines []string
		m.FollowTurn(context.Background(), "chat-1", "msg-1", func(l string) bool {
			lines = append(lines, l)
			return true
		})
		got <- lines
	}()
	time.Sleep(20 * time.Millisecond)
	lt.add(`{"type":"result"}`)
	m.EndLive("chat-1", "msg-1")

	select {
	case lines := <-got:
		if len(lines) != 2 {
			t.Fatalf("live lines = %v", lines)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FollowTurn did not return when the turn ended")
	}

	// After the end, a new follower reads the file.
	var replay []string
	m.FollowTurn(context.Background(), "chat-1", "msg-1", func(l string) bool {
		replay = append(replay, l)
		return true
	})
	if len(replay) != 2 || replay[1] != `{"type":"result"}` {
		t.Errorf("replay = %v", replay)
	}
}

func TestTranscriptPathRefusesPathIDs(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	for _, id := range []string{"", ".", "..", "a/b", "../x"} {
		if p := m.TranscriptPath("chat-1", id); p != "" {
			t.Errorf("TranscriptPath(%q) = %q", id, p)
		}
	}
	if _, err := m.Transcript("chat-1", "missing"); !os.IsNotExist(err) {
		t.Errorf("missing transcript err = %v", err)
	}
}

func TestLineWriterSplitsAndFlushes(t *testing.T) {
	var got []string
	w := &lineWriter{sink: func(l string) { got = append(got, l) }}
	_, _ = w.Write([]byte("a\nb"))
	_, _ = w.Write([]byte("c\r\n\nd"))
	w.Flush()
	if !slices.Equal(got, []string{"a", "bc", "d"}) {
		t.Errorf("lines = %q", got)
	}
}

func TestTitleFrom(t *testing.T) {
	for in, want := range map[string]string{
		"\n  ¿Por qué volvió a Fix local?\nmás": "¿Por qué volvió a Fix local?",
		strings.Repeat("palabra ", 20):          "palabra palabra palabra palabra palabra palabra palabra…",
		"   ":                                   "",
	} {
		if got := titleFrom(in); got != want {
			t.Errorf("titleFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"":                        true,
		"Manager":                 true,
		"Manager · ABC-123: algo": true,
		"Claves PAPER":            false,
	} {
		if got := defaultTitle(&chat.Chat{AgentType: "manager", Title: title}); got != want {
			t.Errorf("defaultTitle(%q) = %v", title, got)
		}
	}
}
