//go:build !darwin

package hostrun

import "errors"

// Linux shows every same-user process's environment, so the run's marker finds what leaves the tree
// before a poll; a systemd scope holds the rest.
func newForkWatch() (forkWatch, error) { return nil, errors.ErrUnsupported }
