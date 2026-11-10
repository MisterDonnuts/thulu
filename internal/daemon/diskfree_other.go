//go:build !linux && !darwin

package daemon

import "github.com/MisterDonnuts/thulu/internal/apperr"

func diskFreePercent(string) (float64, error) {
	return 0, apperr.NotImplemented("daemon.errors.disk_free_unsupported")
}
