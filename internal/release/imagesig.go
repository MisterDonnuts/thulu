package release

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const cosignSignatureAnnotation = "dev.cosignproject.cosign/signature"

const maxPayload = 64 << 10

type simpleSigning struct {
	Critical struct {
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
}

func RemoteOptions(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}
}

// docker-reference is not compared: after `octopus mirror` it names the original repository.
func (k Keys) VerifyImage(ctx context.Context, ref string, opts ...remote.Option) error {
	d, err := name.NewDigest(ref)
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "release.errors.not_digest_ref", "ref", ref)
	}
	if len(opts) == 0 {
		opts = RemoteOptions(ctx)
	}
	sigTag := d.Context().Tag(strings.Replace(d.DigestStr(), ":", "-", 1) + ".sig")
	sigImg, err := remote.Image(sigTag, opts...)
	if err != nil {
		return apperr.Wrap(err, "release.errors.no_image_signature", "ref", ref, "tag", sigTag)
	}
	m, err := sigImg.Manifest()
	if err != nil {
		return apperr.Wrap(err, "release.errors.read_signature_manifest", "ref", ref)
	}
	for i := range m.Layers {
		desc := &m.Layers[i]
		sig := desc.Annotations[cosignSignatureAnnotation]
		if sig == "" {
			continue
		}
		layer, err := sigImg.LayerByDigest(desc.Digest)
		if err != nil {
			continue
		}
		rc, err := layer.Compressed()
		if err != nil {
			continue
		}
		// The registry layer reader verifies content against desc.Digest while reading, so a
		// swapped payload fails here.
		payload, err := io.ReadAll(io.LimitReader(rc, maxPayload))
		_ = rc.Close()
		if err != nil {
			continue
		}
		if k.VerifyBlob(payload, []byte(sig)) != nil {
			continue
		}
		var p simpleSigning
		if err := json.Unmarshal(payload, &p); err != nil {
			continue
		}
		if p.Critical.Image.DockerManifestDigest == d.DigestStr() {
			return nil
		}
	}
	return apperr.Wrap(ErrBadSignature, "release.errors.subject", "subject", ref)
}
