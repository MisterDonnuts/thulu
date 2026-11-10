package project

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// The archive is tenant input. Links are never created (a later entry could write through one), and
// only HEAD,
// refs and objects of the top-level .git are kept: its config, hooks and alternates must never
// reach git.

const MaxUploadArchiveBytes int64 = 200 << 20

type UploadLimits struct {
	MaxFiles     int
	MaxEntries   int
	MaxBytes     int64
	MaxFileBytes int64
}

func DefaultUploadLimits() UploadLimits {
	return UploadLimits{MaxFiles: 20000, MaxEntries: 200000, MaxBytes: 1 << 30, MaxFileBytes: MaxUploadArchiveBytes}
}

// Keep in sync with web/src/lib/project-upload.ts.
var UploadSkipDirs = []string{
	"node_modules", ".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache",
	".ruff_cache", ".tox", ".gradle", ".next", ".nuxt", ".turbo", ".parcel-cache",
	".cache", "__MACOSX",
}

var junkFiles = []string{".DS_Store", "Thumbs.db", "desktop.ini"}

var secretNames = []string{
	".env", ".netrc", ".pgpass", ".pypirc", ".git-credentials", ".htpasswd",
	"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
}

var secretExts = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".ppk"}

var envTemplates = []string{"example", "sample", "template", "dist", "defaults"}

func IsSecretPath(p string) bool {
	base := path.Base(p)
	lower := strings.ToLower(base)
	if slices.Contains(secretNames, lower) {
		return true
	}
	if rest, ok := strings.CutPrefix(lower, ".env."); ok {
		return !slices.Contains(envTemplates, rest)
	}
	if slices.Contains(secretExts, path.Ext(lower)) {
		return true
	}
	return lower == "credentials" && path.Base(path.Dir(p)) == ".aws"
}

type UploadReport struct {
	Files          int      `json:"files"`
	Bytes          int64    `json:"bytes"`
	Excluded       []string `json:"excluded,omitempty"`
	ExcludedCount  int      `json:"excluded_count"`
	Skipped        []string `json:"skipped,omitempty"`
	SkippedCount   int      `json:"skipped_count"`
	History        bool     `json:"history"`
	HistoryIgnored bool     `json:"history_ignored,omitempty"`
	Branch         string   `json:"branch"`
	Commit         string   `json:"commit"`
	Version        string   `json:"version,omitempty"`
	Changed        bool     `json:"changed"`
}

const reportListCap = 100

func (r *UploadReport) exclude(p string) {
	r.ExcludedCount++
	if len(r.Excluded) < reportListCap {
		r.Excluded = append(r.Excluded, p)
	}
}

func (r *UploadReport) skip(p string) {
	if slices.Contains(r.Skipped, p) {
		return
	}
	r.SkippedCount++
	if len(r.Skipped) < reportListCap {
		r.Skipped = append(r.Skipped, p)
	}
}

type unpacked struct {
	files  string
	git    string
	hasGit bool
	report UploadReport
	// Excluded from the commit so a skipped file a previous version tracked is not read as deleted.
	keep []string
}

func errUploadEntry(name string) error {
	return apperr.Invalid("project.errors.upload_bad_entry", "name", name)
}

func unpackUpload(src, dir string, lim UploadLimits) (*unpacked, error) {
	u := &unpacked{files: filepath.Join(dir, "files"), git: filepath.Join(dir, "git")}
	for _, d := range []string{u.files, u.git} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, apperr.Wrap(err, "project.errors.upload_unpack")
		}
	}
	filesRoot, err := os.OpenRoot(u.files)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.upload_unpack")
	}
	defer filesRoot.Close()
	gitRoot, err := os.OpenRoot(u.git)
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.upload_unpack")
	}
	defer gitRoot.Close()
	w := &uploadWriter{u: u, files: filesRoot, git: gitRoot, lim: lim}

	f, err := os.Open(src) //nolint:gosec // G304: src is the daemon's own temp file
	if err != nil {
		return nil, apperr.Wrap(err, "project.errors.upload_unpack")
	}
	defer f.Close()
	var magic [4]byte
	n, err := io.ReadFull(f, magic[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, apperr.Wrap(err, "project.errors.upload_unpack")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, apperr.Wrap(err, "project.errors.upload_unpack")
	}
	switch {
	case n >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		err = w.readTarGz(f)
	case n == 4 && bytes.Equal(magic[:], []byte("PK\x03\x04")):
		err = w.readZip(f)
	default:
		err = apperr.Invalid("project.errors.upload_format")
	}
	if err != nil {
		return nil, err
	}
	for _, d := range UploadSkipDirs {
		u.keep = append(u.keep, ":(exclude,glob)**/"+d+"/**") //i18n:ignore git pathspec
	}
	return u, nil
}

type uploadWriter struct {
	u          *unpacked
	files, git *os.Root
	lim        UploadLimits
	entries    int
	total      int64
	prefix     string
}

func (w *uploadWriter) entry(raw string) (dest string, toGit bool, err error) {
	w.entries++
	if w.lim.MaxEntries > 0 && w.entries > w.lim.MaxEntries {
		return "", false, apperr.TooLarge("project.errors.upload_too_many", "max", w.lim.MaxEntries)
	}
	if strings.ContainsRune(raw, 0) {
		return "", false, errUploadEntry(raw)
	}
	name := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(raw, "\\", "/")), "/")
	name = strings.TrimPrefix(name, w.prefix)
	if name == "" || name == "." || strings.HasPrefix(name, "__MACOSX/") {
		return "", false, nil
	}
	parts := strings.Split(name, "/")
	if parts[0] == ".git" {
		if rel, ok := historyPath(parts[1:]); ok {
			return rel, true, nil
		}
		return "", false, nil
	}
	for i, p := range parts {
		if p == ".git" {
			w.u.report.skip(strings.Join(parts[:i+1], "/") + "/")
			return "", false, nil
		}
		if i < len(parts)-1 && slices.Contains(UploadSkipDirs, p) {
			w.u.report.skip(strings.Join(parts[:i+1], "/") + "/")
			return "", false, nil
		}
	}
	base := parts[len(parts)-1]
	if slices.Contains(junkFiles, base) {
		return "", false, nil
	}
	if IsSecretPath(name) {
		w.u.report.exclude(name)
		w.u.keep = append(w.u.keep, ":(exclude,literal)"+name) //i18n:ignore git pathspec
		return "", false, nil
	}
	return name, false, nil
}

// objects/info is dropped: alternates there would point git at another repository's objects.
func historyPath(parts []string) (string, bool) {
	if len(parts) == 0 {
		return "", false
	}
	switch parts[0] {
	case "HEAD", "packed-refs", "shallow", "config":
		// config is read as text only (adoptUploadedGit); it never becomes the repository's config.
		return parts[0], len(parts) == 1
	case "refs":
		return strings.Join(parts, "/"), len(parts) > 1
	case "objects":
		return strings.Join(parts, "/"), len(parts) > 1 && parts[1] != "info"
	}
	return "", false
}

func (w *uploadWriter) link(raw string) error {
	dest, toGit, err := w.entry(raw)
	if err != nil || dest == "" || toGit {
		return err
	}
	w.u.report.skip(dest)
	w.u.keep = append(w.u.keep, ":(exclude,literal)"+dest) //i18n:ignore git pathspec
	return nil
}

func (w *uploadWriter) file(raw string, size int64, exec bool, r io.Reader) error {
	dest, toGit, err := w.entry(raw)
	if err != nil || dest == "" {
		return err
	}
	if w.lim.MaxFileBytes > 0 && size > w.lim.MaxFileBytes {
		return apperr.TooLarge("project.errors.upload_file_too_big", "name", dest, "max", humanBytes(w.lim.MaxFileBytes))
	}
	root := w.files
	if toGit {
		root = w.git
		w.u.hasGit = true
	} else {
		w.u.report.Files++
		if w.lim.MaxFiles > 0 && w.u.report.Files > w.lim.MaxFiles {
			return apperr.TooLarge("project.errors.upload_too_many", "max", w.lim.MaxFiles)
		}
	}
	if err = root.MkdirAll(path.Dir(dest), 0o750); err != nil {
		return errUploadEntry(raw)
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	out, err := root.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errUploadEntry(raw)
	}
	// Count bytes actually read, not the header's size: a header can lie.
	budget := w.lim.MaxBytes - w.total
	if w.lim.MaxBytes <= 0 {
		budget = 1 << 62
	}
	n, err := io.Copy(out, io.LimitReader(r, budget+1))
	closeErr := out.Close()
	w.total += n
	if !toGit {
		w.u.report.Bytes += n
	}
	if err != nil || closeErr != nil {
		return apperr.Wrap(errors.Join(err, closeErr), "project.errors.upload_unpack")
	}
	if n > budget {
		return apperr.TooLarge("project.errors.upload_too_big", "max", humanBytes(w.lim.MaxBytes))
	}
	return nil
}

func (w *uploadWriter) readTarGz(f io.Reader) error {
	gz, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return apperr.Wrap(err, "project.errors.upload_format")
	}
	err = w.readTar(tar.NewReader(gz))
	if cerr := gz.Close(); err == nil && cerr != nil {
		err = apperr.Wrap(cerr, "project.errors.upload_format")
	}
	return err
}

func (w *uploadWriter) readTar(tr *tar.Reader) error {
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return apperr.Wrap(err, "project.errors.upload_format")
		}
		switch h.Typeflag {
		case tar.TypeReg:
			err = w.file(h.Name, h.Size, h.Mode&0o111 != 0, tr)
		case tar.TypeSymlink, tar.TypeLink:
			err = w.link(h.Name)
		}
		if err != nil {
			return err
		}
	}
}

func (w *uploadWriter) readZip(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return apperr.Wrap(err, "project.errors.upload_unpack")
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return apperr.Wrap(err, "project.errors.upload_format")
	}
	w.prefix = zipPrefix(zr.File)
	for _, zf := range zr.File {
		mode := zf.Mode()
		switch {
		case mode.IsDir() || strings.HasSuffix(zf.Name, "/"):
			continue
		case mode&os.ModeSymlink != 0:
			err = w.link(zf.Name)
		case mode.IsRegular():
			var rc io.ReadCloser
			if rc, err = zf.Open(); err != nil {
				return apperr.Wrap(err, "project.errors.upload_format")
			}
			size := int64(zf.UncompressedSize64) //nolint:gosec // G115: compared against a cap, and the copy is capped again
			err = w.file(zf.Name, size, mode&0o111 != 0, rc)
			_ = rc.Close()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func zipPrefix(files []*zip.File) string {
	prefix := ""
	for _, zf := range files {
		name := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(zf.Name, "\\", "/")), "/")
		first, _, nested := strings.Cut(name, "/")
		if first == "__MACOSX" || name == "" {
			continue
		}
		if !nested && !strings.HasSuffix(zf.Name, "/") {
			return ""
		}
		if prefix == "" {
			prefix = first
		} else if first != prefix {
			return ""
		}
	}
	if prefix == "" || prefix == ".git" {
		return ""
	}
	return prefix + "/"
}

func humanBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return strconv.FormatFloat(f, 'f', -1, 64) + " " + units[i]
}
