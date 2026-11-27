package hostrun

import (
	"errors"
	"path/filepath"
	"testing"
)

func doctorStates(rep DoctorReport) map[string]string {
	out := map[string]string{}
	for _, m := range rep.MCPs {
		out[m.Name] = m.State
	}
	return out
}

func TestDoctorSaysWhatEachMCPGets(t *testing.T) {
	dir, _ := withPolicyFixture(t, nil, nil)
	writeJSON(t, filepath.Join(dir, "managed-settings.json"), map[string]any{
		"permissions":      map[string]any{"disableBypassPermissionsMode": "disable"},
		"deniedMcpServers": []any{map[string]any{"serverName": "fs"}},
	})
	file := filepath.Join(t.TempDir(), "local-mcp.json")
	writeJSON(t, file, map[string]any{"mcpServers": map[string]any{
		"pg": map[string]any{"command": "pg"}, "fs": map[string]any{"command": "fs"}, "offline": map[string]any{"command": "x"},
	}})
	withMCPList(t, []ListedMCP{
		{Name: "pg", Status: mcpConnected},
		{Name: "fs", Status: mcpConnected},
		{Name: "claude.ai Gmail", Status: mcpNeedsAuth},
		{Name: "claude.ai Drive", Status: mcpConnected},
		{Name: "github", Status: mcpFailed},
	}, nil)
	config := writeToolsConfig(t, map[string]any{
		"local_tools": "selected", "local_mcp_config": file,
		"local_mcp":     []string{"pg", "fs", "offline", "claude.ai Gmail", "claude.ai Notion", "gone", "github"},
		"allowed_tools": []string{"Edit"},
	})

	rep, err := Doctor(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mode != LocalToolsSelected || rep.ModeFrom != ModeFromConfig || rep.MCPFile != file {
		t.Fatalf("report = %+v", rep)
	}
	if !rep.Policy.BypassDisabled || len(rep.AllowedTools) != 1 {
		t.Fatalf("policy = %+v, allowed = %v", rep.Policy, rep.AllowedTools)
	}
	want := map[string]string{
		"pg":               MCPLoaded,
		"fs":               MCPPolicy,
		"offline":          MCPUnchecked,
		"claude.ai Gmail":  MCPNeedsAuth,
		"claude.ai Drive":  MCPNotSelected,
		"claude.ai Notion": MCPNotConnected,
		"gone":             MCPMissing,
		"github":           MCPMissing,
	}
	got := doctorStates(rep)
	for name, state := range want {
		if got[name] != state {
			t.Errorf("%s: %s, want %s", name, got[name], state)
		}
	}
	if len(got) != len(want) {
		t.Errorf("rows = %v", got)
	}
}

func TestDoctorOnAThuluRunner(t *testing.T) {
	withPolicyFixture(t, nil, nil)
	withMCPList(t, []ListedMCP{{Name: "claude.ai Slack", Status: mcpConnected}}, nil)
	rep, err := Doctor(t.Context(), writeToolsConfig(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Mode != LocalToolsThulu || rep.ModeFrom != ModeFromDefault || rep.Policy.BypassDisabled {
		t.Fatalf("report = %+v", rep)
	}
	if got := doctorStates(rep); got["claude.ai Slack"] != MCPStrict {
		t.Fatalf("on thulu every machine MCP is left out by the strict flag: %v", got)
	}
}

func TestDoctorKeepsGoingWhenClaudeIsMissing(t *testing.T) {
	withPolicyFixture(t, nil, nil)
	withMCPList(t, nil, errors.New("exec: claude: not found"))
	file := filepath.Join(t.TempDir(), "mcp.json")
	writeJSON(t, file, map[string]any{"mcpServers": map[string]any{"pg": map[string]any{"command": "pg"}}})
	rep, err := Doctor(t.Context(), writeToolsConfig(t, map[string]any{"local_mcp_config": file}))
	if err != nil {
		t.Fatal(err)
	}
	if rep.ListErr == "" || rep.ModeFrom != ModeFromLegacy {
		t.Fatalf("report = %+v", rep)
	}
	if got := doctorStates(rep); got["pg"] != MCPUnchecked {
		t.Fatalf("rows = %v", got)
	}
}
