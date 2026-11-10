//go:build !linux && !darwin

package infra

import "github.com/MisterDonnuts/thulu/internal/apperr"

func statDisk(path string) (Disk, uint64, error) {
	return Disk{}, 0, apperr.NotImplemented("infra.errors.statfs_unsupported", "path", path)
}
