//go:build !unix

package hostrun

import (
	"context"
	"errors"
	"os/exec"
)

func setProcessGroup(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) { kill(cmd) }

func kill(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killGroup(int) {}

func commandLine(context.Context, int) (string, error) {
	return "", errors.ErrUnsupported
}

func groupHasMembers(context.Context, int) bool { return false }
