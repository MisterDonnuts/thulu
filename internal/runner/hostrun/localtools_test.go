package hostrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeToolsConfig(t *testing.T, cfg map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runner.json")
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["runner_id"] = "r1"
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeJSONFixture is a ~/.claude.json with a user-scope server, a local-scope one for project and
// a name both scopes define.
func claudeJSONFixture(t *testing.T, project string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{
		"numStartups": 12,
		"mcpServers": map[string]any{
			"github": map[string]any{"type": "http", "url": "https://api.githubcopilot.com/mcp/"},
			"shared": map[string]any{"type": "stdio", "command": "user-shared"},
		},
		"projects": map[string]any{
			project: map[string]any{"mcpServers": map[string]any{
				"postgres": map[string]any{"type": "stdio", "command": "npx", "args": []string{"-y", "pg-mcp"}},
				"shared":   map[string]any{"type": "stdio", "command": "local-shared"},
			}},
		},
	})
	return path
}

func TestFindLocalMCPsReadsEveryScopeInClaudeOrder(t *testing.T) {
	project := t.TempDir()
	writeJSON(t, filepath.Join(project, ".mcp.json"), map[string]any{"mcpServers": map[string]any{
		"linear": map[string]any{"type": "sse", "url": "https://mcp.linear.app/sse"},
	}})
	claudeJSON := claudeJSONFixture(t, project)

	found, err := FindLocalMCPs(claudeJSON, project)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]FoundMCP{}
	for _, f := range found {
		names = append(names, f.Name)
		byName[f.Name] = f
	}
	if want := []string{"linear", "postgres", "shared", "github"}; !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if !strings.Contains(string(byName["shared"].Def), "local-shared") {
		t.Fatalf("the local scope must win over the user scope: %s", byName["shared"].Def)
	}

	userOnly, err := FindLocalMCPs(claudeJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(userOnly) != 2 {
		t.Fatalf("without a project only the user scope counts: %+v", userOnly)
	}
}

func TestFindLocalMCPsFollowsASymlinkedClaudeJSON(t *testing.T) {
	real := claudeJSONFixture(t, "/nowhere")
	link := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	found, err := FindLocalMCPs(link, "")
	if err != nil || len(found) != 2 {
		t.Fatalf("found %+v, err %v", found, err)
	}
}

func TestImportMCPsFillsTheFileAndLocalMCP(t *testing.T) {
	project := t.TempDir()
	claudeJSON := claudeJSONFixture(t, project)
	config := writeToolsConfig(t, nil)
	found, err := FindLocalMCPs(claudeJSON, project)
	if err != nil {
		t.Fatal(err)
	}

	ch, err := ImportMCPs(config, found, []string{"postgres", "claude.ai Slack"})
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Switched || ch.Mode != LocalToolsSelected {
		t.Fatalf("importing on a thulu runner switches it to selected: %+v", ch)
	}
	if ch.File != filepath.Join(filepath.Dir(config), "local-mcp.json") {
		t.Fatalf("file = %s", ch.File)
	}
	cfg, err := LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalTools != LocalToolsSelected || cfg.LocalMCPConfig != ch.File {
		t.Fatalf("runner.json = %+v", cfg)
	}
	if want := []string{"claude.ai Slack", "postgres"}; !slices.Equal(cfg.LocalMCP, want) {
		t.Fatalf("local_mcp = %v, want %v", cfg.LocalMCP, want)
	}
	defs, err := readMCPFile(ch.File)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defs["postgres"]; !ok || len(defs) != 1 {
		t.Fatalf("the file holds only the imported servers, never a connector: %v", defs)
	}
	info, err := os.Stat(ch.File)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the MCP file can carry tokens and must be 0600: %v %v", info.Mode(), err)
	}

	if _, err := ImportMCPs(config, found, []string{"nope"}); err == nil {
		t.Fatal("importing a name nobody defines must fail")
	}
}

func TestAddAndRemoveMCP(t *testing.T) {
	config := writeToolsConfig(t, map[string]any{"local_tools": "all"})
	def, err := LocalMCPDef("", nil, "https://mcp.example.com", "", map[string]string{"Authorization": "Bearer x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := AddMCP(config, "example", def)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Switched || ch.Mode != LocalToolsAll {
		t.Fatalf("adding never changes a mode the person chose other than thulu: %+v", ch)
	}
	stdio, err := LocalMCPDef("npx", []string{"-y", "srv"}, "", "", nil, map[string]string{"TOKEN": "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddMCP(config, "local", stdio); err != nil {
		t.Fatal(err)
	}
	if _, err := AddMCP(config, "claude.ai Notion", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := AddMCP(config, "undefined", nil); err == nil {
		t.Fatal("a server with no definition anywhere must be refused")
	}
	if _, err := AddMCP(config, "bad name", stdio); err == nil {
		t.Fatal("a name with a space that is not a connector must be refused")
	}

	cfg, _ := LoadConfig(config)
	sel, err := cfg.Selection()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude.ai Notion", "example", "local"}; !slices.Equal(sel.Names(), want) {
		t.Fatalf("selection = %v, want %v", sel.Names(), want)
	}
	var example struct {
		Type    string            `json:"type"`
		Headers map[string]string `json:"headers"`
	}
	var local struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	_ = json.Unmarshal(sel.Servers["example"], &example)
	_ = json.Unmarshal(sel.Servers["local"], &local)
	if example.Type != "http" || example.Headers["Authorization"] != "Bearer x" ||
		local.Command != "npx" || !slices.Equal(local.Args, []string{"-y", "srv"}) || local.Env["TOKEN"] != "t" {
		t.Fatalf("definitions = %s / %s", sel.Servers["example"], sel.Servers["local"])
	}

	if _, err := RemoveMCP(config, "example"); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveMCP(config, "example"); err == nil {
		t.Fatal("removing a server twice must say it is not there")
	}
	cfg, _ = LoadConfig(config)
	if slices.Contains(cfg.LocalMCP, "example") {
		t.Fatalf("local_mcp = %v", cfg.LocalMCP)
	}
	defs, _ := readMCPFile(cfg.LocalMCPConfig)
	if _, ok := defs["example"]; ok {
		t.Fatal("remove must drop the definition too")
	}
}

func TestLocalToolsModeAndOverride(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mine.json")
	writeJSON(t, file, map[string]any{"mcpServers": map[string]any{"a": map[string]any{"command": "a"}}})

	cases := []struct {
		name string
		cfg  *Config
		env  string
		want string
	}{
		{"default", &Config{}, "", LocalToolsThulu},
		{"explicit", &Config{LocalTools: LocalToolsAll}, "", LocalToolsAll},
		// A runner.json from 1.1.0 with only local_mcp_config meant "load these".
		{"legacy file", &Config{LocalMCPConfig: file}, "", LocalToolsSelected},
		{"env override", &Config{}, file, LocalToolsSelected},
		{"explicit wins over env", &Config{LocalTools: LocalToolsThulu}, file, LocalToolsThulu},
		{"garbage falls back", &Config{LocalTools: "everything"}, "", LocalToolsThulu},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(LocalMCPConfigEnv, tc.env)
			if got := tc.cfg.Mode(); got != tc.want {
				t.Fatalf("Mode() = %s, want %s", got, tc.want)
			}
		})
	}

	t.Setenv(LocalMCPConfigEnv, file)
	c := Config{LocalMCPConfig: "/somewhere/else.json"}
	if c.LocalMCPFile() != file {
		t.Fatalf("%s must win over runner.json: %s", LocalMCPConfigEnv, c.LocalMCPFile())
	}
}

func TestSelectionReportsMissingNames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mcp.json")
	writeJSON(t, file, map[string]any{"mcpServers": map[string]any{"a": map[string]any{"command": "a"}}})
	c := Config{LocalMCPConfig: file, LocalMCP: []string{"a", "gone", "claude.ai Slack"}}
	sel, err := c.Selection()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sel.Missing, []string{"gone"}) || !slices.Equal(sel.Connectors, []string{"claude.ai Slack"}) {
		t.Fatalf("selection = %+v", sel)
	}
	all := Config{LocalMCPConfig: file}
	if sel, _ := all.Selection(); len(sel.Servers) != 1 {
		t.Fatalf("an empty local_mcp selects every server in the file: %+v", sel)
	}
}

func TestSetLocalToolsAndAllowedTools(t *testing.T) {
	config := writeToolsConfig(t, nil)
	if err := SetLocalTools(config, "everything"); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
	if err := SetLocalTools(config, LocalToolsSelected); err != nil {
		t.Fatal(err)
	}
	if err := SetAllowedTools(config, []string{"Bash(git *)", " Edit ", "Bash(git *)", ""}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadConfig(config)
	if cfg.LocalTools != LocalToolsSelected || !slices.Equal(cfg.AllowedTools, []string{"Bash(git *)", "Edit"}) {
		t.Fatalf("runner.json = %+v", cfg)
	}
	if err := SetAllowedTools(config, nil); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := LoadConfig(config); cfg.AllowedTools != nil {
		t.Fatalf("no rules clears allowed_tools: %v", cfg.AllowedTools)
	}
}

func TestToolRuleNamesToolsLikeClaude(t *testing.T) {
	for in, want := range map[string]string{
		"octopus-api":     "mcp__octopus-api__*",
		"claude.ai Slack": "mcp__claude_ai_Slack__*",
		"my.server":       "mcp__my_server__*",
	} {
		if got := ToolRule(in); got != want {
			t.Errorf("ToolRule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolsStateReportsTheMachineSetting(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mcp.json")
	writeJSON(t, file, map[string]any{"mcpServers": map[string]any{"pg": map[string]any{"command": "pg"}}})
	c := &Config{LocalTools: LocalToolsSelected, LocalMCPConfig: file, LocalMCP: []string{"pg", "claude.ai Slack"},
		AllowedTools: []string{"Edit"}}
	st := c.ToolsState(t.Context())
	if st.Mode != LocalToolsSelected || !slices.Equal(st.MCP, []string{"claude.ai Slack", "pg"}) || st.AllowedTools != 1 || st.BypassDisabled {
		t.Fatalf("state = %+v", st)
	}
	if st := (&Config{}).ToolsState(t.Context()); st.Mode != LocalToolsThulu || st.MCP != nil {
		t.Fatalf("a thulu runner reports no list: %+v", st)
	}
}
