//go:build unix

package hostrun

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// The runner can stop process groups here; with a readable process table it takes runs.
const processControl = true

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

// ownGroup is the runner's own process group, which no kill may reach.
func ownGroup() int { return syscall.Getpgrp() }

func signalGroup(pgid int, sig syscall.Signal) {
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		logx.Warn(err, "hostrun.log.signal_failed", "pid", pgid, "signal", sig.String())
	}
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

// signalPid signals one process, or with a negative pid its group. A process already gone is not an
// error.
func signalPid(pid int, sig syscall.Signal) error {
	err := unix.Kill(pid, sig)
	if err == nil || errors.Is(err, unix.ESRCH) {
		return nil
	}
	return apperr.Wrap(err, "hostrun.errors.signal", "pid", pid, "signal", sig.String())
}

// How long ps and lsof get to describe the processes a run leaves.
const detailsTimeout = 5 * time.Second

// procDetails reads every process's memory and command line from one ps call, and the TCP ports
// they listen on from one lsof call. Either may come back empty: they describe the kill, they do not
// decide it.
func procDetails(ctx context.Context) (map[int]procDetail, map[int][]string) {
	ctx, cancel := context.WithTimeout(ctx, detailsTimeout)
	defer cancel()
	var details map[int]procDetail
	out, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,rss=,command=").Output()
	if err == nil {
		details = parsePS(out)
	} else {
		logx.Warn(apperr.Wrap(err, "hostrun.errors.ps_all"), "hostrun.log.details_failed")
	}
	// lsof exits 1 when nothing matches, and some machines do not have it.
	out, err = exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpn").Output()
	if err != nil && len(out) == 0 {
		return details, nil
	}
	return details, parseLsof(out)
}
