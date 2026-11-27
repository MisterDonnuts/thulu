package hostrun

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
)

type Config struct {
	DaemonURL   string            `json:"daemon_url"`
	RunnerID    string            `json:"runner_id"`
	WorkspaceID string            `json:"workspace_id"`
	Name        string            `json:"name"`
	Credential  string            `json:"credential"`
	Projects    map[string]string `json:"projects"`
	// Folders the person allows the runner to work in. Projects created in the dashboard must sit
	// inside one; empty means only the folders mapped in Projects.
	Roots          []string            `json:"roots,omitempty"`
	ProjectFiles   map[string][]string `json:"project_files,omitempty"`
	WorkDir        string              `json:"work_dir,omitempty"`
	LocalMCPConfig string              `json:"local_mcp_config,omitempty"`
	// How much of the machine's own tooling an agent gets: LocalToolsThulu (the default),
	// LocalToolsSelected or LocalToolsAll. Decided on the machine only; the daemon shows it.
	LocalTools string `json:"local_tools,omitempty"`
	// What LocalToolsSelected adds: servers defined in LocalMCPConfig, and claude.ai connectors by
	// the name claude shows ("claude.ai Slack"). Empty means every server in LocalMCPConfig.
	LocalMCP []string `json:"local_mcp,omitempty"`
	// Permission rules a run gets when a Claude Code policy turns off bypass mode.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Written into the service definition by `thulu runner service install`, over what the file
	// already had: PATH and anything else the runner's service must keep.
	ServiceEnv   map[string]string `json:"service_env,omitempty"`
	UserSettings bool              `json:"user_settings,omitempty"`
	Bash         string            `json:"bash,omitempty"`
	Concurrency  int               `json:"concurrency,omitempty"`
	// Chat turns take slots of their own, so a turn never waits behind a long run.
	ChatConcurrency int    `json:"chat_concurrency,omitempty"`
	KeepRuns        bool   `json:"keep_runs,omitempty"`
	Entrypoint      string `json:"entrypoint,omitempty"`

	// Memory, in MB (memory.go). 0 takes the default; a negative value turns the check off.
	// The most a run (the agent CLI and everything it starts) may take; DefaultRunMemoryLimitMB.
	RunMemoryLimitMB int `json:"run_memory_limit_mb,omitempty"`
	// A cap per project on this machine, by project id, over run_memory_limit_mb and the daemon's.
	ProjectMemoryLimitMB map[string]int `json:"project_memory_limit_mb,omitempty"`
	// Under this much available memory the runner claims no new run; DefaultMinFreeMemoryMB.
	MinFreeMemoryMB int `json:"min_free_memory_mb,omitempty"`
	// Under this much available memory the runner stops its largest run; DefaultMachineFloorMB.
	MachineMemoryFloorMB int `json:"machine_memory_floor_mb,omitempty"`
	// A worktree's build cache (.angular/cache, .next/cache, node_modules/.cache) over this many MB
	// goes when a run ends; DefaultBuildCacheMaxMB. Negative keeps every cache.
	BuildCacheMaxMB int `json:"build_cache_max_mb,omitempty"`

	// Where it was read from, so a running runner picks up `thulu add` and `thulu runner allow`
	// without a restart. mu guards Roots and Projects, which Reload swaps while jobs read them.
	path string
	mu   sync.RWMutex
}

const DefaultChatConcurrency = 2

func DefaultConfigPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("OCTOPUS_RUNNER_CONFIG")); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", apperr.Wrap(err, "hostrun.errors.home")
	}
	return filepath.Join(home, ".octopus", "runner.json"), nil
}

func LoadConfig(path string) (*Config, error) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the runner's own config, named by its operator
	if errors.Is(err, fs.ErrNotExist) {
		return nil, apperr.NotFound("hostrun.errors.not_registered", "path", path)
	}
	if err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.read_config", "path", path)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, apperr.Wrap(err, "hostrun.errors.read_config", "path", path)
	}
	if c.Projects == nil {
		c.Projects = map[string]string{}
	}
	switch {
	case c.WorkDir == "":
		c.WorkDir = filepath.Join(filepath.Dir(path), "work")
	case !filepath.IsAbs(c.WorkDir):
		c.WorkDir = filepath.Join(filepath.Dir(path), c.WorkDir)
	}
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	if c.ChatConcurrency < 1 {
		c.ChatConcurrency = DefaultChatConcurrency
	}
	c.path = path
	return &c, nil
}

// Reload takes the allowed folders and the mapped projects from the file again; the rest of the
// config (credential, daemon, limits) stays as the runner started with it.
func (c *Config) Reload() error {
	if c.path == "" {
		return nil
	}
	fresh, err := LoadConfig(c.path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Roots, c.Projects = fresh.Roots, fresh.Projects
	return nil
}

// folders is a copy of the allowed folders and the mapped projects, safe to read while Reload runs.
func (c *Config) folders() (roots []string, projects map[string]string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.Roots), maps.Clone(c.Projects)
}

// 0600 and through a temp file: the file holds the runner's credential, and a crash must not leave
// half of it.
func (c *Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return apperr.Wrap(err, "hostrun.errors.write_config", "path", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return apperr.Wrap(err, "hostrun.errors.write_config", "path", path)
	}
	if err := fsx.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return apperr.Wrap(err, "hostrun.errors.write_config", "path", path)
	}
	return nil
}

func (c *Config) ProjectIDs() []string {
	_, projects := c.folders()
	out := make([]string, 0, len(projects))
	for id := range projects {
		out = append(out, id)
	}
	return out
}
