//go:build unix

package execagent

import (
	"errors"
	"os/exec"
	"syscall"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Kill the whole group on timeout or cancel, else a timed-out bash -lc's children survive
// reparented to PID 1.
// A normal return keeps background children; setsid children (init_run's script) are never touched.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return apperr.Wrap(err, "execagent.errors.kill_group")
		}
		return nil
	}
}
