package project

// Must match byte for byte the shell side in sandbox/setup.go (TestSetupKeyGoMatchesShell).
// No files: line when there are no support files, so manifests from before that field keep their
// keys.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Bump on any change to the canonical text, so old markers and snapshots stop matching.
const SetupKeyVersion = "octopus-setup-v1"

var ImplicitSetupKeyFiles = []string{".tool-versions", "mise.toml", ".mise.toml"}

type SetupFileReader func(rel string) (content []byte, found bool, err error)

func SetupToolsLine(tools map[string]string) string {
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, n := range names {
		pairs = append(pairs, n+"="+tools[n])
	}
	return strings.Join(pairs, ",")
}

func SetupFilesDigest(files map[string]string) string {
	if len(files) == 0 {
		return ""
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(p + "\n" + sha256Hex([]byte(files[p])) + "\n")
	}
	return sha256Hex([]byte(b.String()))
}

func SetupImplicitFiles(m InitManifest) []string {
	listed := map[string]bool{}
	for _, k := range m.Cache.Key {
		listed[k] = true
	}
	var out []string
	for _, f := range ImplicitSetupKeyFiles {
		if !listed[f] {
			out = append(out, f)
		}
	}
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func SetupKeyText(m InitManifest, read SetupFileReader) (string, error) {
	var b strings.Builder
	b.WriteString(SetupKeyVersion + "\n")
	b.WriteString("setup:" + sha256Hex([]byte(m.Setup)) + "\n")
	b.WriteString("tools:" + SetupToolsLine(m.Tools) + "\n")
	if d := SetupFilesDigest(m.Files); d != "" {
		b.WriteString("files:" + d + "\n")
	}
	line := func(rel string, implicit bool) error {
		content, found, err := read(rel)
		if err != nil {
			return err
		}
		switch {
		case found:
			b.WriteString("file:" + rel + ":" + sha256Hex(content) + "\n")
		case !implicit:
			b.WriteString("file:" + rel + ":-\n")
		}
		return nil
	}
	for _, rel := range m.Cache.Key {
		if err := line(rel, false); err != nil {
			return "", err
		}
	}
	for _, rel := range SetupImplicitFiles(m) {
		if err := line(rel, true); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func SetupKey(m InitManifest, read SetupFileReader) (string, error) {
	text, err := SetupKeyText(m, read)
	if err != nil {
		return "", err
	}
	return sha256Hex([]byte(text)), nil
}

func (s *Store) defaultBranchRef(ctx context.Context, projectID string) (*Project, string, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateBranchName(proj.DefaultBranch); err != nil {
		return nil, "", err
	}
	return proj, "origin/" + proj.DefaultBranch, nil
}

func (s *Store) DefaultBranchCommit(ctx context.Context, projectID string) (string, error) {
	proj, ref, err := s.defaultBranchRef(ctx, projectID)
	if err != nil {
		return "", err
	}
	return resolveCommit(proj.Path, ref)
}

const maxRootFiles = 500

func (s *Store) RootFiles(ctx context.Context, projectID string) (files []string, commit string, err error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	if rerr := proj.checkout(); rerr != nil {
		return nil, "", rerr
	}
	if berr := ValidateBranchName(proj.DefaultBranch); berr != nil {
		return nil, "", berr
	}
	ref := "origin/" + proj.DefaultBranch
	commit, err = resolveCommit(proj.Path, ref)
	if err != nil {
		return nil, "", apperr.WrapKind(apperr.KindPrecondition, err, "project.errors.root_files_no_commit", "ref", ref)
	}
	out, err := gitOutput(proj.Path, "ls-tree", "-z", "--end-of-options", commit)
	if err != nil {
		return nil, "", apperr.Wrap(err, "project.errors.root_files", "ref", ref)
	}
	files = []string{}
	for _, entry := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok || len(files) >= maxRootFiles {
			continue
		}
		if fields := strings.Fields(meta); len(fields) == 3 && fields[1] == "blob" {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, commit, nil
}

func resolveCommit(repo, ref string) (string, error) {
	out, err := gitOutput(repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", apperr.Wrap(err, "project.errors.setup_key_ref", "ref", ref)
	}
	return strings.TrimSpace(out), nil
}

func (s *Store) SetupKeyAtDefault(ctx context.Context, projectID string, m InitManifest) (key, commit string, err error) {
	proj, ref, err := s.defaultBranchRef(ctx, projectID)
	if err != nil {
		return "", "", err
	}
	commit, err = resolveCommit(proj.Path, ref)
	if err != nil {
		return "", "", err
	}
	key, err = SetupKey(m, func(rel string) ([]byte, bool, error) {
		return readTreeFile(proj.Path, commit, path.Join(m.Workdir, rel))
	})
	return key, commit, err
}

const maxSymlinkHops = 8

// Symlinks are followed only inside the repo, to match [ -f ] in the sandbox's shell key.
func readTreeFile(repo, commit, p string) ([]byte, bool, error) {
	for range maxSymlinkHops {
		out, err := gitOutput(repo, "ls-tree", "-z", "--end-of-options", commit, "--", p)
		if err != nil {
			return nil, false, apperr.Wrap(err, "project.errors.setup_key_read", "path", p)
		}
		entry, _, _ := strings.Cut(out, "\x00")
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok || name != p {
			return nil, false, nil
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, false, nil
		}
		blob, err := gitOutput(repo, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, false, apperr.Wrap(err, "project.errors.setup_key_read", "path", p)
		}
		if fields[0] != "120000" {
			return []byte(blob), true, nil
		}
		target := blob
		if strings.HasPrefix(target, "/") {
			return nil, false, nil
		}
		next := path.Clean(path.Join(path.Dir(p), target))
		if next == "." || next == ".." || strings.HasPrefix(next, "../") {
			return nil, false, nil
		}
		p = next
	}
	return nil, false, nil
}

// Close returns git's failure: a caller that read to EOF must still check it.
func (s *Store) OpenDefaultBranchArchive(ctx context.Context, projectID, commit string) (io.ReadCloser, error) {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return archiveCommit(ctx, proj.Path, commit, "tar.gz")
}

func (s *Store) PrewarmSourceDir(projectID string) (string, error) {
	if err := validateTaskID(projectID); err != nil || s.baseDir == "" {
		return "", apperr.Invalid("project.errors.setup_archive_dir", "id", projectID)
	}
	return filepath.Join(s.baseDir, prewarmDirName, projectID), nil
}

func (s *Store) ExtractDefaultBranch(ctx context.Context, projectID, commit, dest string) error {
	proj, err := s.Get(ctx, projectID)
	if err != nil {
		return err
	}
	if err = os.RemoveAll(dest); err != nil {
		return apperr.Wrap(err, "project.errors.setup_archive", "output", dest)
	}
	if err = os.MkdirAll(dest, 0o750); err != nil {
		return apperr.Wrap(err, "project.errors.setup_archive", "output", dest)
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return apperr.Wrap(err, "project.errors.setup_archive", "output", dest)
	}
	defer root.Close()
	src, err := archiveCommit(ctx, proj.Path, commit, "tar")
	if err != nil {
		return err
	}
	defer src.Close()
	if untarErr := untar(src, root); untarErr != nil {
		return untarErr
	}
	return src.Close()
}

func untar(r io.Reader, root *os.Root) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return apperr.Wrap(err, "project.errors.setup_archive", "output", "")
		}
		name := path.Clean(h.Name)
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0o750)
		case tar.TypeReg:
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			err = writeTarFile(root, name, tr, mode)
		case tar.TypeSymlink:
			if err = root.MkdirAll(path.Dir(name), 0o750); err == nil {
				err = root.Symlink(h.Linkname, name)
			}
		}
		if err != nil {
			return apperr.Wrap(err, "project.errors.setup_archive", "output", name)
		}
	}
}

func writeTarFile(root *os.Root, name string, r io.Reader, mode os.FileMode) error {
	if err := root.MkdirAll(path.Dir(name), 0o750); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func archiveCommit(ctx context.Context, repo, commit, format string, extra ...string) (io.ReadCloser, error) {
	if !isHexObject(commit) {
		return nil, apperr.Invalid("project.errors.setup_archive_commit", "commit", commit)
	}
	args := append(append([]string{"archive", "--format=" + format}, extra...), commit)
	cmd, err := repoGit(repo).commandContext(ctx, repo, args)
	if err != nil {
		return nil, err
	}
	a := &gitArchive{cmd: cmd}
	cmd.Stderr = &a.stderr
	if a.out, err = cmd.StdoutPipe(); err != nil {
		return nil, apperr.Wrap(err, "project.errors.setup_archive", "output", "")
	}
	if err := cmd.Start(); err != nil {
		return nil, apperr.Wrap(err, "project.errors.setup_archive", "output", "")
	}
	return a, nil
}

type gitArchive struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr strings.Builder
	once   sync.Once
	err    error
}

func (a *gitArchive) Read(p []byte) (int, error) { return a.out.Read(p) }

func (a *gitArchive) Close() error {
	a.once.Do(func() {
		_ = a.out.Close()
		if err := a.cmd.Wait(); err != nil {
			a.err = apperr.Wrap(err, "project.errors.setup_archive", "output", strings.TrimSpace(a.stderr.String()))
		}
	})
	return a.err
}

func isHexObject(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
