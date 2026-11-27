//go:build !unix

package hostrun

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
)

// The runner cannot stop a process group here, so Preflight refuses to take runs.
const processControl = false

func setProcessGroup(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) { kill(cmd) }

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func commandLine(context.Context, int) (string, error) {
	return "", errors.ErrUnsupported
}

func groupHasMembers(context.Context, int) bool { return false }

func signalPid(int, syscall.Signal) error { return errors.ErrUnsupported }

func procDetails(context.Context) (map[int]procDetail, map[int][]string) { return nil, nil }

// killRun cannot find a run's processes without a process table; Preflight keeps such a machine from
// taking runs.
func killRun(context.Context, *runTracker, io.Writer) []procInfo { return nil }

func ownGroup() int { return 0 }
