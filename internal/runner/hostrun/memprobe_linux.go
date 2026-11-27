package hostrun

import (
	"context"
	"io/fs"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// hostMemory reads MemAvailable from /proc/meminfo: the kernel's estimate of what it can give without
// swapping.
type hostMemory struct{}

func (hostMemory) AvailableMB() (int64, error) {
	b, err := fs.ReadFile(procFS, "meminfo")
	if err != nil {
		return 0, apperr.Wrap(err, "hostrun.errors.memory_probe")
	}
	kb, ok := parseMeminfo(b, "MemAvailable")
	if !ok {
		return 0, apperr.Invalid("hostrun.errors.memory_probe_format")
	}
	return kb / 1024, nil
}

// linuxSampler takes VmRSS plus VmSwap from /proc/<pid>/status: what the process holds in memory and
// what the kernel moved to swap. It is the watcher when the run has no systemd scope.
type linuxSampler struct{}

func newHostSampler() memSampler { return linuxSampler{} }

func (linuxSampler) sample(_ context.Context, procs []procInfo) map[procKey]int64 {
	held := make(map[procKey]int64, len(procs))
	for _, p := range procs {
		b, err := fs.ReadFile(procFS, strconv.Itoa(p.PID)+"/status")
		if err != nil {
			continue
		}
		rss, _ := parseMeminfo(b, "VmRSS")
		swap, _ := parseMeminfo(b, "VmSwap")
		held[p.key()] = rss + swap
	}
	return held
}
