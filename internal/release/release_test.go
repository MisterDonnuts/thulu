package release

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func validManifest() *Manifest {
	return &Manifest{
		Schema:      SchemaVersion,
		Version:     "0.3.0",
		Channel:     ChannelStable,
		Commit:      strings.Repeat("a", 40),
		PublishedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		Images: map[string]string{
			"octopus":       "ghcr.io/misterdonnuts/octopus@" + digest('1'),
			"octopus-agent": "ghcr.io/misterdonnuts/octopus-agent@" + digest('2'),
		},
		ThirdParty:   map[string]string{"postgres": "postgres:16-alpine@" + digest('3')},
		Chart:        Chart{Ref: "oci://ghcr.io/misterdonnuts/charts/octopus:0.3.0", Digest: digest('4')},
		DeployBundle: Artifact{File: "octopus_0.3.0_deploy.tar.gz", SHA256: strings.Repeat("5", 64)},
		Binaries: map[string]Artifact{
			"linux_amd64": {File: "octopus_0.3.0_linux_amd64", SHA256: strings.Repeat("6", 64)},
		},
		UpgradeFrom: ">=0.2.0",
		RollbackTo:  "0.2.0",
		Migrations:  Migrations{Added: 6, Contract: 1},
		Requires:    []string{"OCTOPUS_SECRETBOX_KEY"},
		ManualSteps: []string{},
		Advisories: []Advisory{
			{ID: "GHSA-aaaa-bbbb-cccc", Severity: "high", Summary: "x", Affected: "<0.3.0"},
		},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	b, err := validManifest().Encode()
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !m.CanUpgradeFrom("0.2.1") || m.CanUpgradeFrom("0.1.9") || m.CanUpgradeFrom("dev") {
		t.Error("upgrade_from not applied")
	}
	if !m.RollbackWithoutRestore("0.2.0") || m.RollbackWithoutRestore("0.1.0") {
		t.Error("rollback_to not applied")
	}
	if got := m.AdvisoriesFor("0.2.0"); len(got) != 1 {
		t.Errorf("advisories for 0.2.0 = %v", got)
	}
	if got := m.AdvisoriesFor("0.3.0"); len(got) != 0 {
		t.Errorf("advisories for 0.3.0 = %v", got)
	}
}

func TestManifestRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"newer schema":            func(m *Manifest) { m.Schema = 2 },
		"v-prefixed version":      func(m *Manifest) { m.Version = "v0.3.0" },
		"prerelease on stable":    func(m *Manifest) { m.Version = "0.3.0-rc.1" },
		"unknown channel":         func(m *Manifest) { m.Channel = "nightly" },
		"short commit":            func(m *Manifest) { m.Commit = "abc1234" },
		"image by tag":            func(m *Manifest) { m.Images["octopus"] = "ghcr.io/misterdonnuts/octopus:0.3.0" },
		"no daemon image":         func(m *Manifest) { delete(m.Images, "octopus") },
		"third party by tag":      func(m *Manifest) { m.ThirdParty["caddy"] = "caddy:2-alpine" },
		"chart over https":        func(m *Manifest) { m.Chart.Ref = "https://example.com/octopus.tgz" },
		"bundle path":             func(m *Manifest) { m.DeployBundle.File = "../../etc/passwd" },
		"binary hash":             func(m *Manifest) { m.Binaries["linux_amd64"] = Artifact{File: "x", SHA256: "nope"} },
		"bad constraint":          func(m *Manifest) { m.UpgradeFrom = "~>0.2" },
		"empty constraint":        func(m *Manifest) { m.UpgradeFrom = "" },
		"rollback to newer":       func(m *Manifest) { m.RollbackTo = "0.4.0" },
		"requires not an env var": func(m *Manifest) { m.Requires = []string{"rm -rf /"} },
		"advisory without range":  func(m *Manifest) { m.Advisories[0].Affected = "" },
	} {
		m := validManifest()
		mutate(m)
		b, err := m.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(`{"schema":1,"surprise":true}`)); err == nil {
		t.Error("unknown field accepted")
	}
}

func newKey(t *testing.T) (*ecdsa.PrivateKey, Keys) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseKeys(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return priv, keys
}

func sign(t *testing.T, priv *ecdsa.PrivateKey, blob []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(blob)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

func TestVerifyManifest(t *testing.T) {
	priv, keys := newKey(t)
	b, _ := validManifest().Encode()
	sig := sign(t, priv, b)

	if _, err := keys.VerifyManifest(b, sig); err != nil {
		t.Fatalf("good signature: %v", err)
	}
	tampered := []byte(strings.Replace(string(b), `"rollback_to": "0.2.0"`, `"rollback_to": "0.1.0"`, 1))
	if _, err := keys.VerifyManifest(tampered, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered manifest: %v", err)
	}
	_, other := newKey(t)
	if _, err := other.VerifyManifest(b, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key: %v", err)
	}
	both := append(append(Keys{}, other...), keys...)
	if _, err := both.VerifyManifest(b, sig); err != nil {
		t.Errorf("rotation set: %v", err)
	}
}

func TestTrustedKeysIsTheKMSKey(t *testing.T) {
	keys, err := TrustedKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("%d embedded keys", len(keys))
	}
}

func TestParseKeysRejectsOtherAlgorithms(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if _, err := ParseKeys(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); err == nil {
		t.Error("P-384 key accepted")
	}
	if _, err := ParseKeys([]byte("not pem")); err == nil {
		t.Error("garbage accepted")
	}
}

func pushSigned(t *testing.T, host string, priv *ecdsa.PrivateKey, payloadDigest func(v1.Hash) string) string {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := name.NewRepository(host + "/octopus")
	ref := repo.Tag("0.3.0")
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	h, _ := img.Digest()
	payload := fmt.Sprintf(`{"critical":{"identity":{"docker-reference":"%s"},"image":{"docker-manifest-digest":"%s"},"type":"cosign container image signature"},"optional":null}`,
		repo.String(), payloadDigest(h))
	layer := static.NewLayer([]byte(payload), "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:       layer,
		Annotations: map[string]string{cosignSignatureAnnotation: strings.TrimSpace(string(sign(t, priv, []byte(payload))))},
		MediaType:   types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(repo.Tag(strings.Replace(h.String(), ":", "-", 1)+".sig"), sigImg); err != nil {
		t.Fatal(err)
	}
	return repo.Digest(h.String()).String()
}

func TestVerifyImage(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()
	opts := []remote.Option{remote.WithContext(ctx)}

	priv, keys := newKey(t)
	good := pushSigned(t, host, priv, func(h v1.Hash) string { return h.String() })
	if err := keys.VerifyImage(ctx, good, opts...); err != nil {
		t.Fatalf("signed image: %v", err)
	}

	_, other := newKey(t)
	if err := other.VerifyImage(ctx, good, opts...); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key: %v", err)
	}

	moved := pushSigned(t, host, priv, func(v1.Hash) string { return digest('9') })
	if err := keys.VerifyImage(ctx, moved, opts...); !errors.Is(err, ErrBadSignature) {
		t.Errorf("signature for another digest: %v", err)
	}

	img, _ := random.Image(128, 1)
	repo, _ := name.NewRepository(host + "/unsigned")
	if err := remote.Write(repo.Tag("x"), img); err != nil {
		t.Fatal(err)
	}
	h, _ := img.Digest()
	if err := keys.VerifyImage(ctx, repo.Digest(h.String()).String(), opts...); err == nil {
		t.Error("unsigned image verified")
	}

	if err := keys.VerifyImage(ctx, host+"/octopus:0.3.0", opts...); err == nil {
		t.Error("tag accepted")
	}
}
