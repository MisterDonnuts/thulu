package catrust

import (
	"archive/tar"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Thulu Test Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestLoadUnsetIsNoop(t *testing.T) {
	t.Setenv(BundleEnv, "")
	got, err := Load()
	if err != nil || got != nil {
		t.Fatalf("Load() = %v, %v; want nil, nil", got, err)
	}
}

func TestLoadValidatesBundle(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.pem")
	if err := os.WriteFile(good, testCA(t), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BundleEnv, good)
	if data, err := Load(); err != nil || len(data) == 0 {
		t.Fatalf("Load(good) = %d bytes, %v", len(data), err)
	}

	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BundleEnv, bad)
	if _, err := Load(); err == nil {
		t.Fatal("Load(bad) must fail: a set-but-unusable bundle is a misconfiguration")
	}

	t.Setenv(BundleEnv, filepath.Join(dir, "missing.pem"))
	if _, err := Load(); err == nil {
		t.Fatal("Load(missing) must fail")
	}
}

func TestArchiveLayout(t *testing.T) {
	ca := testCA(t)
	buf, err := Archive(ca)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(buf)
	seen := map[string]string{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Uid != 0 || hdr.Gid != 0 {
			t.Errorf("%s owned by %d:%d; want root so the agent cannot swap its roots", hdr.Name, hdr.Uid, hdr.Gid)
		}
		body, _ := io.ReadAll(tr)
		seen[hdr.Name] = string(body)
	}
	if seen["etc/octopus/ca.crt"] != string(ca) {
		t.Error("archive must place the bundle at etc/octopus/ca.crt")
	}
	if seen["etc/profile.d/octopus-ca.sh"] != ProfileScript {
		t.Error("archive must place the profile hook at etc/profile.d/octopus-ca.sh")
	}
}
