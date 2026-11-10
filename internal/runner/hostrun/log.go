package hostrun

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	logMaxSize = 10 << 20
	logKeep    = 3
)

type Log struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Crash output (debug.SetCrashOutput) goes to this log: launchd discards stderr, so a panic would
// vanish.
func OpenLog(path string) (*Log, error) {
	f, size, err := openLogFile(path)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, max: logMaxSize, f: f, size: size}, nil
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var rerr error
	if l.size > 0 && l.size+int64(len(p)) > l.max {
		// A failed rotation still writes the line to the current file: an oversized log beats
		// losing what went wrong.
		rerr = l.rotate()
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, errors.Join(rerr, err)
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

func (l *Log) rotate() error {
	for i := logKeep - 1; i > 0; i-- {
		err := os.Rename(rotatedLog(l.path, i-1), rotatedLog(l.path, i))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return apperr.Wrap(err, "hostrun.errors.rotate_log", "path", l.path)
		}
	}
	f, size, err := openLogFile(l.path)
	if err != nil {
		return err
	}
	old := l.f
	l.f, l.size = f, size
	return old.Close()
}

// Force 0600 even when an older runner or the service manager created the file readable by others.
func openLogFile(path string) (*os.File, int64, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, 0, apperr.Wrap(err, "hostrun.errors.open_log", "path", path)
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, apperr.Wrap(err, "hostrun.errors.open_log", "path", path)
	}
	info, err := f.Stat()
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = debug.SetCrashOutput(f, debug.CrashOptions{})
	}
	if err != nil {
		return nil, 0, apperr.Wrap(errors.Join(err, f.Close()), "hostrun.errors.open_log", "path", path)
	}
	return f, info.Size(), nil
}

func rotatedLog(path string, i int) string {
	if i == 0 {
		return path
	}
	return path + "." + strconv.Itoa(i)
}

// Uninstall deletes what this returns: match only name.<digits>, never runner.log.old or
// runner.log.1.gz.
func LogFiles(path string) []string {
	var out []string
	if _, err := os.Lstat(path); err == nil {
		out = append(out, path)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return out
	}
	for _, e := range entries {
		n, ok := strings.CutPrefix(e.Name(), filepath.Base(path)+".")
		if ok && n != "" && strings.Trim(n, "0123456789") == "" && !e.IsDir() {
			out = append(out, filepath.Join(filepath.Dir(path), e.Name()))
		}
	}
	return out
}
