//go:build unix

package hostrun

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		signalGroup(cmd.Process.Pid, syscall.SIGTERM)
	}
}

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		signalGroup(cmd.Process.Pid, syscall.SIGKILL)
	}
}

func signalGroup(pgid int, sig syscall.Signal) {
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		logx.Warn(err, "hostrun.log.signal_failed", "pid", pgid, "signal", sig.String())
	}
}

func killGroup(pgid int) {
	signalGroup(pgid, syscall.SIGKILL)
}

func commandLine(ctx context.Context, pid int) (string, error) {
	// G204: a fixed program and a number.
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=").Output() //nolint:gosec // see above
	if err != nil {
		return "", apperr.Wrap(err, "hostrun.errors.ps", "pid", pid)
	}
	return string(out), nil
}

func groupHasMembers(ctx context.Context, pgid int) bool {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pgid=").Output()
	if err != nil {
		logx.Warn(apperr.Wrap(err, "hostrun.errors.ps", "pid", pgid), "hostrun.log.group_check_failed", "pid", pgid)
		return false
	}
	want := strconv.Itoa(pgid)
	for _, f := range strings.Fields(string(out)) {
		if f == want {
			return true
		}
	}
	return false
}
