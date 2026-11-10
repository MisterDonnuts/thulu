//go:build linux || darwin

package daemon

import (
	"syscall"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Bavail, not Bfree: root-reserved blocks are unusable by the daemon's user. The ratio cancels
// block size, whose type differs on Linux and macOS.
func diskFreePercent(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, apperr.Wrap(err, "daemon.errors.statfs", "path", path)
	}
	if st.Blocks == 0 {
		return 100, nil
	}
	return float64(st.Bavail) / float64(st.Blocks) * 100, nil
}
