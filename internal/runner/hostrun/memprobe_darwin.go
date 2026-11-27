package hostrun

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// hostMemory reads the machine's available memory: kern.memorystatus_level is the percent of memory
// macOS counts as available (free, inactive, purgeable), of hw.memsize.
type hostMemory struct{}

func (hostMemory) AvailableMB() (int64, error) {
	level, err := unix.SysctlUint32("kern.memorystatus_level")
	if err != nil {
		return 0, apperr.Wrap(err, "hostrun.errors.memory_probe")
	}
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, apperr.Wrap(err, "hostrun.errors.memory_probe")
	}
	return int64(total/100*uint64(level)) >> 20, nil //nolint:gosec // G115: a percent of the machine's memory fits
}

// How often at most the sampler asks top for the processes' footprint; ps runs at every check.
var footprintEvery = 10 * time.Second

// darwinSampler takes, for each process, the larger of its resident size (ps RSS) and its physical
// footprint (top's MEM). RSS leaves out the pages macOS compressed or swapped out: a dev server
// that sits idle under memory pressure keeps a small RSS and gigabytes in the compressor, which is
// how the ng serve of the incident grew to 7.5 GB.
//
// Checked on this kind of machine before relying on it (macOS 27, 16 GB, 2026-11-25, no swap in
// use): Slack's renderer had RSS 195 MB, top MEM 371 MB, CMPRS 249 MB, and `footprint` said 371 MB;
// Spotify's renderer had RSS 132 MB, MEM 260 MB, CMPRS 178 MB. With nothing swapped, the part of MEM
// above RSS can only be compressed memory, so top's MEM counts it. Swapped pages could not be checked
// (the swap was empty); the footprint counts them by Apple's definition. RSS can still be the larger
// one, because it counts clean and shared pages the footprint leaves out (a VS Code renderer: RSS 607
// MB, MEM 378 MB), hence the max. top takes longer than ps, so it runs at most every footprintEvery.
type darwinSampler struct {
	lastTop time.Time
	top     map[procKey]int64
}

func newHostSampler() memSampler { return &darwinSampler{top: map[procKey]int64{}} }

func (s *darwinSampler) sample(ctx context.Context, procs []procInfo) map[procKey]int64 {
	if len(procs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, detailsTimeout)
	defer cancel()
	list := pidList(procs)
	// ps exits 1 when one of the pids is gone; it still prints the others. G204: a fixed program and
	// numbers.
	out, err := exec.CommandContext(ctx, "ps", "-o", "pid=,rss=", "-p", strings.Join(list, ",")).Output() //nolint:gosec // see above
	if err != nil && len(out) == 0 {
		logx.Warn(apperr.Wrap(err, "hostrun.errors.ps_all"), "hostrun.log.memory_sample_failed")
	}
	rss := parsePSRSS(out)
	if time.Since(s.lastTop) >= footprintEvery {
		s.lastTop = time.Now()
		args := []string{"-l", "1", "-s", "0", "-stats", "pid,mem"}
		for _, pid := range list {
			args = append(args, "-pid", pid)
		}
		top, err := exec.CommandContext(ctx, "top", args...).Output()
		if err != nil {
			logx.Warn(apperr.Wrap(err, "hostrun.errors.top"), "hostrun.log.memory_sample_failed")
		}
		fresh := parseTopMem(top)
		clear(s.top)
		for _, p := range procs {
			if kb, ok := fresh[p.PID]; ok {
				s.top[p.key()] = kb
			}
		}
	}
	held := make(map[procKey]int64, len(procs))
	for _, p := range procs {
		held[p.key()] = max(rss[p.PID], s.top[p.key()])
	}
	return held
}

func pidList(procs []procInfo) []string {
	out := make([]string, 0, len(procs))
	for _, p := range procs {
		out = append(out, strconv.Itoa(p.PID))
	}
	return out
}
