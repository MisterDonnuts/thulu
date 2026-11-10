package sandbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const InitFilesEnv = "OCTOPUS_INIT_FILES"

func initFilesPath(d string) string { return d + "/files" }

// Paths are extracted as-is; they were validated with the manifest (relative, clean, no "..").
func initFilesArchive(files map[string]string) ([]byte, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, p := range paths {
		body := []byte(files[p])
		hdr := &tar.Header{Name: p, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, apperr.Wrap(err, "sandbox.errors.init_files_pack")
		}
		if _, err := tw.Write(body); err != nil {
			return nil, apperr.Wrap(err, "sandbox.errors.init_files_pack")
		}
	}
	if err := tw.Close(); err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.init_files_pack")
	}
	if err := gz.Close(); err != nil {
		return nil, apperr.Wrap(err, "sandbox.errors.init_files_pack")
	}
	return buf.Bytes(), nil
}

func (m *Manager) writeInitSupportFiles(ctx context.Context, t *initTarget, d string, files map[string]string) error {
	dir := shellSingleQuote(initFilesPath(d))
	//i18n:ignore shell command run in the sandbox
	if err := m.initStep(ctx, t, fmt.Sprintf(`rm -rf %[1]s && mkdir -p %[1]s`, dir)); err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	archive, err := initFilesArchive(files)
	if err != nil {
		return err
	}
	tgz := d + "/files.tgz"
	if err := m.writeInitFile(ctx, t, tgz, string(archive)); err != nil {
		return err
	}
	q := shellSingleQuote(tgz)
	//i18n:ignore shell command run in the sandbox
	return m.initStep(ctx, t, fmt.Sprintf(`tar -xzf %[1]s -C %[2]s --no-same-owner && rm -f %[1]s`, q, dir))
}

func (m *Manager) initStep(ctx context.Context, t *initTarget, cmd string) error {
	res, _, err := m.initExec(ctx, t, cmd, initStepTimeoutMs)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return apperr.Internal("sandbox.errors.init_files_write", "stderr", strings.TrimSpace(res.Stderr))
	}
	return nil
}
