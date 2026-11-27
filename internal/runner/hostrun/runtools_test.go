package hostrun

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

func TestParseMCPList(t *testing.T) {
	out := `Checking MCP server health...

claude.ai Slack: https://mcp.slack.com/mcp - ✔ Connected
claude.ai Gmail: https://gmail.mcp.claude.com/mcp - ! Needs authentication
github: https://api.githubcopilot.com/mcp/ (HTTP) - ✘ Failed to connect
pg: npx -y pg-mcp - ⏸ Pending approval
old: node old.js - ⊘ Disabled
fs: npx fs - ✘ Blocked by enterprise managed policy
`
	got := parseMCPList([]byte(out))
	want := map[string]string{
		"claude.ai Slack": mcpConnected, "claude.ai Gmail": mcpNeedsAuth, "github": mcpFailed,
		"pg": mcpPending, "old": mcpDisabled, "fs": mcpBlocked,
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %+v", got)
	}
	for _, m := range got {
		if want[m.Name] != m.Status {
			t.Errorf("%s: status %s, want %s", m.Name, m.Status, want[m.Name])
		}
	}
	if got[2].Target != "https://api.githubcopilot.com/mcp/ (HTTP)" {
		t.Fatalf("target = %q", got[2].Target)
	}
}

func toolsJob(t *testing.T, cfg *Config) *job {
	t.Helper()
	return &job{r: &Runner{cfg: cfg}, root: t.TempDir(),
		spec: &runner.JobSpec{Env: map[string]string{"MCP_TOOLS": "octopus-api@write,argus-browser", "ENGINE": "claude-code"}}}
}

func withMCPList(t *testing.T, list []ListedMCP, err error) *int {
	t.Helper()
	calls := 0
	old := listMCPs
	t.Cleanup(func() { listMCPs = old })
	listMCPs = func(context.Context, []string, string) ([]ListedMCP, error) {
		calls++
		return list, err
	}
	return &calls
}

func localMCPFixture(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "local-mcp.json")
	writeJSON(t, file, map[string]any{"mcpServers": map[string]any{
		"pg":     map[string]any{"type": "stdio", "command": "pg"},
		"unused": map[string]any{"type": "stdio", "command": "unused"},
	}})
	return file
}

func TestPlanToolsModes(t *testing.T) {
	file := localMCPFixture(t)
	t.Run("thulu", func(t *testing.T) {
		tools, err := toolsJob(t, &Config{LocalMCP: []string{"pg"}}).planTools(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		tools.env(m)
		if m["OCTOPUS_LOCAL_TOOLS"] != LocalToolsThulu || m[LocalMCPConfigEnv] != "" || m["OCTOPUS_LOCAL_MCP_CONNECTORS"] != "" {
			t.Fatalf("thulu passes nothing of the machine: %v", m)
		}
	})
	t.Run("selected writes only the chosen servers", func(t *testing.T) {
		j := toolsJob(t, &Config{LocalTools: LocalToolsSelected, LocalMCPConfig: file, LocalMCP: []string{"pg", "gone"}})
		tools, err := j.planTools(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		tools.env(m)
		if m[LocalMCPConfigEnv] != filepath.Join(j.root, "local-mcp.json") || m["OCTOPUS_LOCAL_MCP_CONNECTORS"] != "" {
			t.Fatalf("env = %v", m)
		}
		defs, err := readMCPFile(m[LocalMCPConfigEnv])
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := defs["pg"]; !ok || len(defs) != 1 {
			t.Fatalf("the run's file holds the selection only: %v", defs)
		}
		if len(tools.warnings) != 1 || !strings.Contains(tools.warnings[0], "gone") {
			t.Fatalf("a selected name missing from the file warns the task: %v", tools.warnings)
		}
	})
	t.Run("all", func(t *testing.T) {
		tools, err := toolsJob(t, &Config{LocalTools: LocalToolsAll, LocalMCPConfig: file}).planTools(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		tools.env(m)
		if m["OCTOPUS_LOCAL_TOOLS"] != LocalToolsAll || m[LocalMCPConfigEnv] != "" {
			t.Fatalf("all hands claude the machine as it is: %v", m)
		}
	})
}

func TestPlanToolsDeniesWhatConnectorsDoNotSelect(t *testing.T) {
	file := localMCPFixture(t)
	calls := withMCPList(t, []ListedMCP{
		{Name: "claude.ai Slack"}, {Name: "claude.ai Gmail"}, {Name: "pg"}, {Name: "unused"}, {Name: "octopus-api"},
	}, nil)
	cfg := &Config{LocalTools: LocalToolsSelected, LocalMCPConfig: file, LocalMCP: []string{"claude.ai Slack", "pg"}}
	r := &Runner{cfg: cfg}
	for range 2 {
		j := toolsJob(t, cfg)
		j.r = r
		tools, err := j.planTools(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		tools.env(m)
		if m["OCTOPUS_LOCAL_MCP_CONNECTORS"] != "1" {
			t.Fatalf("env = %v", m)
		}
		deny := strings.Split(m["OCTOPUS_LOCAL_MCP_DENY"], "\n")
		if want := []string{"mcp__claude_ai_Gmail__*", "mcp__unused__*"}; !slices.Equal(deny, want) {
			t.Fatalf("deny = %v, want %v", deny, want)
		}
	}
	if *calls != 1 {
		t.Fatalf("claude mcp list ran %d times; the runner keeps its answer for a while", *calls)
	}
}

func TestPlanToolsTurnsConnectorsOffWhenTheListFails(t *testing.T) {
	withMCPList(t, nil, errors.New("claude: not found"))
	cfg := &Config{LocalTools: LocalToolsSelected, LocalMCPConfig: localMCPFixture(t), LocalMCP: []string{"claude.ai Slack", "pg"}}
	tools, err := toolsJob(t, cfg).planTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	tools.env(m)
	if m["OCTOPUS_LOCAL_MCP_CONNECTORS"] != "" || m[LocalMCPConfigEnv] == "" {
		t.Fatalf("without the list the run stays strict with the file servers: %v", m)
	}
	if len(tools.warnings) != 1 || !strings.Contains(tools.warnings[0], "claude: not found") {
		t.Fatalf("warnings = %v", tools.warnings)
	}
}

func TestPlanToolsPassesAllowedToolsWhenPolicyTurnsBypassOff(t *testing.T) {
	dir, _ := withPolicyFixture(t, nil, nil)
	writeJSON(t, filepath.Join(dir, "managed-settings.json"), map[string]any{
		"permissions": map[string]any{"disableBypassPermissionsMode": "disable"},
	})
	cfg := &Config{LocalTools: LocalToolsSelected, LocalMCPConfig: localMCPFixture(t), LocalMCP: []string{"pg"},
		AllowedTools: []string{"Bash(git *)", "Edit"}}
	tools, err := toolsJob(t, cfg).planTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	tools.env(m)
	if m["OCTOPUS_BYPASS_DISABLED"] != "1" {
		t.Fatalf("env = %v", m)
	}
	want := []string{"Bash(git *)", "Edit", "mcp__octopus-api__*", "mcp__argus-browser__*", "mcp__pg__*"}
	if got := strings.Split(m["OCTOPUS_ALLOWED_TOOLS"], "\n"); !slices.Equal(got, want) {
		t.Fatalf("allowed = %v, want %v", got, want)
	}
	if len(tools.warnings) != 0 || len(tools.notes) != 1 {
		t.Fatalf("with allowed_tools set the run only notes the policy: %v / %v", tools.warnings, tools.notes)
	}

	cfg.AllowedTools = nil
	tools, err = toolsJob(t, cfg).planTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.warnings) != 1 || !strings.Contains(tools.warnings[0], "allowed-tools") {
		t.Fatalf("without allowed_tools the task hears how to fix it: %v", tools.warnings)
	}
}

func TestRunToolsEnvClearsInheritedValues(t *testing.T) {
	m := map[string]string{LocalMCPConfigEnv: "/from/the/runner/env.json", "OCTOPUS_BYPASS_DISABLED": "1"}
	runTools{mode: LocalToolsThulu}.env(m)
	if m[LocalMCPConfigEnv] != "" || m["OCTOPUS_BYPASS_DISABLED"] != "" {
		t.Fatalf("the runner's own env must not leak into a thulu run: %v", m)
	}
}
