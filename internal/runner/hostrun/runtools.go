package hostrun

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// ListedMCP is one line of `claude mcp list`: a server claude would load here, and its health.
type ListedMCP struct {
	Name   string
	Target string
	Status string
}

// Health statuses `claude mcp list` prints, by the words they start with.
const (
	mcpConnected = "connected"
	mcpNeedsAuth = "needs_auth"
	mcpFailed    = "failed"
	mcpPending   = "pending"
	mcpDisabled  = "disabled"
	mcpBlocked   = "blocked"
	mcpUnknown   = "unknown"
)

// parseMCPList reads lines like "claude.ai Slack: https://… - ✔ Connected". A line without a
// health status is not a server (a header or a warning).
func parseMCPList(out []byte) []ListedMCP {
	var list []ListedMCP
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		i := strings.LastIndex(line, " - ")
		if i < 0 {
			continue
		}
		head, status := line[:i], strings.TrimSpace(line[i+3:])
		name, target, ok := strings.Cut(head, ": ")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		list = append(list, ListedMCP{Name: strings.TrimSpace(name), Target: strings.TrimSpace(target), Status: mcpHealth(status)})
	}
	return list
}

func mcpHealth(status string) string {
	s := strings.ToLower(status)
	switch {
	case strings.Contains(s, "needs authentication"): //i18n:ignore claude mcp list output
		return mcpNeedsAuth
	case strings.Contains(s, "connected"):
		return mcpConnected
	case strings.Contains(s, "pending approval"): //i18n:ignore claude mcp list output
		return mcpPending
	case strings.Contains(s, "disabled"):
		return mcpDisabled
	case strings.Contains(s, "blocked"):
		return mcpBlocked
	case strings.Contains(s, "failed"), strings.Contains(s, "error"):
		return mcpFailed
	}
	return mcpUnknown
}

// `claude mcp list` connects to every server to report its health; slow servers take seconds.
const mcpListTimeout = time.Minute

var listMCPs = func(ctx context.Context, env []string, dir string) ([]ListedMCP, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "mcp", "list")
	cmd.Env, cmd.Dir = env, dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := runGrouped(cmd); err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.mcp_list")
	}
	return parseMCPList(out.Bytes()), nil
}

// A machine's servers change when its owner adds one; ten minutes keeps a run from paying for the
// health check of every server each time.
const mcpListTTL = 10 * time.Minute

type mcpListCache struct {
	mu   sync.Mutex
	at   time.Time
	list []ListedMCP
}

func (c *mcpListCache) get(ctx context.Context, env []string, dir string) ([]ListedMCP, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list != nil && time.Since(c.at) < mcpListTTL {
		return c.list, nil
	}
	list, err := listMCPs(ctx, env, dir)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []ListedMCP{}
	}
	c.list, c.at = list, time.Now()
	return list, nil
}

// runTools is how a run reaches the machine's tooling, as the claude script reads it from the env.
type runTools struct {
	mode string
	// The selected servers' definitions, written for this run.
	mcpFile    string
	connectors bool
	deny       []string
	bypassOff  bool
	allowed    []string
	// For the task: what the person has to act on.
	warnings []string
	// For the run's log only.
	notes []string
}

// thuluServers are the MCPs the daemon wired into the job ("octopus-api@write" is octopus-api).
func thuluServers(mcpTools string) []string {
	var out []string
	for _, e := range strings.Split(mcpTools, ",") {
		name, _, _ := strings.Cut(e, "@")
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func (j *job) planTools(ctx context.Context) (runTools, error) {
	cfg := j.r.cfg.current()
	t := runTools{mode: cfg.Mode()}
	thulu := thuluServers(j.spec.Env["MCP_TOOLS"])
	var selected []string
	if t.mode == LocalToolsSelected {
		var err error
		if selected, err = j.selectTools(ctx, cfg, &t, thulu); err != nil {
			return t, err
		}
	}
	t.permissions(ReadPolicy(ctx, cfg.UserSettings), cfg.AllowedTools, append(slices.Clone(thulu), selected...))
	return t, nil
}

// selectTools writes the selected servers' definitions for the run and, when connectors are
// selected, the deny rules that keep every other server of the machine out. It returns the names
// selected.
func (j *job) selectTools(ctx context.Context, cfg *Config, t *runTools, thulu []string) ([]string, error) {
	sel, err := cfg.Selection()
	if err != nil {
		return nil, err
	}
	for _, n := range sel.Missing {
		t.warnings = append(t.warnings, i18n.Text("hostrun.warnings.mcp_missing", "name", n, "file", cfg.LocalMCPFile()))
	}
	if len(sel.Servers) > 0 {
		t.mcpFile = filepath.Join(j.root, "local-mcp.json")
		if werr := writeMCPFile(t.mcpFile, sel.Servers); werr != nil {
			return nil, werr
		}
	}
	selected := sel.Names()
	if len(sel.Connectors) == 0 {
		return selected, nil
	}
	deny, err := j.denyUnselected(ctx, append(slices.Clone(selected), thulu...))
	if err != nil {
		// Fail closed: without the list nothing tells the connectors apart, so none load.
		t.warnings = append(t.warnings, i18n.Text("hostrun.warnings.connectors_off", "err", apperr.Text(err)))
		return selected, nil
	}
	t.connectors, t.deny = true, deny
	return selected, nil
}

// permissions handles a policy that turns bypass off: the run then gets allowed_tools plus every
// tool of the servers it was given, and nothing else.
func (t *runTools) permissions(pol Policy, allowedTools, servers []string) {
	if !pol.BypassDisabled {
		return
	}
	t.bypassOff = true
	t.allowed = slices.Clone(allowedTools)
	for _, s := range servers {
		t.allowed = append(t.allowed, ToolRule(s))
	}
	if len(allowedTools) == 0 {
		t.warnings = append(t.warnings, i18n.Text("hostrun.warnings.bypass_off_no_tools", "source", pol.BypassSource))
		return
	}
	t.notes = append(t.notes, i18n.Text("hostrun.warnings.bypass_off", "source", pol.BypassSource,
		"tools", strings.Join(allowedTools, ", ")))
}

// denyUnselected names, as deny rules, every server claude lists on this machine that is neither
// Thulu's nor in keep.
func (j *job) denyUnselected(ctx context.Context, keep []string) ([]string, error) {
	env := envList(loginEnv(j.spec.Env["ENGINE"]))
	list, err := j.r.mcpList.get(ctx, env, j.root)
	if err != nil {
		return nil, err
	}
	var deny []string
	for _, m := range list {
		if !slices.Contains(keep, m.Name) {
			deny = append(deny, ToolRule(m.Name))
		}
	}
	return deny, nil
}

func (t runTools) env(m map[string]string) {
	for _, k := range []string{"OCTOPUS_LOCAL_TOOLS", LocalMCPConfigEnv, "OCTOPUS_LOCAL_MCP_CONNECTORS",
		"OCTOPUS_LOCAL_MCP_DENY", "OCTOPUS_BYPASS_DISABLED", "OCTOPUS_ALLOWED_TOOLS"} {
		delete(m, k)
	}
	m["OCTOPUS_LOCAL_TOOLS"] = t.mode
	if t.mcpFile != "" {
		m[LocalMCPConfigEnv] = t.mcpFile
	}
	if t.connectors {
		m["OCTOPUS_LOCAL_MCP_CONNECTORS"] = "1"
		m["OCTOPUS_LOCAL_MCP_DENY"] = strings.Join(t.deny, "\n")
	}
	if t.bypassOff {
		m["OCTOPUS_BYPASS_DISABLED"] = "1"
		m["OCTOPUS_ALLOWED_TOOLS"] = strings.Join(t.allowed, "\n")
	}
}

// LocalMCPDef builds the definition `thulu runner mcp add` writes from its flags.
func LocalMCPDef(command string, args []string, url, kind string, headers, env map[string]string) (json.RawMessage, error) {
	def := map[string]any{}
	switch {
	case url != "":
		if kind == "" {
			kind = "http"
		}
		def["type"], def["url"] = kind, url
		if len(headers) > 0 {
			def["headers"] = headers
		}
	case command != "":
		def["type"], def["command"] = "stdio", command
		if len(args) > 0 {
			def["args"] = args
		}
	default:
		return nil, apperr.Invalid("hostrun.errors.mcp_needs_definition_flags")
	}
	if len(env) > 0 {
		def["env"] = env
	}
	b, err := json.Marshal(def)
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.write_mcp_file", "path", "-")
	}
	return b, nil
}
