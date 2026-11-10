package task

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/safepath"
)

// Layout: <data>/sessions/<rootTaskID>/<taskID>/<agent>/<sid>.tar.gz. The task level keeps a
// subtask and its parent apart; PruneSessions walks exactly these levels.

const (
	sessionsDirName    = "sessions"
	sessionSnapshotExt = ".tar.gz"
	// Partial uploads are never served or counted as the snapshot, so a half-written upload cannot
	// replace a good one.
	uploadPrefix = ".upload-"
)

func SessionsRootBeside(runsDir string) string {
	if runsDir == "" || !filepath.IsAbs(runsDir) {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Clean(runsDir)), sessionsDirName)
}

// Path-element charset; a leading dot is refused so no element is ".", ".." or hidden.
var sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidSessionName(s string) bool {
	return sessionNameRe.MatchString(s) && !strings.Contains(s, "..")
}

type SessionRef struct {
	RootTaskID string
	TaskID     string
	Agent      string
	SessionID  string
}

func (r SessionRef) valid() bool {
	return ValidSessionName(r.RootTaskID) && ValidSessionName(r.TaskID) &&
		ValidSessionName(r.Agent) && ValidSessionName(r.SessionID)
}

func (r SessionRef) dir() string { return filepath.Join(r.RootTaskID, r.TaskID, r.Agent) }

func (r SessionRef) file() string {
	return filepath.Join(r.dir(), r.SessionID+sessionSnapshotExt)
}

func SaveSessionSnapshot(root string, ref SessionRef, src io.Reader, maxBytes int64) (int64, error) {
	if root == "" {
		return 0, apperr.Unavailable("task.sessions.errors.no_store")
	}
	if !ref.valid() {
		return 0, apperr.Invalid("task.sessions.errors.bad_ref")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return 0, apperr.Wrap(err, "task.sessions.errors.store_failed")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return 0, apperr.Wrap(err, "task.sessions.errors.store_failed")
	}
	defer r.Close()
	if err = r.MkdirAll(ref.dir(), 0o700); err != nil {
		return 0, apperr.Wrap(err, "task.sessions.errors.store_failed")
	}

	tmp := filepath.Join(ref.dir(), uploadPrefix+mustGenerateID())
	n, err := writeLimited(r, tmp, src, maxBytes)
	if err != nil {
		logx.Warn(ignoreMissing(r.Remove(tmp)), "task.sessions.log.remove_partial", "path", tmp)
		return n, err
	}
	if err := r.Rename(tmp, ref.file()); err != nil {
		logx.Warn(ignoreMissing(r.Remove(tmp)), "task.sessions.log.remove_partial", "path", tmp)
		return n, apperr.Wrap(err, "task.sessions.errors.store_failed")
	}
	removeOlderSnapshots(r, ref)
	return n, nil
}

func writeLimited(r *os.Root, name string, src io.Reader, maxBytes int64) (int64, error) {
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, apperr.Wrap(err, "task.sessions.errors.store_failed")
	}
	n, copyErr := io.Copy(f, io.LimitReader(src, maxBytes+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return n, apperr.Wrap(copyErr, "task.sessions.errors.store_failed")
	case closeErr != nil:
		return n, apperr.Wrap(closeErr, "task.sessions.errors.store_failed")
	case n > maxBytes:
		return n, apperr.TooLarge("task.sessions.errors.snapshot_too_large", "max", maxBytes)
	case n == 0:
		return 0, apperr.Invalid("task.sessions.errors.snapshot_empty")
	}
	return n, nil
}

// In-flight uploads are left alone: a concurrent upload of the same pair replaces this one when it
// lands.
func removeOlderSnapshots(r *os.Root, ref SessionRef) {
	entries, err := readDirIn(r, ref.dir())
	if err != nil {
		logx.Warn(err, "task.sessions.log.list_failed", "dir", ref.dir())
		return
	}
	keep := ref.SessionID + sessionSnapshotExt
	for _, e := range entries {
		if e.Name() == keep || strings.HasPrefix(e.Name(), uploadPrefix) {
			continue
		}
		logx.Warn(r.RemoveAll(filepath.Join(ref.dir(), e.Name())),
			"task.sessions.log.remove_old", "dir", ref.dir(), "name", e.Name())
	}
}

func OpenSessionSnapshot(root string, ref SessionRef) (*os.File, error) {
	if root == "" || !ref.valid() {
		return nil, apperr.NotFound("task.sessions.errors.snapshot_not_found")
	}
	f, err := safepath.Open(root, ref.file())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, apperr.NotFound("task.sessions.errors.snapshot_not_found")
	}
	if err != nil {
		return nil, apperr.Wrap(err, "task.sessions.errors.read_failed")
	}
	return f, nil
}

func RemoveTaskSessions(root, rootTaskID, taskID string) error {
	if root == "" || !ValidSessionName(rootTaskID) || !ValidSessionName(taskID) {
		return nil
	}
	name := rootTaskID
	if taskID != rootTaskID {
		name = filepath.Join(rootTaskID, taskID)
	}
	err := safepath.RemoveAll(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func PruneSessions(root string, cutoff time.Time) (int64, error) {
	if root == "" {
		return 0, nil
	}
	r, err := os.OpenRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer r.Close()
	var removed int64
	for _, group := range sessionSubdirs(r, ".") {
		for _, tk := range sessionSubdirs(r, group) {
			for _, agent := range sessionSubdirs(r, tk) {
				removed += pruneSnapshotFiles(r, agent, cutoff)
				removeIfEmpty(r, agent)
			}
			removeIfEmpty(r, tk)
		}
		removeIfEmpty(r, group)
	}
	return removed, nil
}

func sessionSubdirs(r *os.Root, dir string) []string {
	entries, err := readDirIn(r, dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

func pruneSnapshotFiles(r *os.Root, dir string, cutoff time.Time) int64 {
	entries, err := readDirIn(r, dir)
	if err != nil {
		return 0
	}
	var n int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := r.Remove(filepath.Join(dir, e.Name())); err != nil {
			logx.Warn(err, "task.sessions.log.remove_old", "dir", dir, "name", e.Name())
			continue
		}
		if !strings.HasPrefix(e.Name(), uploadPrefix) {
			n++
		}
	}
	return n
}

func removeIfEmpty(r *os.Root, dir string) {
	if entries, err := readDirIn(r, dir); err == nil && len(entries) == 0 {
		logx.Warn(ignoreMissing(r.Remove(dir)), "task.sessions.log.remove_empty", "dir", dir)
	}
}

func readDirIn(r *os.Root, dir string) ([]fs.DirEntry, error) {
	f, err := r.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func ignoreMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (d *Dispatcher) WithSessionsDir(dir string) *Dispatcher {
	d.sessionsDir = dir
	return d
}
