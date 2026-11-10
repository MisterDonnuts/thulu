package types

import (
	"reflect"
	"testing"
)

func TestParseEnginesSupported_EmptyFallsBackToAgentEngine(t *testing.T) {
	got := parseEnginesSupported("", "claude-code")
	want := []string{"claude-code"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseEnginesSupported_EmptyAndNoFallback(t *testing.T) {
	if got := parseEnginesSupported("", ""); got != nil {
		t.Errorf("expected nil for empty label and no fallback, got %v", got)
	}
}

func TestParseEnginesSupported_TrimsAndDedupes(t *testing.T) {
	got := parseEnginesSupported(" claude-code , openai-codex , claude-code , aider ", "claude-code")
	want := []string{"claude-code", "openai-codex", "aider"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAgentType_SupportsEngine(t *testing.T) {
	at := &AgentType{
		Engine:           "claude-code",
		SupportedEngines: []string{"claude-code", "openai-codex"},
	}
	if !at.SupportsEngine("claude-code") {
		t.Error("expected claude-code to be supported")
	}
	if !at.SupportsEngine("openai-codex") {
		t.Error("expected openai-codex to be supported")
	}
	if at.SupportsEngine("aider") {
		t.Error("expected aider NOT to be supported")
	}
	if at.SupportsEngine("") {
		t.Error("empty engine must never be supported")
	}
}

func TestAgentType_SupportsEngine_EmptyListImpliesAgentEngine(t *testing.T) {
	at := &AgentType{Engine: "claude-code"}
	if !at.SupportsEngine("claude-code") {
		t.Error("empty SupportedEngines must imply [Engine]")
	}
	if at.SupportsEngine("openai-codex") {
		t.Error("empty SupportedEngines must NOT permit other engines")
	}
}
