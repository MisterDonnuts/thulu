package api

import (
	"path/filepath"
	"strings"

	dtypes "github.com/docker/docker/api/types"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/watcher"
)

// Returns "" rather than a relative path: filepath.Join("", id) would resolve against the daemon's
// cwd.
// instanceID comes from container labels, so it must be a single path element.
// The janitor retention sweep and task teardown clean this same root.
func runOutputDir(runsRoot, instanceID string) string {
	if runsRoot == "" || !filepath.IsAbs(runsRoot) || !isRunInstanceName(instanceID) {
		return ""
	}
	return filepath.Join(filepath.Clean(runsRoot), instanceID)
}

func isRunInstanceName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.HasPrefix(name, ".") && !strings.ContainsAny(name, `/\`) &&
		filepath.Base(name) == name
}

func (s *Server) runOutputDirOf(instanceID string) string {
	return runOutputDir(s.runsDir, instanceID)
}

// Containers launched while the thulu.instance label was missing carry only the env and the name.
func containerInstanceID(info dtypes.ContainerJSON) string {
	if info.Config != nil {
		if inst := info.Config.Labels[agent.LabelInstance]; inst != "" {
			return inst
		}
		if inst := watcher.InstanceIDFromEnv(info.Config.Env); inst != "" {
			return inst
		}
	}
	if info.ContainerJSONBase != nil {
		return strings.TrimPrefix(info.Name, "/")
	}
	return ""
}
