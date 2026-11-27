package hostrun

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/engine"
)

// What `thulu runner doctor` says about one MCP server, as a run of this machine would see it.
const (
	MCPLoaded       = "loaded"
	MCPNotSelected  = "not_selected"
	MCPStrict       = "strict"
	MCPPolicy       = "policy"
	MCPNeedsAuth    = "needs_auth"
	MCPFailed       = "failed"
	MCPOff          = "off"
	MCPUnchecked    = "unchecked"
	MCPMissing      = "missing"
	MCPNotConnected = "not_connected"
)

type DoctorMCP struct {
	Name   string
	Origin string
	State  string
}

// Where the mode came from: runner.json's local_tools, a local_mcp_config from before it, or the
// default.
const (
	ModeFromConfig  = "config"
	ModeFromLegacy  = "legacy"
	ModeFromDefault = "default"
)

type DoctorReport struct {
	Config      string
	Mode        string
	ModeFrom    string
	EnvOverride string
	MCPFile     string
	MCPs        []DoctorMCP
	// Why the machine's MCPs could not be listed (claude missing or failing); the rows then come from
	// runner.json alone.
	ListErr      string
	Policy       Policy
	AllowedTools []string
	UserSettings bool
	Memory       MemoryLimits
	// The work dir and how much it holds ("" when it does not exist yet), and the build cache limit
	// in MB, zero or less for none.
	WorkDir         string
	WorkDirSize     string
	BuildCacheMaxMB int64
}

// How long doctor walks the work dir to measure it.
var doctorSizeLimit = 20 * time.Second

// Doctor reads the runner's config and asks claude, as a job would, which MCPs this machine has.
func Doctor(ctx context.Context, configPath string) (DoctorReport, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return DoctorReport{}, err
	}
	rep := DoctorReport{Config: cfg.path, Mode: cfg.Mode(), MCPFile: cfg.LocalMCPFile(),
		EnvOverride: strings.TrimSpace(os.Getenv(LocalMCPConfigEnv)), AllowedTools: cfg.AllowedTools,
		UserSettings: cfg.UserSettings, Memory: cfg.MemoryLimits(), WorkDir: cfg.WorkDir,
		BuildCacheMaxMB: cfg.buildCacheMaxMB()}
	rep.WorkDirSize = workDirSize(cfg.WorkDir)
	switch {
	case ValidLocalTools(cfg.LocalTools):
		rep.ModeFrom = ModeFromConfig
	case rep.MCPFile != "":
		rep.ModeFrom = ModeFromLegacy
	default:
		rep.ModeFrom = ModeFromDefault
	}
	rep.Policy = ReadPolicy(ctx, cfg.UserSettings)
	sel, err := cfg.Selection()
	if err != nil {
		return rep, err
	}
	dir := cfg.WorkDir
	if _, serr := os.Stat(dir); serr != nil {
		dir = filepath.Dir(cfg.path)
	}
	listed, lerr := listMCPs(ctx, envList(loginEnv(engine.DefaultName)), dir)
	if lerr != nil {
		rep.ListErr = apperr.Text(lerr)
	}
	seen := map[string]bool{}
	for _, m := range listed {
		seen[m.Name] = true
		rep.MCPs = append(rep.MCPs, DoctorMCP{Name: m.Name, Origin: m.Target, State: rep.mcpState(m, sel)})
	}
	for name := range sel.Servers {
		if !seen[name] {
			state := MCPUnchecked
			if rep.Mode != LocalToolsSelected {
				state = MCPNotSelected
			} else if rep.Policy.Blocks(name) {
				state = MCPPolicy
			}
			rep.MCPs = append(rep.MCPs, DoctorMCP{Name: name, Origin: rep.MCPFile, State: state})
		}
	}
	for _, name := range sel.Connectors {
		if !seen[name] && lerr == nil {
			rep.MCPs = append(rep.MCPs, DoctorMCP{Name: name, State: MCPNotConnected})
		}
	}
	for _, name := range sel.Missing {
		if !seen[name] {
			rep.MCPs = append(rep.MCPs, DoctorMCP{Name: name, Origin: rep.MCPFile, State: MCPMissing})
		}
	}
	slices.SortFunc(rep.MCPs, func(a, b DoctorMCP) int { return strings.Compare(a.Name, b.Name) })
	return rep, nil
}

// workDirSize measures the work dir; empty when it does not exist yet.
func workDirSize(dir string) string {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return bytesText(diskSize(dir, doctorSizeLimit))
}

// mcpState decides in the order claude does: the mode first (a server left out never connects), then
// the policy, then the server's own health.
func (rep DoctorReport) mcpState(m ListedMCP, sel Selection) string {
	switch rep.Mode {
	case LocalToolsThulu:
		return MCPStrict
	case LocalToolsSelected:
		// A name the file lacks stays out even when claude knows it from ~/.claude.json: the run
		// gets the file, and with connectors the deny rules cover it.
		if slices.Contains(sel.Missing, m.Name) {
			return MCPMissing
		}
		if !slices.Contains(sel.Names(), m.Name) {
			return MCPNotSelected
		}
	}
	if rep.Policy.Blocks(m.Name) || m.Status == mcpBlocked {
		return MCPPolicy
	}
	switch m.Status {
	case mcpNeedsAuth:
		return MCPNeedsAuth
	case mcpFailed:
		return MCPFailed
	case mcpDisabled, mcpPending:
		return MCPOff
	}
	return MCPLoaded
}
