package hostrun

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

const pidFile = ".octopus-run.pid"

func sweepLeftovers(ctx context.Context, runsDir string) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := filepath.Join(runsDir, e.Name())
		b, err := os.ReadFile(filepath.Join(root, pidFile)) //nolint:gosec // G304: a file this runner wrote in its own work dir
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 && leftoverGroup(ctx, pid, root) {
				killGroup(pid)
			}
		}
		removeSession(root)
		if err := os.RemoveAll(root); err != nil {
			logx.Warn(err, "hostrun.log.remove_failed", "path", root)
			continue
		}
		log.Printf("[runner] %s", i18n.Log("hostrun.log.swept", "job", e.Name()))
	}
}

func removeSession(root string) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return
	}
	if dir := claudeProjectsDir(resolved); dir != "" {
		logx.Warn(os.RemoveAll(dir), "hostrun.log.remove_failed", "path", dir)
	}
}

// While the leader lives its command line must name root, so a pid reused by another process is
// left alone.
// Once the leader is gone, any process still in pgid is the run's: no new process gets a pid equal
// to a live group id.
func leftoverGroup(ctx context.Context, pgid int, root string) bool {
	if out, err := commandLine(ctx, pgid); err == nil {
		return strings.Contains(out, root)
	}
	return groupHasMembers(ctx, pgid)
}
