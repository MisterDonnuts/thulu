package release

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	"github.com/MisterDonnuts/thulu/deploy"
	"github.com/MisterDonnuts/thulu/internal/apperr"
)

var ErrBadSignature error = apperr.Unprocessable("release.errors.bad_signature")

type Keys []*ecdsa.PublicKey

func TrustedKeys() (Keys, error) {
	return ParseKeys(deploy.ReleasePublicKey)
}

// Only ECDSA P-256, what the KMS key is: accepting more would widen what a substituted key file
// could do.
func ParseKeys(pemBytes []byte) (Keys, error) {
	var keys Keys
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, apperr.Invalid("release.errors.key_unexpected_pem", "type", block.Type)
		}
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, apperr.WrapKind(apperr.KindInvalid, err, "release.errors.release_key")
		}
		ec, ok := pub.(*ecdsa.PublicKey)
		if !ok || ec.Curve != elliptic.P256() {
			return nil, apperr.Invalid("release.errors.key_not_p256")
		}
		keys = append(keys, ec)
	}
	if len(keys) == 0 {
		return nil, apperr.Invalid("release.errors.key_none_found")
	}
	return keys, nil
}

// Format of cosign `sign-blob --key`: base64 of an ASN.1 ECDSA signature over sha256(blob).
func (k Keys) VerifyBlob(blob, sigB64 []byte) error {
	sig, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sigB64)))
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "release.errors.signature_not_base64")
	}
	sum := sha256.Sum256(blob)
	for _, key := range k {
		if ecdsa.VerifyASN1(key, sum[:], sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// Verify before parsing: nothing in an unverified document is read, not even for a friendlier
// error.
func (k Keys) VerifyManifest(manifest, sig []byte) (*Manifest, error) {
	if err := k.VerifyBlob(manifest, sig); err != nil {
		return nil, apperr.Wrap(err, "release.errors.subject", "subject", ManifestFile)
	}
	return Parse(manifest)
}

func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}
