package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Two concurrent `compose up`s against one database can run a migration twice.
const LockFile = "deploy/.octopus-update.lock"

type lockInfo struct {
	PID      int       `json:"pid"`
	Host     string    `json:"host"`
	Started  time.Time `json:"started"`
	Operator string    `json:"operator"`
}

func acquireLock(root, operator string, now time.Time) (release func(), err error) {
	path := filepath.Join(root, LockFile)
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	body, err := json.Marshal(lockInfo{PID: os.Getpid(), Host: host, Started: now, Operator: operator})
	if err != nil {
		return nil, apperr.Wrap(err, "updater.errors.encode", "what", LockFile)
	}
	unlock := func() { warnRemove(path) }
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // fixed name under the install root
		if err == nil {
			_, werr := f.Write(body)
			if err = errors.Join(werr, f.Close()); err != nil {
				unlock()
				return nil, apperr.Wrap(err, "updater.errors.write_path", "path", LockFile)
			}
			return unlock, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, apperr.Wrap(err, "updater.errors.take_lock", "file", LockFile)
		}
		held := readLock(path)
		if host != "" && held.Host == host && held.PID > 0 && !processAlive(held.PID) && removeStale(path, held) {
			continue
		}
		return nil, apperr.Conflict("updater.errors.lock_held", "operator", held.Operator, "pid", held.PID,
			"host", held.Host, "since", held.Started.Format(time.RFC3339), "file", LockFile)
	}
	return nil, apperr.Conflict("updater.errors.lock_not_taken", "file", LockFile)
}

// The guard file keeps two runs that both saw the lock stale from both removing it (the second
// would delete the lock the first just took).
// Under the guard the lock is re-read and removed only if it is still the stale one.
func removeStale(path string, stale lockInfo) bool {
	guard := path + ".takeover"
	g, err := os.OpenFile(guard, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // fixed name under the install root
	if err != nil {
		return false
	}
	defer warnRemove(guard)
	if err := g.Close(); err != nil {
		return false
	}
	if now := readLock(path); now.PID != stale.PID || now.Host != stale.Host || !now.Started.Equal(stale.Started) {
		return false
	}
	warnRemove(path)
	return true
}

func readLock(path string) lockInfo {
	var held lockInfo
	raw, err := os.ReadFile(path) //nolint:gosec // fixed name under the install root
	if err != nil {
		return held
	}
	if err := json.Unmarshal(raw, &held); err != nil {
		return lockInfo{}
	}
	return held
}

func warnRemove(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(os.Stderr, i18n.Text("updater.cli.warn_remove_failed", "path", path, "err", apperr.Text(err)))
	}
}

// When liveness is unknown, report alive: a stale lock costs one rm, a stolen one a broken update.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // On Windows FindProcess fails for a dead pid, so success means alive.
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
