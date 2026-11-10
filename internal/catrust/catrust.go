package catrust

import (
	"archive/tar"
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	BundleEnv = "OCTOPUS_EXTRA_CA_BUNDLE"

	// ProfileScript and images/snippets/ca-trust.sh hardcode this path; change them together.
	ContainerDir  = "/etc/octopus"
	ContainerPath = ContainerDir + "/ca.crt"

	ProfilePath = "/etc/profile.d/octopus-ca.sh"

	ConfigMapName       = "octopus-ca-bundle"
	ConfigMapCAKey      = "ca.crt"
	ConfigMapProfileKey = "profile.sh"
)

// Keep in step with images/snippets/ca-trust.sh. Never replace the system roots: merge system +
// extra.
//
//i18n:ignore shell script sourced by sandboxes, not a message
const ProfileScript = `# Installed by Thulu: trust the operator's extra CA bundle (OCTOPUS_EXTRA_CA_BUNDLE).
if [ -r /etc/octopus/ca.crt ]; then
  _octopus_ca=/etc/octopus/ca.crt
  _octopus_merged="${HOME:-/tmp}/.octopus-ca-bundle.crt"
  if [ -s "$_octopus_merged" ]; then
    _octopus_ca="$_octopus_merged"
  elif [ -r /etc/ssl/certs/ca-certificates.crt ] &&
    cat /etc/ssl/certs/ca-certificates.crt /etc/octopus/ca.crt > "$_octopus_merged" 2>/dev/null; then
    _octopus_ca="$_octopus_merged"
  fi
  export NODE_EXTRA_CA_CERTS=/etc/octopus/ca.crt
  export SSL_CERT_FILE="$_octopus_ca" REQUESTS_CA_BUNDLE="$_octopus_ca" CURL_CA_BUNDLE="$_octopus_ca" GIT_SSL_CAINFO="$_octopus_ca"
  unset _octopus_ca _octopus_merged
fi
`

// A set but unusable bundle must fail the launch, not start agents that fail every TLS handshake
// behind the proxy.
func Load() ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(BundleEnv))
	if path == "" {
		return nil, nil
	}
	return LoadFile(path)
}

func LoadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: operator-set path (OCTOPUS_EXTRA_CA_BUNDLE), not request input.
	if err != nil {
		return nil, apperr.Wrap(err, "catrust.errors.read_bundle", "env", BundleEnv, "path", path)
	}
	if err := Validate(data); err != nil {
		return nil, apperr.Wrap(err, "catrust.errors.invalid_bundle", "env", BundleEnv, "path", path)
	}
	return data, nil
}

func Validate(data []byte) error {
	certs := 0
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return apperr.WrapKind(apperr.KindInvalid, err, "catrust.errors.certificate_unparseable", "n", certs+1)
		}
		certs++
	}
	if certs == 0 {
		return apperr.Invalid("catrust.errors.no_certificates")
	}
	return nil
}

// Root-owned so the agent cannot swap the roots it is told to trust.
func Archive(ca []byte) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries := []struct {
		name string
		mode int64
		body []byte
		dir  bool
	}{
		{name: strings.TrimPrefix(ContainerDir, "/") + "/", mode: 0o755, dir: true},
		{name: strings.TrimPrefix(ContainerPath, "/"), mode: 0o644, body: ca},
		{name: strings.TrimPrefix(ProfilePath, "/"), mode: 0o644, body: []byte(ProfileScript)},
	}
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.dir {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, apperr.Wrap(err, "catrust.errors.tar_header", "name", e.name)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				return nil, apperr.Wrap(err, "catrust.errors.tar_body", "name", e.name)
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, apperr.Wrap(err, "catrust.errors.finalize_tar")
	}
	return &buf, nil
}
