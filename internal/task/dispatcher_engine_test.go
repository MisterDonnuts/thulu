package task

import (
	"context"
	"io"
	"iter"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type stubEngine struct {
	name string
	caps engine.Capabilities
}

func (s *stubEngine) Name() string { return s.name }
func (s *stubEngine) ParseStream(io.Reader) iter.Seq[engine.Event] {
	return func(func(engine.Event) bool) {}
}
func (s *stubEngine) AuthEnv(context.Context) []string  { return nil }
func (s *stubEngine) Capabilities() engine.Capabilities { return s.caps }

var stubOnce sync.Once

func registerStubs(t *testing.T) {
	t.Helper()
	stubOnce.Do(func() {
		engine.Register(&stubEngine{name: "stub-no-caps", caps: engine.Capabilities{}})
		engine.Register(&stubEngine{name: "stub-mcp-only", caps: engine.Capabilities{MCP: true}})
		engine.Register(&stubEngine{name: "stub-effort-only", caps: engine.Capabilities{EffortFlag: true}})
	})
}

func TestApplyEngineEnv_ClaudeCode_Keeps(t *testing.T) {
	env := map[string]string{"EFFORT": "high", "MCP_TOOLS": "octopus-api@write"}
	applyEngineEnv(env, &types.AgentType{Engine: "claude-code"}, "probe")
	if env["ENGINE"] != "claude-code" {
		t.Errorf("ENGINE = %q, want claude-code", env["ENGINE"])
	}
	if env["EFFORT"] != "high" {
		t.Errorf("EFFORT = %q, want high (gate should keep)", env["EFFORT"])
	}
	if env["MCP_TOOLS"] != "octopus-api@write" {
		t.Errorf("MCP_TOOLS = %q, want octopus-api@write (gate should keep)", env["MCP_TOOLS"])
	}
}

func TestApplyEngineEnv_NoCaps_DropsBoth(t *testing.T) {
	registerStubs(t)
	env := map[string]string{"EFFORT": "high", "MCP_TOOLS": "octopus-api@write"}
	applyEngineEnv(env, &types.AgentType{Engine: "stub-no-caps"}, "test")
	if env["ENGINE"] != "stub-no-caps" {
		t.Errorf("ENGINE = %q, want stub-no-caps", env["ENGINE"])
	}
	if _, ok := env["EFFORT"]; ok {
		t.Errorf("EFFORT should have been dropped, got %q", env["EFFORT"])
	}
	if _, ok := env["MCP_TOOLS"]; ok {
		t.Errorf("MCP_TOOLS should have been dropped, got %q", env["MCP_TOOLS"])
	}
}

func TestApplyEngineEnv_MCPOnly_KeepsMCP_DropsEffort(t *testing.T) {
	registerStubs(t)
	env := map[string]string{"EFFORT": "low", "MCP_TOOLS": "x@read"}
	applyEngineEnv(env, &types.AgentType{Engine: "stub-mcp-only"}, "test")
	if env["MCP_TOOLS"] != "x@read" {
		t.Errorf("MCP_TOOLS = %q, want x@read (MCP cap is on)", env["MCP_TOOLS"])
	}
	if _, ok := env["EFFORT"]; ok {
		t.Errorf("EFFORT should have been dropped, got %q", env["EFFORT"])
	}
}

func TestApplyEngineEnv_EffortOnly_KeepsEffort_DropsMCP(t *testing.T) {
	registerStubs(t)
	env := map[string]string{"EFFORT": "medium", "MCP_TOOLS": "x@read"}
	applyEngineEnv(env, &types.AgentType{Engine: "stub-effort-only"}, "test")
	if env["EFFORT"] != "medium" {
		t.Errorf("EFFORT = %q, want medium (EffortFlag cap is on)", env["EFFORT"])
	}
	if _, ok := env["MCP_TOOLS"]; ok {
		t.Errorf("MCP_TOOLS should have been dropped, got %q", env["MCP_TOOLS"])
	}
}

func TestApplyEngineEnv_EmptyEngine_FallsBackToDefault(t *testing.T) {
	env := map[string]string{}
	applyEngineEnv(env, &types.AgentType{}, "noname")
	if env["ENGINE"] != engine.DefaultName {
		t.Errorf("ENGINE = %q, want %q", env["ENGINE"], engine.DefaultName)
	}
}

func TestApplyEngineEnv_UnknownEngine_PassesThrough(t *testing.T) {
	env := map[string]string{"EFFORT": "high", "MCP_TOOLS": "x@read"}
	applyEngineEnv(env, &types.AgentType{Engine: "nope-not-registered"}, "test")
	if env["ENGINE"] != "nope-not-registered" {
		t.Errorf("ENGINE = %q, want nope-not-registered (set even when unknown)", env["ENGINE"])
	}
	if env["EFFORT"] != "high" || env["MCP_TOOLS"] != "x@read" {
		t.Errorf("env was gated despite unknown engine: %+v", env)
	}
}
