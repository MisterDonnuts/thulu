package engine_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
)

func TestLastResult(t *testing.T) {
	ev, ok := engine.LastResult(readFixture(t, "claude-code", "error"), "")
	if !ok || !ev.IsError || !ev.APIError || ev.APIErrorStatus != 529 {
		t.Fatalf("claude-code error fixture = %+v, %v; want an API error 529", ev, ok)
	}
	ev, ok = engine.LastResult(readFixture(t, "codex", "error"), "claude-code")
	if !ok || !ev.APIError || ev.APIErrorStatus != 401 {
		t.Fatalf("codex error fixture = %+v, %v; want an API error 401", ev, ok)
	}
	ev, ok = engine.LastResult(readFixture(t, "claude-code", "ok"), "")
	if !ok || ev.IsError || ev.APIError {
		t.Fatalf("claude-code ok fixture = %+v, %v; want a clean result", ev, ok)
	}
	if _, ok := engine.LastResult([]byte("exit 1\n"), ""); ok {
		t.Fatal("a stream without a result reported one")
	}
}
