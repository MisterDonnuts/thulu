//go:build linux || darwin

package infra

import (
	"os"
	"syscall"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Free is Bavail and Used is Blocks−Bfree, so the percentage matches df despite the root reserve.
func statDisk(path string) (Disk, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Disk{}, 0, apperr.Wrap(err, "infra.errors.statfs", "path", path)
	}
	bs := blockSize(st.Bsize)
	d := Disk{Path: path, Size: st.Blocks * bs, Free: st.Bavail * bs}
	if st.Blocks >= st.Bfree {
		d.Used = (st.Blocks - st.Bfree) * bs
	}
	if denom := d.Used + d.Free; denom > 0 {
		d.Percent = clampPct(float64(d.Used) / float64(denom) * 100)
	}
	d.InodesTotal = st.Files
	if st.Files >= st.Ffree {
		d.InodesUsed = st.Files - st.Ffree
	}
	if d.InodesTotal > 0 {
		d.InodesPercent = clampPct(float64(d.InodesUsed) / float64(d.InodesTotal) * 100)
	}
	return d, deviceOf(path), nil
}

func deviceOf(path string) uint64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return devID(st.Dev)
	}
	return 0
}

func blockSize[T int64 | uint32](v T) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

func devID[T uint64 | int32](v T) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
