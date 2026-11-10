package updater

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Same rule as deploy/backup.sh (keep in sync): keep the newest N regardless of age, so a rarely
// updated install never ends with no snapshot.
// Covers both octopus_<YYYYMMDD_HHMMSS>.tar.gz (backup.sh) and .sql (in-container pg_dump).
const (
	EnvBackupKeep     = "OCTOPUS_BACKUP_KEEP"
	DefaultBackupKeep = 5
)

type BackupFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Zero is refused on purpose: skipping snapshots is -no-backup's job.
func BackupKeep(getenv func(string) string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv(EnvBackupKeep)))
	if err != nil || n < 1 {
		return DefaultBackupKeep
	}
	return n
}

func BackupsBeyondRetention(dir string, keep int, pinned ...string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, apperr.Wrap(err, "updater.errors.read_path", "path", dir)
	}
	var all []BackupFile
	for _, e := range entries {
		if e.IsDir() || !isSnapshotName(e.Name()) {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		all = append(all, BackupFile{Path: filepath.Join(dir, e.Name()), Size: info.Size(), ModTime: info.ModTime()})
	}
	// Sort by the timestamp in the name, not mtime: mtime does not survive a copy to another disk.
	sort.Slice(all, func(i, j int) bool { return filepath.Base(all[i].Path) > filepath.Base(all[j].Path) })
	pinned = append(pinned, keptSnapshotPaths(dir)...)
	if len(all) <= keep {
		return nil, nil
	}
	var out []BackupFile
	for _, b := range all[keep:] {
		if spared(b.Path, pinned) {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

func PruneBackups(dir string, keep int, pinned ...string) ([]BackupFile, error) {
	old, err := BackupsBeyondRetention(dir, keep, pinned...)
	if err != nil {
		return nil, err
	}
	removed := old[:0]
	for _, b := range old {
		if rmErr := os.Remove(b.Path); rmErr != nil && !os.IsNotExist(rmErr) {
			return removed, apperr.Wrap(rmErr, "updater.errors.remove_path", "path", b.Path)
		}
		removed = append(removed, b)
	}
	return removed, nil
}

func isSnapshotName(name string) bool {
	return strings.HasPrefix(name, "octopus_") &&
		(strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".sql"))
}

func spared(path string, pinned []string) bool {
	for _, p := range pinned {
		if p != "" && samePath(path, p) {
			return true
		}
	}
	return false
}

// Applied inside retention so every caller, `thulu gc` included, spares them: a kept version
// without its snapshot cannot be returned to.
func keptSnapshotPaths(dir string) []string {
	state, err := LoadState(filepath.Dir(dir))
	if err != nil || state == nil {
		return nil
	}
	return keptSnapshots(filepath.Dir(dir), state.Kept)
}

// The state file holds whatever path the backup step printed, relative or absolute.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}
