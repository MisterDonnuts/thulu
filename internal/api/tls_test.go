package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeKeyPair(t *testing.T, dir string, serial int64, roots *x509.CertPool) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "octopus-test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if roots != nil {
		parsed, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		roots.AddCert(parsed)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func serveTLS(t *testing.T, r *certReloader) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		TLSConfig:         serverTLSConfig(r),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func handshake(addr string, maxVersion uint16, roots *x509.CertPool) (*big.Int, error) {
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    roots,
		MinVersion: tls.VersionTLS10,
		MaxVersion: maxVersion,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber, nil
}

func TestTLS_RefusesBelowTLS12(t *testing.T) {
	roots := x509.NewCertPool()
	certFile, keyFile := writeKeyPair(t, t.TempDir(), 1, roots)
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	addr := serveTLS(t, r)

	if _, err := handshake(addr, tls.VersionTLS11, roots); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("a TLS 1.1 client: got %v, want the server's protocol version alert", err)
	}
	if _, err := handshake(addr, tls.VersionTLS12, roots); err != nil {
		t.Fatalf("TLS 1.2 handshake: %v", err)
	}
	if _, err := handshake(addr, tls.VersionTLS13, roots); err != nil {
		t.Fatalf("TLS 1.3 handshake: %v", err)
	}
}

func TestTLS_ReloadsRenewedCertificate(t *testing.T) {
	dir := t.TempDir()
	roots := x509.NewCertPool()
	certFile, keyFile := writeKeyPair(t, dir, 1, roots)
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	r.checkInterval = 0
	addr := serveTLS(t, r)

	if got, err := handshake(addr, 0, roots); err != nil || got.Int64() != 1 {
		t.Fatalf("first handshake: serial %v err %v", got, err)
	}

	// Push mtimes forward: on filesystems with coarse timestamps the rewrite would otherwise look
	// unchanged.
	writeKeyPair(t, dir, 2, roots)
	later := time.Now().Add(time.Minute)
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, later, later); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := handshake(addr, 0, roots); err != nil || got.Int64() != 2 {
		t.Fatalf("after renewal: serial %v err %v, want 2", got, err)
	}

	if err := os.WriteFile(certFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	evenLater := later.Add(time.Minute)
	if err := os.Chtimes(certFile, evenLater, evenLater); err != nil {
		t.Fatal(err)
	}
	if got, err := handshake(addr, 0, roots); err != nil || got.Int64() != 2 {
		t.Fatalf("after a broken renewal: serial %v err %v, want the previous pair (2)", got, err)
	}
}

func TestTLS_FilesFromEnv(t *testing.T) {
	t.Setenv(envTLSCert, "")
	t.Setenv(envTLSKey, "")
	if c, k, err := tlsFilesFromEnv(); err != nil || c != "" || k != "" {
		t.Fatalf("unset: got %q %q %v, want TLS off", c, k, err)
	}

	t.Setenv(envTLSCert, "/etc/tls/tls.crt")
	if _, _, err := tlsFilesFromEnv(); err == nil {
		t.Fatal("certificate without key must be an error, not a silent plain-HTTP start")
	}

	t.Setenv(envTLSKey, "/etc/tls/tls.key")
	if c, k, err := tlsFilesFromEnv(); err != nil || c != "/etc/tls/tls.crt" || k != "/etc/tls/tls.key" {
		t.Fatalf("both set: got %q %q %v", c, k, err)
	}
}

func TestStart_TLSMisconfigurationFailsFast(t *testing.T) {
	s := &Server{srv: &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}}

	t.Setenv(envTLSCert, "/nonexistent/tls.crt")
	t.Setenv(envTLSKey, "")
	if err := s.Start(); err == nil || !strings.Contains(err.Error(), "set together") {
		t.Fatalf("half-configured TLS: got %v", err)
	}

	t.Setenv(envTLSKey, "/nonexistent/tls.key")
	if err := s.Start(); err == nil || !strings.Contains(err.Error(), "TLS certificate") {
		t.Fatalf("missing certificate file: got %v", err)
	}
}

func TestNewCertReloader_RejectsMismatchedPair(t *testing.T) {
	certA, _ := writeKeyPair(t, t.TempDir(), 1, nil)
	_, keyB := writeKeyPair(t, t.TempDir(), 2, nil)
	if _, err := newCertReloader(certA, keyB); err == nil {
		t.Fatal("a certificate with someone else's key must not load")
	}
}
