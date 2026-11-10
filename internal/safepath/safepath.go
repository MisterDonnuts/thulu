// Package safepath provides traversal-safe filesystem access for paths built
// from untrusted or externally-derived input (request path segments, names and
// paths read back from the database or container metadata).
//
// It is built on os.Root (Go 1.24+), so resolution is confined to a base
// directory by the kernel: "..", absolute paths and symlinks that point outside
// the base are all refused. This is strictly stronger than a filepath.Clean +
// prefix check, which validates the textual path but cannot see through a
// symlink (a TOCTOU escape). Because gosec recognizes os.Root as a safe API,
// call sites that go through this package do not need a //nolint:gosec.
//
// Every function takes a base directory and a name relative to it. The name may
// be nested ("stages/plan.md"); a leading slash is stripped so a request path
// like "/main.md" is treated as relative to the base rather than rejected.
//
// For absolute paths read back from a trusted store, use [Rel] to split off the
// portion under a known base before calling these helpers.
package safepath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func clean(name string) string { return strings.TrimPrefix(name, "/") }

func Rel(base, full string) (string, error) {
	rel, err := filepath.Rel(base, full)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", os.ErrPermission
	}
	return rel, nil
}

func ReadFile(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.ReadFile(clean(name))
}

func WriteFile(dir, name string, data []byte, perm os.FileMode) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(clean(name), data, perm)
}

func Open(dir, name string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(clean(name))
}

func Create(dir, name string) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Create(clean(name))
}

func Stat(dir, name string) (os.FileInfo, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(clean(name))
}

func MkdirAll(dir, name string, perm os.FileMode) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.MkdirAll(clean(name), perm)
}

// A name resolving to dir itself ("", ".", "a/..") is refused: callers build name from an id, and
// an empty id must not remove every task's files.
func RemoveAll(dir, name string) error {
	name = filepath.Clean(clean(name))
	if name == "." || name == string(os.PathSeparator) {
		return os.ErrPermission
	}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(name)
}
