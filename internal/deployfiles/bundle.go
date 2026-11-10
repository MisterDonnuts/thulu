package deployfiles

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// The real bundle is a few hundred KB; these limits only stop a hostile archive from filling
// memory.
const (
	maxEntries   = 2000
	maxEntrySize = 8 << 20
	maxTreeSize  = 64 << 20
)

// Even after the sha256 check, only regular files at clean relative paths: a bundle must never
// write outside deploy/, through a link, or over .env.
func ReadBundle(r io.Reader) (Tree, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, apperr.Wrap(err, "deployfiles.errors.bundle")
	}
	t, err := readTar(tar.NewReader(gz))
	if cerr := gz.Close(); err == nil && cerr != nil {
		return nil, apperr.Wrap(cerr, "deployfiles.errors.bundle")
	}
	return t, err
}

func readTar(tr *tar.Reader) (Tree, error) {
	t := Tree{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, apperr.Wrap(err, "deployfiles.errors.bundle")
		}
		if bad := checkEntry(t, h); bad != nil {
			return nil, bad
		}
		if total += h.Size; total > maxTreeSize {
			return nil, apperr.TooLarge("deployfiles.errors.bundle_too_large", "max", maxTreeSize)
		}
		body, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil {
			return nil, apperr.Wrap(err, "deployfiles.errors.bundle_entry_read", "name", h.Name)
		}
		if int64(len(body)) != h.Size {
			return nil, apperr.Invalid("deployfiles.errors.bundle_entry_truncated", "name", h.Name)
		}
		// The archive's mode is not trusted (setuid, world-writable); only executable-or-not is
		// derived from the name.
		t[h.Name] = File{Body: body, Mode: ModeFor(h.Name)}
	}
	if len(t) == 0 {
		return nil, apperr.Invalid("deployfiles.errors.bundle_empty")
	}
	return t, nil
}

func checkEntry(t Tree, h *tar.Header) error {
	_, dup := t[h.Name]
	switch {
	case h.Typeflag != tar.TypeReg:
		return apperr.Invalid("deployfiles.errors.bundle_not_regular", "name", h.Name, "type", h.Typeflag)
	case !ValidPath(h.Name):
		return apperr.Invalid("deployfiles.errors.bundle_bad_path", "name", h.Name)
	case dup:
		return apperr.Invalid("deployfiles.errors.bundle_duplicate", "name", h.Name)
	case len(t) >= maxEntries:
		return apperr.TooLarge("deployfiles.errors.bundle_too_many_files", "max", maxEntries)
	case h.Size < 0 || h.Size > maxEntrySize:
		return apperr.TooLarge("deployfiles.errors.bundle_entry_too_large", "name", h.Name, "size", h.Size)
	}
	return nil
}
