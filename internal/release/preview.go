package release

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

// Previews are signed only keylessly, never with the release key, so nothing that trusts releases
// accepts them.
const (
	// Pinned to main: a dispatch from another ref runs that ref's workflow file, which anyone who
	// can push a branch controls.
	PreviewIdentity = "https://github.com/MisterDonnuts/thulu/.github/workflows/octopus-preview.yml@refs/heads/main"
	PreviewIssuer   = "https://token.actions.githubusercontent.com"
	// Must not match the v* tags the release workflow and its signing environment react to.
	PreviewTagPrefix = "preview-v"
)

var ErrNoCosign error = apperr.Unavailable("release.errors.no_cosign")

func IsPreview(version string) bool {
	v, ok := semver.Parse(version)
	return ok && len(v.Pre) > 0 && v.Pre[0] == "dev"
}

var Cosign = func(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath("cosign")
	if err != nil {
		return nil, ErrNoCosign
	}
	// #nosec G204 -- fixed binary; the arguments are paths this package wrote and digest references it validated
	cmd := exec.CommandContext(ctx, path, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	return out.Bytes(), err
}

func VerifyPreviewManifest(ctx context.Context, manifest, bundle []byte) (*Manifest, error) {
	dir, err := os.MkdirTemp("", "octopus-preview-")
	if err != nil {
		return nil, err
	}
	defer func() { logx.Warn(os.RemoveAll(dir), "release.log.remove_temp_failed", "path", dir) }()
	mf, bf := filepath.Join(dir, ManifestFile), filepath.Join(dir, BundleFile)
	if werr := errors.Join(os.WriteFile(mf, manifest, 0o600), os.WriteFile(bf, bundle, 0o600)); werr != nil {
		return nil, werr
	}
	out, err := Cosign(ctx, "verify-blob", "--bundle", bf,
		"--certificate-identity", PreviewIdentity, "--certificate-oidc-issuer", PreviewIssuer, mf)
	if errors.Is(err, ErrNoCosign) {
		return nil, err
	}
	if err != nil {
		rejected := apperr.New("release.errors.preview_manifest_rejected", "output", strings.TrimSpace(string(out)))
		return nil, apperr.WrapKind(apperr.KindUnprocessable, rejected, "release.errors.bad_signature")
	}
	m, err := Parse(manifest)
	if err != nil {
		return nil, err
	}
	if m.Channel != ChannelPreview || !IsPreview(m.Version) {
		return nil, apperr.Unprocessable("release.errors.preview_claims_other", "channel", m.Channel, "version", m.Version)
	}
	return m, nil
}

func VerifyPreviewImage(ctx context.Context, ref string) error {
	if _, _, ok := strings.Cut(ref, "@sha256:"); !ok {
		return apperr.Invalid("release.errors.not_digest_ref", "ref", ref)
	}
	out, err := Cosign(ctx, "verify", "--certificate-identity", PreviewIdentity,
		"--certificate-oidc-issuer", PreviewIssuer, ref)
	if err != nil {
		if errors.Is(err, ErrNoCosign) {
			return err
		}
		rejected := apperr.New("release.errors.cosign_rejected", "ref", ref, "output", strings.TrimSpace(string(out)))
		return apperr.WrapKind(apperr.KindUnprocessable, rejected, "release.errors.bad_signature")
	}
	return nil
}
