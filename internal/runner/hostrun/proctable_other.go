//go:build !darwin && !linux

package hostrun

import (
	"context"
	"errors"
	"time"
)

// Without a process table the runner cannot find what a run started outside its process group, so
// Preflight refuses to take runs.
const procTableSupported = false

func listProcs() ([]procInfo, error) { return nil, errors.ErrUnsupported }

func lookupProc(int) (procInfo, bool) { return procInfo{}, false }

func procEnv(int) ([]string, error) { return nil, errors.ErrUnsupported }

func procCwds(context.Context, []int) map[int]string { return nil }

func machineBootTime() (time.Time, bool) { return time.Time{}, false }

func startUnits(time.Time) int64 { return 0 }
