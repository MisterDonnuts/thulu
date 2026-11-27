package runner

import (
	"archive/tar"
	"cmp"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// Stops a runner from filling the daemon's disk.
const MaxOutputBytes = 200 << 20

// OutputBudget is what a runner packs: the tar headers, the padding and the gzip framing of the
// files still fit under MaxOutputBytes.
const OutputBudget = MaxOutputBytes - 4<<20

// Each entry costs its header (a long name adds a PAX header) and the padding to the next block.
const tarEntryCost = 3 * 512

// A file the output budget left out.
type SkippedOutput struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// What the dispatcher reads from the output dir. They go first so a run's verdict reaches the
// daemon whatever else the agent left there.
var controlOutputs = map[string]bool{
	"verdict.md": true, "feedback.md": true, "auditor-verdict.md": true, "comment.md": true,
	"auditor-comment.md": true, "_index.md": true, "flags.yaml": true, "locked-tests.txt": true,
}

// Reports and data: small, and what a person reads first.
var textOutputs = map[string]bool{
	".md": true, ".json": true, ".yaml": true, ".yml": true, ".txt": true, ".csv": true, ".log": true,
}

type outputFile struct {
	rel  string
	abs  string
	info fs.FileInfo
}

func outputRank(rel string) int {
	switch {
	case !strings.Contains(rel, "/") && (controlOutputs[rel] || strings.HasSuffix(rel, ".posted")):
		return 0
	case textOutputs[strings.ToLower(path.Ext(rel))]:
		return 1
	}
	return 2
}

// PackOutputs packs dir within OutputBudget: control files first, then reports, then the rest from
// the smallest up. A file that does not fit is left out and listed; the upload goes on without it.
// Symlinks are skipped so nothing outside the output dir is uploaded.
func PackOutputs(dir string, w io.Writer) ([]SkippedOutput, error) {
	return packOutputs(dir, w, OutputBudget)
}

func packOutputs(dir string, w io.Writer, budget int64) ([]SkippedOutput, error) {
	dirs, files, err := listOutputs(dir)
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.pack_outputs")
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	left := budget
	for _, d := range dirs {
		if err = writeTarEntry(tw, d); err != nil {
			return nil, apperr.Wrap(err, "runner.errors.pack_outputs")
		}
		left -= tarEntryCost
	}
	var skipped []SkippedOutput
	for _, f := range files {
		cost := f.info.Size() + tarEntryCost
		if cost > left {
			skipped = append(skipped, SkippedOutput{Path: f.rel, Size: f.info.Size()})
			continue
		}
		if err = writeTarEntry(tw, f); err != nil {
			return nil, apperr.Wrap(err, "runner.errors.pack_outputs")
		}
		left -= cost
	}
	if err = tw.Close(); err == nil {
		err = gz.Close()
	}
	return skipped, apperr.Wrap(err, "runner.errors.pack_outputs")
}

// listOutputs returns the directories in walk order and the regular files in packing order.
func listOutputs(dir string) (dirs, files []outputFile, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil || rel == "." {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return apperr.Wrap(infoErr, "runner.errors.stat_output", "path", rel)
		}
		f := outputFile{rel: filepath.ToSlash(rel), abs: p, info: info}
		switch {
		case info.IsDir():
			dirs = append(dirs, f)
		case info.Mode().IsRegular():
			files = append(files, f)
		}
		return nil
	})
	slices.SortStableFunc(files, func(a, b outputFile) int {
		return cmp.Or(cmp.Compare(outputRank(a.rel), outputRank(b.rel)),
			cmp.Compare(a.info.Size(), b.info.Size()), cmp.Compare(a.rel, b.rel))
	})
	return dirs, files, err
}

func writeTarEntry(tw *tar.Writer, f outputFile) error {
	hdr, err := tar.FileInfoHeader(f.info, "")
	if err != nil {
		return err
	}
	hdr.Name = f.rel
	if f.info.IsDir() {
		hdr.Name += "/"
		return tw.WriteHeader(hdr)
	}
	if werr := tw.WriteHeader(hdr); werr != nil {
		return werr
	}
	file, err := os.Open(f.abs)
	if err != nil {
		return err
	}
	defer logx.Close(file, f.rel)
	// The header promised this size: a file that grew since the walk must not overrun it.
	_, err = io.CopyN(tw, file, f.info.Size())
	return err
}

// ExtractOutputs unpacks a run's outputs under the cap the runner packs for: a file that would take
// the total over MaxOutputBytes is left out and listed, and the rest still lands.
func ExtractOutputs(r io.Reader, dst string) ([]SkippedOutput, error) {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return nil, apperr.Wrap(err, "runner.errors.extract_outputs")
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, apperr.Wrap(err, "runner.errors.extract_outputs")
	}
	defer root.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, apperr.Invalid("runner.errors.outputs_not_gzip")
	}
	x := extraction{root: root, tr: tar.NewReader(gz)}
	for {
		h, err := x.tr.Next()
		if errors.Is(err, io.EOF) {
			return x.skipped, nil
		}
		if err != nil {
			if tooLarge(err) {
				return x.skipped, apperr.TooLarge("runner.errors.outputs_too_large", "max", MaxOutputBytes)
			}
			return x.skipped, apperr.Invalid("runner.errors.outputs_not_tar")
		}
		err = x.entry(h)
		if tooLarge(err) {
			return x.skipped, apperr.TooLarge("runner.errors.outputs_too_large", "max", MaxOutputBytes)
		}
		if err != nil {
			return x.skipped, apperr.Wrap(err, "runner.errors.extract_outputs")
		}
	}
}

type extraction struct {
	root    *os.Root
	tr      *tar.Reader
	total   int64
	skipped []SkippedOutput
}

// entry writes one tar entry under the root; a path that leaves it is dropped, and a file that
// would take the total over MaxOutputBytes is listed instead of written.
func (x *extraction) entry(h *tar.Header) error {
	name := path.Clean(h.Name)
	if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
		return nil
	}
	switch h.Typeflag {
	case tar.TypeDir:
		return x.root.MkdirAll(name, 0o750)
	case tar.TypeReg:
		if x.total+h.Size > MaxOutputBytes {
			x.skipped = append(x.skipped, SkippedOutput{Path: name, Size: h.Size})
			return nil
		}
		x.total += h.Size
		return writeOutput(x.root, name, x.tr)
	}
	return nil
}

// The daemon caps the request body: a body cut there is too large, not broken.
func tooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
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
