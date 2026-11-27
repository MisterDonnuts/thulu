package hostrun

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// What of the machine's own tooling a run gets. Thulu's MCPs are wired in every mode.
const (
	// Only the MCPs Thulu wires (--strict-mcp-config).
	LocalToolsThulu = runner.LocalToolsThulu
	// Thulu's MCPs plus the machine's MCPs listed in local_mcp.
	LocalToolsSelected = runner.LocalToolsSelected
	// Every MCP the machine has, claude.ai connectors included.
	LocalToolsAll = runner.LocalToolsAll
)

// LocalMCPConfigEnv, in the runner's environment, names the file of local MCP definitions over
// runner.json's local_mcp_config.
const LocalMCPConfigEnv = "OCTOPUS_LOCAL_MCP_CONFIG"

// ConnectorPrefix starts the name claude gives a claude.ai connector, the name `claude mcp list`
// shows and local_mcp holds.
const ConnectorPrefix = "claude.ai "

func IsConnector(name string) bool { return strings.HasPrefix(name, ConnectorPrefix) }

func ValidLocalTools(v string) bool {
	return v == LocalToolsThulu || v == LocalToolsSelected || v == LocalToolsAll
}

// LocalMCPFile is the file of local MCP definitions a run reads: the environment's, else
// runner.json's.
func (c *Config) LocalMCPFile() string {
	if p := strings.TrimSpace(os.Getenv(LocalMCPConfigEnv)); p != "" {
		return p
	}
	return c.LocalMCPConfig
}

// Mode is the local_tools a run uses. A runner.json from before the setting with a local MCP file
// meant selected.
func (c *Config) Mode() string {
	if ValidLocalTools(c.LocalTools) {
		return c.LocalTools
	}
	if c.LocalMCPFile() != "" {
		return LocalToolsSelected
	}
	return LocalToolsThulu
}

// Selection is what LocalToolsSelected adds to a run.
type Selection struct {
	Servers    map[string]json.RawMessage
	Connectors []string
	// Listed in local_mcp with no definition in the file.
	Missing []string
}

func (s Selection) Names() []string {
	out := make([]string, 0, len(s.Servers)+len(s.Connectors))
	for n := range s.Servers {
		out = append(out, n)
	}
	out = append(out, s.Connectors...)
	sort.Strings(out)
	return out
}

func (c *Config) Selection() (Selection, error) {
	sel := Selection{Servers: map[string]json.RawMessage{}}
	defs := map[string]json.RawMessage{}
	if file := c.LocalMCPFile(); file != "" {
		var err error
		if defs, err = readMCPFile(file); err != nil {
			return sel, err
		}
	}
	if len(c.LocalMCP) == 0 {
		sel.Servers = defs
		return sel, nil
	}
	for _, name := range c.LocalMCP {
		switch def, ok := defs[name]; {
		case IsConnector(name):
			sel.Connectors = append(sel.Connectors, name)
		case ok:
			sel.Servers[name] = def
		default:
			sel.Missing = append(sel.Missing, name)
		}
	}
	return sel, nil
}

type mcpFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

func readMCPFile(path string) (map[string]json.RawMessage, error) {
	b, err := readResolved(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.read_mcp_file", "path", path)
	}
	var f mcpFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.read_mcp_file", "path", path)
	}
	if f.MCPServers == nil {
		f.MCPServers = map[string]json.RawMessage{}
	}
	return f.MCPServers, nil
}

// 0600: a definition can carry a token in its env or headers.
func writeMCPFile(path string, servers map[string]json.RawMessage) error {
	b, err := json.MarshalIndent(mcpFile{MCPServers: servers}, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.write_mcp_file", "path", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.write_mcp_file", "path", path)
	}
	if err := fsx.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return apperr.Wrap(err, "hostrun.errors.write_mcp_file", "path", path)
	}
	return nil
}

// FoundMCP is one MCP server claude has on this machine, and where it is defined.
type FoundMCP struct {
	Name   string
	Origin string
	Def    json.RawMessage
}

// ClaudeJSONPath is the file where claude keeps the user's and each project's MCP servers.
func ClaudeJSONPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", apperr.Wrap(err, "hostrun.errors.home")
	}
	return filepath.Join(home, ".claude.json"), nil
}

// FindLocalMCPs lists the servers in claudeJSON's user scope, and with project set, the ones in its
// local scope for that folder and in the folder's .mcp.json. A name defined twice keeps the first:
// project, then local, then user, the order claude prefers them in.
func FindLocalMCPs(claudeJSON, project string) ([]FoundMCP, error) {
	var global struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		} `json:"projects"`
	}
	b, err := readResolved(claudeJSON)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, apperr.Wrap(err, "hostrun.errors.read_mcp_file", "path", claudeJSON)
	default:
		if err := json.Unmarshal(b, &global); err != nil {
			return nil, apperr.Wrap(err, "hostrun.errors.read_mcp_file", "path", claudeJSON)
		}
	}
	var out []FoundMCP
	seen := map[string]bool{}
	add := func(servers map[string]json.RawMessage, origin string) {
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				out = append(out, FoundMCP{Name: n, Origin: origin, Def: servers[n]})
			}
		}
	}
	if project != "" {
		abs, aerr := filepath.Abs(project)
		if aerr == nil {
			project = abs
		}
		repoFile := filepath.Join(project, ".mcp.json")
		defs, rerr := readMCPFile(repoFile)
		if rerr != nil {
			return nil, rerr
		}
		add(defs, repoFile)
		if p, ok := global.Projects[project]; ok {
			add(p.MCPServers, claudeJSON+" ("+project+")")
		}
	}
	add(global.MCPServers, claudeJSON)
	return out, nil
}

// DefaultLocalMCPPath is where `thulu runner mcp` keeps definitions when runner.json names no file.
func DefaultLocalMCPPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "local-mcp.json")
}

// MCPChange is what an import, add or remove did to runner.json.
type MCPChange struct {
	Names    []string
	File     string
	Mode     string
	Switched bool
}

// ImportMCPs copies the found servers named in names (all of them when names is empty) into the
// local MCP file and lists them in local_mcp.
func ImportMCPs(configPath string, found []FoundMCP, names []string) (MCPChange, error) {
	pick := map[string]FoundMCP{}
	for _, f := range found {
		if len(names) == 0 || slices.Contains(names, f.Name) {
			pick[f.Name] = f
		}
	}
	for _, n := range names {
		if _, ok := pick[n]; !ok && !IsConnector(n) {
			return MCPChange{}, apperr.NotFound("hostrun.errors.mcp_not_found", "name", n)
		}
	}
	defs := map[string]json.RawMessage{}
	var listed []string
	for n, f := range pick {
		defs[n] = f.Def
		listed = append(listed, n)
	}
	for _, n := range names {
		if IsConnector(n) {
			listed = append(listed, n)
		}
	}
	if len(listed) == 0 {
		return MCPChange{}, apperr.NotFound("hostrun.errors.no_local_mcps")
	}
	return changeMCPs(configPath, defs, listed, nil)
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// AddMCP lists one server. A connector needs no definition; any other name needs one, unless the
// local MCP file already has it.
func AddMCP(configPath, name string, def json.RawMessage) (MCPChange, error) {
	name = strings.TrimSpace(name)
	if !IsConnector(name) && !serverName.MatchString(name) {
		return MCPChange{}, apperr.Invalid("hostrun.errors.mcp_bad_name", "name", name)
	}
	defs := map[string]json.RawMessage{}
	if def != nil && !IsConnector(name) {
		defs[name] = def
	}
	return changeMCPs(configPath, defs, []string{name}, nil)
}

func RemoveMCP(configPath, name string) (MCPChange, error) {
	return changeMCPs(configPath, nil, nil, []string{strings.TrimSpace(name)})
}

func changeMCPs(configPath string, add map[string]json.RawMessage, list, drop []string) (MCPChange, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return MCPChange{}, err
	}
	ch := MCPChange{Names: append(slices.Clone(list), drop...)}
	// Adding to a runner on "thulu" means the person wants the server in runs: switch to
	// "selected", decided before the file below makes an unset mode read as selected.
	if len(list) > 0 && cfg.Mode() == LocalToolsThulu {
		cfg.LocalTools, ch.Switched = LocalToolsSelected, true
	}
	ch.File = cfg.LocalMCPFile()
	if ch.File == "" {
		ch.File = DefaultLocalMCPPath(cfg.path)
		cfg.LocalMCPConfig = ch.File
	}
	defs, err := readMCPFile(ch.File)
	if err != nil {
		return MCPChange{}, err
	}
	// An empty list meant every server in the file; spell that out before it gets a first entry.
	if len(cfg.LocalMCP) == 0 {
		cfg.LocalMCP = slices.Collect(maps.Keys(defs))
	}
	if err := cfg.addMCPs(defs, add, list); err != nil {
		return MCPChange{}, err
	}
	if err := cfg.dropMCPs(defs, drop); err != nil {
		return MCPChange{}, err
	}
	sort.Strings(cfg.LocalMCP)
	ch.Mode = cfg.Mode()
	if err := writeMCPFile(ch.File, defs); err != nil {
		return MCPChange{}, err
	}
	if err := cfg.Save(cfg.path); err != nil {
		return MCPChange{}, err
	}
	sort.Strings(ch.Names)
	return ch, nil
}

// addMCPs lists each name, storing its definition from add; a name with no definition in add or in
// the file must be a connector, which claude.ai defines.
func (c *Config) addMCPs(defs, add map[string]json.RawMessage, list []string) error {
	for _, n := range list {
		_, given := add[n]
		_, have := defs[n]
		if !given && !have && !IsConnector(n) {
			return apperr.Invalid("hostrun.errors.mcp_needs_definition", "name", n)
		}
	}
	maps.Copy(defs, add)
	for _, n := range list {
		if !slices.Contains(c.LocalMCP, n) {
			c.LocalMCP = append(c.LocalMCP, n)
		}
	}
	return nil
}

func (c *Config) dropMCPs(defs map[string]json.RawMessage, drop []string) error {
	for _, n := range drop {
		_, have := defs[n]
		if !have && !slices.Contains(c.LocalMCP, n) {
			return apperr.NotFound("hostrun.errors.mcp_not_listed", "name", n)
		}
		c.LocalMCP = slices.DeleteFunc(c.LocalMCP, func(s string) bool { return s == n })
		delete(defs, n)
	}
	return nil
}

// SetLocalTools writes local_tools; the running runner reads it on its next job.
func SetLocalTools(configPath, mode string) error {
	if !ValidLocalTools(mode) {
		return apperr.Invalid("hostrun.errors.bad_local_tools", "value", mode)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	cfg.LocalTools = mode
	return cfg.Save(cfg.path)
}

// SetAllowedTools replaces allowed_tools; no rules clears it.
func SetAllowedTools(configPath string, rules []string) error {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	cfg.AllowedTools = nil
	for _, r := range rules {
		if r = strings.TrimSpace(r); r != "" && !slices.Contains(cfg.AllowedTools, r) {
			cfg.AllowedTools = append(cfg.AllowedTools, r)
		}
	}
	return cfg.Save(cfg.path)
}

// current reads the file again for the settings a job takes at its start, so `thulu runner config`
// and `thulu runner mcp` count from the next job without a restart. On a read error the config the
// runner started with stands.
func (c *Config) current() *Config {
	if c.path == "" {
		return c
	}
	fresh, err := LoadConfig(c.path)
	if err != nil {
		return c
	}
	return fresh
}

// ToolsState is what the runner reports about its local_tools.
func (c *Config) ToolsState(ctx context.Context) *runner.LocalToolsState {
	st := &runner.LocalToolsState{Mode: c.Mode(), AllowedTools: len(c.AllowedTools)}
	if st.Mode == LocalToolsSelected {
		if sel, err := c.Selection(); err == nil {
			st.MCP = sel.Names()
		}
	}
	st.BypassDisabled = ReadPolicy(ctx, c.UserSettings).BypassDisabled
	return st
}

var toolNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// ToolRule is the permission rule that covers every tool of an MCP server, named the way claude
// names its tools: "claude.ai Slack" becomes mcp__claude_ai_Slack__*.
func ToolRule(server string) string {
	return "mcp__" + toolNameUnsafe.ReplaceAllString(server, "_") + "__*"
}
