package releasetest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/MisterDonnuts/thulu/internal/release"
)

type Key struct {
	Private *ecdsa.PrivateKey
	Keys    release.Keys
	PEM     []byte
}

func NewKey(t testing.TB) *Key {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	p := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	keys, err := release.ParseKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	return &Key{Private: priv, Keys: keys, PEM: p}
}

func (k *Key) SignBlob(t testing.TB, blob []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(blob)
	sig, err := ecdsa.SignASN1(rand.Reader, k.Private, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

func (k *Key) SignManifest(t testing.TB, m *release.Manifest) (body, sig []byte) {
	t.Helper()
	body, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return body, k.SignBlob(t, body)
}

func PushImage(t testing.TB, host, repo, tag string) string {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.NewRepository(host + "/" + repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = remote.Write(r.Tag(tag), img); err != nil {
		t.Fatal(err)
	}
	h, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return r.Digest(h.String()).String()
}

func (k *Key) SignImage(t testing.TB, ref string) {
	t.Helper()
	d, err := name.NewDigest(ref)
	if err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"critical":{"identity":{"docker-reference":%q},`+
		//i18n:ignore cosign simple-signing payload, a protocol document
		`"image":{"docker-manifest-digest":%q},"type":"cosign container image signature"},"optional":null}`,
		d.Context().String(), d.DigestStr())
	sigTag := d.Context().Tag(strings.Replace(d.DigestStr(), ":", "-", 1) + ".sig")
	base := v1.Image(empty.Image)
	if existing, lookupErr := remote.Image(sigTag); lookupErr == nil {
		base = existing
	}
	sigImg, err := mutate.Append(base, mutate.Addendum{
		Layer:       static.NewLayer([]byte(payload), "application/vnd.dev.cosign.simplesigning.v1+json"),
		Annotations: map[string]string{"dev.cosignproject.cosign/signature": strings.TrimSpace(string(k.SignBlob(t, []byte(payload))))},
		MediaType:   types.MediaType("application/vnd.dev.cosign.simplesigning.v1+json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = remote.Write(sigTag, sigImg); err != nil {
		t.Fatal(err)
	}
}
