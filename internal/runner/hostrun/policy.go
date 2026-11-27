package hostrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/safepath"
)

// Policy is what Claude Code's settings outside Thulu's reach decide for a run on this machine.
// Server-managed settings (claude.ai admin console) reach claude over the network and are not here.
type Policy struct {
	// permissions.disableBypassPermissionsMode is "disable": claude rejects
	// --dangerously-skip-permissions.
	BypassDisabled bool
	BypassSource   string
	// deniedMcpServers and allowedMcpServers entries that name a server.
	DeniedMCP  []string
	AllowedMCP []string
	// An allowedMcpServers list exists, even an empty one: servers outside it do not load.
	AllowList bool
	Sources   []string
	// The allowlist also matches by URL or command, which the runner cannot judge by name.
	allowOther bool
}

// The system directory Claude Code reads managed-settings.json and managed-settings.d/ from, per
// https://code.claude.com/docs/en/managed-settings.
var managedSettingsDir = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode" //i18n:ignore a system path
	case "linux":
		return "/etc/claude-code"
	}
	return ""
}

// The com.anthropic.claudecode managed preferences an MDM profile installs on a Mac.
var managedPlists = func() []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	const dir, file = "/Library/Managed Preferences", "com.anthropic.claudecode.plist" //i18n:ignore system paths
	out := []string{filepath.Join(dir, file)}
	if u := os.Getenv("USER"); u != "" {
		out = append(out, filepath.Join(dir, u, file))
	}
	return out
}

// readPlist turns a property list, binary or XML, into JSON; plutil ships with macOS. The plist
// goes in on stdin so the command line stays fixed.
var readPlist = func(ctx context.Context, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(body)
	return cmd.Output()
}

// userSettingsPath is ~/.claude/settings.json, read only when the runner hands runs the user's
// settings (user_settings).
var userSettingsPath = func() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

type settingsDoc struct {
	Permissions struct {
		DisableBypass string `json:"disableBypassPermissionsMode"`
	} `json:"permissions"`
	Denied  []mcpRule  `json:"deniedMcpServers"`
	Allowed *[]mcpRule `json:"allowedMcpServers"`
}

type mcpRule struct {
	ServerName string `json:"serverName"`
}

type policySource struct {
	path string
	body []byte
	// The file exists and could not be read or parsed.
	broken bool
}

// ReadPolicy reads the managed settings files and profiles, and the user's settings when runs get
// them. A source that exists but cannot be read counts as turning bypass off, as claude counts it.
func ReadPolicy(ctx context.Context, userSettings bool) Policy {
	var p Policy
	for _, src := range policySources(ctx, userSettings) {
		var doc settingsDoc
		if !src.broken && json.Unmarshal(src.body, &doc) != nil {
			src.broken = true
		}
		p.Sources = append(p.Sources, src.path)
		if (src.broken || doc.Permissions.DisableBypass == "disable") && !p.BypassDisabled {
			p.BypassDisabled, p.BypassSource = true, src.path
		}
		if !src.broken {
			p.add(doc)
		}
	}
	return p
}

// policySources reads every settings source claude would, in its order; missing ones are left out.
func policySources(ctx context.Context, userSettings bool) []policySource {
	var paths []string
	if dir := managedSettingsDir(); dir != "" {
		paths = append(paths, filepath.Join(dir, "managed-settings.json"))
		if dropins, err := filepath.Glob(filepath.Join(dir, "managed-settings.d", "*.json")); err == nil {
			sort.Strings(dropins)
			paths = append(paths, dropins...)
		}
	}
	var out []policySource
	for _, path := range paths {
		if src, ok := readSource(path); ok {
			out = append(out, src)
		}
	}
	for _, path := range managedPlists() {
		src, ok := readSource(path)
		if !ok {
			continue
		}
		if !src.broken {
			body, err := readPlist(ctx, src.body)
			src.body, src.broken = body, err != nil
		}
		out = append(out, src)
	}
	if userSettings {
		if path := userSettingsPath(); path != "" {
			if src, ok := readSource(path); ok {
				out = append(out, src)
			}
		}
	}
	return out
}

func readSource(path string) (policySource, bool) {
	b, err := readResolved(path)
	if errors.Is(err, fs.ErrNotExist) {
		return policySource{}, false
	}
	return policySource{path: path, body: b, broken: err != nil}, true
}

func (p *Policy) add(doc settingsDoc) {
	for _, r := range doc.Denied {
		if r.ServerName != "" {
			p.DeniedMCP = append(p.DeniedMCP, r.ServerName)
		}
	}
	if doc.Allowed == nil {
		return
	}
	p.AllowList = true
	for _, r := range *doc.Allowed {
		if r.ServerName == "" {
			p.allowOther = true
		} else {
			p.AllowedMCP = append(p.AllowedMCP, r.ServerName)
		}
	}
}

// readResolved reads a file named by a full path, following a symlink first (a dotfiles manager
// links ~/.claude.json), then opening it inside its own directory.
func readResolved(path string) ([]byte, error) {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	return safepath.ReadFile(filepath.Dir(path), filepath.Base(path))
}

// Blocks reports whether the policy keeps server from loading, by name. URL and command entries
// are left to claude: the runner does not resolve them.
func (p Policy) Blocks(server string) bool {
	for _, d := range p.DeniedMCP {
		if d == server {
			return true
		}
	}
	if !p.AllowList || p.allowOther {
		return false
	}
	for _, a := range p.AllowedMCP {
		if a == server {
			return false
		}
	}
	return true
}
