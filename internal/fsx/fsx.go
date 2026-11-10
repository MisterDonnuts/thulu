package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

func WriteFile(name string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(name)
	// CreateTemp opens 0600 and Chmod runs before Write, so the data is never readable by others
	// mid-write.
	f, err := os.CreateTemp(dir, "."+filepath.Base(name)+".tmp-*")
	if err != nil {
		return err
	}
	err = f.Chmod(perm)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		return errors.Join(err, os.Remove(f.Name()))
	}
	return syncDir(dir)
}

// Windows cannot sync a directory and some file systems refuse (EINVAL, ErrUnsupported); there the
// rename is as durable as it gets.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	err = d.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
		err = nil
	}
	return errors.Join(err, d.Close())
}
