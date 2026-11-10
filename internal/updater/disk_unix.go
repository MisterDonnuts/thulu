//go:build unix

package updater

import "syscall"

func freeBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	// Bsize is int64 on Linux and uint32 on macOS; a non-positive block size reads as unknown.
	bsize := int64(st.Bsize)
	if bsize <= 0 {
		return -1
	}
	const limit = 1 << 62
	size := uint64(bsize)
	if st.Bavail > limit/size {
		return limit
	}
	free := st.Bavail * size
	if free > limit {
		return limit
	}
	return int64(free)
}
