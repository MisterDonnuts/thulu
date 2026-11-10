package checktest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/semver"
)

func Digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func Manifest(version, channel string) *release.Manifest {
	rollbackTo := ""
	if semver.Newer(version, "0.2.0") {
		rollbackTo = "0.2.0"
	}
	return &release.Manifest{
		Schema:      release.SchemaVersion,
		Version:     version,
		Channel:     channel,
		Commit:      strings.Repeat("a", 40),
		PublishedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		Images: map[string]string{
			"octopus":       "ghcr.io/misterdonnuts/octopus@" + Digest('1'),
			"octopus-agent": "ghcr.io/misterdonnuts/octopus-agent@" + Digest('2'),
		},
		Chart:        release.Chart{Ref: "oci://ghcr.io/misterdonnuts/charts/octopus:" + version, Digest: Digest('4')},
		DeployBundle: release.Artifact{File: "octopus_" + version + "_deploy.tar.gz", SHA256: strings.Repeat("5", 64)},
		UpgradeFrom:  ">=0.2.0",
		RollbackTo:   rollbackTo,
		Migrations:   release.Migrations{Added: 6, Contract: 1},
		Requires:     []string{"OCTOPUS_SECRETBOX_KEY"},
		ManualSteps:  []string{},
		Advisories: []release.Advisory{
			//i18n:ignore fixture advisory data, not a message
			{ID: "GHSA-aaaa-bbbb-cccc", Severity: "high", Summary: "token leak", Affected: "<0.3.0"},
		},
	}
}

type Signer struct {
	priv *ecdsa.PrivateKey
	Keys release.Keys
}

func NewSigner(t testing.TB) *Signer {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := release.ParseKeys(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return &Signer{priv: priv, Keys: keys}
}

func (s *Signer) Sign(t testing.TB, blob []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(blob)
	sig, err := ecdsa.SignASN1(rand.Reader, s.priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

type Source struct {
	mu       sync.Mutex
	listings []release.Listing
	files    map[string][]byte
	listErr  error

	Lists atomic.Int32
	Opens atomic.Int32
}

func (f *Source) Name() string { return "fake" }

func (f *Source) List(context.Context) ([]release.Listing, error) {
	f.Lists.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]release.Listing(nil), f.listings...), f.listErr
}

func (f *Source) Open(_ context.Context, version, file string) (io.ReadCloser, error) {
	f.Opens.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.files[version+"/"+file]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *Source) FailList(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErr = err
}

func (f *Source) Publish(t testing.TB, s *Signer, m *release.Manifest, listedChannel string) {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[m.Version+"/"+release.ManifestFile] = b
	f.files[m.Version+"/"+release.SignatureFile] = s.Sign(t, b)
	f.listings = append(f.listings, release.Listing{Version: m.Version, Channel: listedChannel, Notes: "notes " + m.Version})
}

func (f *Source) PublishPreview(t testing.TB, version string) {
	t.Helper()
	m := Manifest(version, release.ChannelPreview)
	m.Advisories = nil
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[version+"/"+release.ManifestFile] = b
	f.files[version+"/"+release.BundleFile] = []byte(`{"bundle":"placeholder"}`)
	f.listings = append(f.listings, release.Listing{Version: version, Channel: release.ChannelPreview, Notes: "notes " + version})
}
