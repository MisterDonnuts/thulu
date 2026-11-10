package api

import (
	"crypto/tls"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	envTLSCert = "OCTOPUS_TLS_CERT_FILE"
	envTLSKey  = "OCTOPUS_TLS_KEY_FILE"
)

func tlsFilesFromEnv() (certFile, keyFile string, err error) {
	certFile = strings.TrimSpace(os.Getenv(envTLSCert))
	keyFile = strings.TrimSpace(os.Getenv(envTLSKey))
	if (certFile == "") != (keyFile == "") {
		return "", "", apperr.Invalid("api.tls.errors.cert_and_key_together", "cert", envTLSCert, "key", envTLSKey)
	}
	return certFile, keyFile, nil
}

type certReloader struct {
	certFile, keyFile string
	checkInterval     time.Duration

	mu        sync.Mutex
	cert      *tls.Certificate
	certMod   time.Time
	keyMod    time.Time
	lastCheck time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, checkInterval: 30 * time.Second}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) load() error {
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return apperr.Wrap(err, "api.tls.errors.load_key_pair")
	}
	r.cert, r.certMod, r.keyMod = &pair, certMod, keyMod
	return nil
}

func (r *certReloader) modTimes() (certMod, keyMod time.Time, err error) {
	ci, err := os.Stat(r.certFile)
	if err != nil {
		return time.Time{}, time.Time{}, apperr.Wrap(err, "api.tls.errors.stat_certificate")
	}
	ki, err := os.Stat(r.keyFile)
	if err != nil {
		return time.Time{}, time.Time{}, apperr.Wrap(err, "api.tls.errors.stat_key")
	}
	return ci.ModTime(), ki.ModTime(), nil
}

func (r *certReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastCheck) >= r.checkInterval {
		r.lastCheck = time.Now()
		r.reloadIfChanged()
	}
	if r.cert == nil {
		return nil, apperr.Unavailable("api.tls.errors.no_certificate")
	}
	return r.cert, nil
}

func (r *certReloader) reloadIfChanged() {
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		log.Printf("[api] %s", i18n.Log("api.tls.log.stat_failed", "err", err))
		return
	}
	if certMod.Equal(r.certMod) && keyMod.Equal(r.keyMod) {
		return
	}
	if err := r.load(); err != nil {
		log.Printf("[api] %s", i18n.Log("api.tls.log.reload_failed", "err", err))
		return
	}
	log.Printf("[api] %s", i18n.Log("api.tls.log.reloaded", "path", r.certFile))
}

func serverTLSConfig(r *certReloader) *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: r.getCertificate,
	}
}
