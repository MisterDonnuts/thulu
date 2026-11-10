package hostrun

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/fsx"
)

type Config struct {
	DaemonURL      string              `json:"daemon_url"`
	RunnerID       string              `json:"runner_id"`
	WorkspaceID    string              `json:"workspace_id"`
	Name           string              `json:"name"`
	Credential     string              `json:"credential"`
	Projects       map[string]string   `json:"projects"`
	ProjectFiles   map[string][]string `json:"project_files,omitempty"`
	WorkDir        string              `json:"work_dir,omitempty"`
	LocalMCPConfig string              `json:"local_mcp_config,omitempty"`
	UserSettings   bool                `json:"user_settings,omitempty"`
	Bash           string              `json:"bash,omitempty"`
	Concurrency    int                 `json:"concurrency,omitempty"`
	KeepRuns       bool                `json:"keep_runs,omitempty"`
	Entrypoint     string              `json:"entrypoint,omitempty"`
}

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
	return &c, nil
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
	out := make([]string, 0, len(c.Projects))
	for id := range c.Projects {
		out = append(out, id)
	}
	return out
}
