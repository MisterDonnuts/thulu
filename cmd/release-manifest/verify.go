package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/release"
)

func verifyRelease(ctx context.Context, path string, keys release.Keys, keysErr error, out io.Writer, opts ...remote.Option) error {
	if keysErr != nil {
		return keysErr
	}
	body, err := os.ReadFile(path) // #nosec G304 -- the workflow names the file
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(path + ".sig") // #nosec G304 -- next to the manifest
	if err != nil {
		return apperr.Wrap(err, "releasemanifest.errors.signature")
	}
	m, err := keys.VerifyManifest(body, sig)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, i18n.Text("releasemanifest.cli.manifest_signature_ok", "version", m.Version))

	dir, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if b, err := dir.ReadFile(m.DeployBundle.File); err == nil {
		if got := release.SHA256Hex(b); got != m.DeployBundle.SHA256 {
			return apperr.Unprocessable("releasemanifest.errors.bundle_sha_mismatch",
				"file", m.DeployBundle.File, "got", got, "want", m.DeployBundle.SHA256)
		}
		fmt.Fprintln(out, i18n.Text("releasemanifest.cli.sha256_ok", "file", m.DeployBundle.File))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	refs := make([]string, 0, len(m.Images)+1)
	for _, name := range m.ImageNames() {
		refs = append(refs, m.Images[name])
	}
	refs = append(refs, chartRef(m.Chart))
	var failed []string
	for _, ref := range refs {
		if err := keys.VerifyImage(ctx, ref, opts...); err != nil {
			failed = append(failed, apperr.Text(err))
			continue
		}
		fmt.Fprintln(out, i18n.Text("releasemanifest.cli.signature_ok", "ref", ref))
	}
	if len(failed) > 0 {
		return apperr.Unprocessable("releasemanifest.errors.signatures_failed",
			"failed", len(failed), "total", len(refs), "list", strings.Join(failed, "\n  "))
	}
	return nil
}

func chartRef(c release.Chart) string {
	ref := strings.TrimPrefix(c.Ref, "oci://")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref + "@" + c.Digest
}
