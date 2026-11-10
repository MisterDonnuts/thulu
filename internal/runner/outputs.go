package runner

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Stops a runner from filling the daemon's disk.
const MaxOutputBytes = 200 << 20

// Symlinks are skipped so nothing outside the output dir is uploaded.
func PackOutputs(dir string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return packEntry(tw, dir, p, d)
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	return apperr.Wrap(err, "runner.errors.pack_outputs")
}

func packEntry(tw *tar.Writer, dir, p string, d fs.DirEntry) error {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." {
		return err
	}
	info, err := d.Info()
	if err != nil {
		return apperr.Wrap(err, "runner.errors.stat_output", "path", rel)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(rel)
	if info.IsDir() {
		hdr.Name += "/"
		return tw.WriteHeader(hdr)
	}
	if werr := tw.WriteHeader(hdr); werr != nil {
		return werr
	}
	f, err := os.Open(p) //nolint:gosec // G304: walking the run's own output dir
	if err != nil {
		return err
	}
	defer logx.Close(f, rel)
	_, err = io.Copy(tw, f)
	return err
}

func ExtractOutputs(r io.Reader, dst string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return apperr.Wrap(err, "runner.errors.extract_outputs")
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return apperr.Wrap(err, "runner.errors.extract_outputs")
	}
	defer root.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return apperr.Invalid("runner.errors.outputs_not_gzip")
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return apperr.Invalid("runner.errors.outputs_not_tar")
		}
		name := path.Clean(h.Name)
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0o750)
		case tar.TypeReg:
			total += h.Size
			if total > MaxOutputBytes {
				return apperr.TooLarge("runner.errors.outputs_too_large", "max", MaxOutputBytes)
			}
			err = writeOutput(root, name, tr)
		}
		if err != nil {
			return apperr.Wrap(err, "runner.errors.extract_outputs")
		}
	}
}

func writeOutput(root *os.Root, name string, r io.Reader) error {
	if err := root.MkdirAll(path.Dir(name), 0o750); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
