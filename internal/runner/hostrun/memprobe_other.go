//go:build !darwin && !linux

package hostrun

import (
	"context"
	"errors"
)

// Preflight refuses runs on these systems; the probe only keeps the package building.
type hostMemory struct{}

func (hostMemory) AvailableMB() (int64, error) { return 0, errors.ErrUnsupported }

type noSampler struct{}

func newHostSampler() memSampler { return noSampler{} }

func (noSampler) sample(context.Context, []procInfo) map[procKey]int64 { return nil }
