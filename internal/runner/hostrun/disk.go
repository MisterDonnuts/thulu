package hostrun

import (
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"time"
)

// diskSize adds up the size of the files under path without following symlinks (a copy of a repo
// links node_modules/.bin into itself). A tree too large to walk within limit gives what was counted
// so far and done false.
func diskSize(path string, limit time.Duration) (size int64, done bool) {
	deadline := time.Now().Add(limit)
	errStop := fs.SkipAll
	n := 0
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry counts as empty; the walk goes on
		}
		n++
		if n%1024 == 0 && time.Now().After(deadline) {
			return errStop
		}
		if d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size, err == nil && !time.Now().After(deadline)
}

// bytesText writes a size the way the runner's messages show it: "0.7 MB", "312 MB", "3.7 GB". A
// size cut short by its walk reads "at least".
func bytesText(b int64, done bool) string {
	mb := float64(b) / (1 << 20)
	var s string
	switch {
	case mb < 10:
		s = strconv.FormatFloat(math.Round(mb*10)/10, 'f', -1, 64) + " MB" //i18n:ignore a unit, the same in every language
	case mb < 1024:
		s = strconv.FormatFloat(math.Round(mb), 'f', -1, 64) + " MB" //i18n:ignore a unit, the same in every language
	default:
		s = strconv.FormatFloat(math.Round(mb/1024*10)/10, 'f', -1, 64) + " GB" //i18n:ignore a unit, the same in every language
	}
	if !done {
		s = ">= " + s //i18n:ignore a symbol, the same in every language
	}
	return s
}

// How long the runner walks one tree to measure it for a log line.
var sizeWalkLimit = 5 * time.Second
