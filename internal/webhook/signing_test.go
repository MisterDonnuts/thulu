package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type captureServer struct {
	mu   sync.Mutex
	sigs []string
	body []byte
	*httptest.Server
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	c := &captureServer{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.sigs = append(c.sigs, r.Header.Get("X-Octopus-Signature"))
		c.body = b
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *captureServer) last(t *testing.T) (sig string, body []byte) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sigs) == 0 {
		t.Fatal("no delivery reached the receiver")
	}
	return c.sigs[len(c.sigs)-1], c.body
}

func verify(secret string, body []byte, header string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal([]byte(header), []byte("sha256="+hex.EncodeToString(mac.Sum(nil))))
}

func TestDelivery_SignedWithTheSecretTheReceiverWasShown(t *testing.T) {
	rcv := newCaptureServer(t)
	s := newInternalStore(t)
	ep, err := s.CreateEndpoint(t.Context(), "ws-sig", rcv.URL, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if ep.LegacySigning {
		t.Error("a new endpoint must not be marked legacy_signing")
	}
	d := newOutboxDispatcher(s)
	if _, err := d.DeliverTest(t.Context(), ep.ID, "ws-sig", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	sig, body := rcv.last(t)
	if !verify(ep.Secret, body, sig) {
		t.Fatalf("signature %q does not verify with the endpoint secret", sig)
	}
	if verify(hashSecret(ep.Secret), body, sig) {
		t.Error("new endpoint still signed with the legacy hash key")
	}
}

func insertLegacy(t *testing.T, s *Store, id, ws, url, secret string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO webhook_endpoints (id, workspace_id, url, secret_hash, events, active, created_at)
		VALUES ($1, $2, $3, $4, '["*"]', 1, NOW())`, id, ws, url, hashSecret(secret)); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyEndpoint_KeepsLegacySigningUntilRotated(t *testing.T) {
	rcv := newCaptureServer(t)
	s := newInternalStore(t)
	const secret = "whsec_legacy"
	insertLegacy(t, s, "ep-legacy", "ws-legacy", rcv.URL, secret)

	list, err := s.ListEndpoints(t.Context(), "ws-legacy")
	if err != nil || len(list) != 1 || !list[0].LegacySigning {
		t.Fatalf("ListEndpoints must report legacy_signing, got %+v err=%v", list, err)
	}
	got, err := s.GetEndpoint(t.Context(), "ep-legacy", "ws-legacy")
	if err != nil || !got.LegacySigning {
		t.Fatalf("GetEndpoint must report legacy_signing, got %+v err=%v", got, err)
	}

	d := newOutboxDispatcher(s)
	if _, err := d.DeliverTest(t.Context(), "ep-legacy", "ws-legacy", nil); err != nil {
		t.Fatal(err)
	}
	sig, body := rcv.last(t)
	if !verify(hashSecret(secret), body, sig) {
		t.Fatal("legacy endpoint changed signing scheme without a rotation")
	}

	rotated, err := s.RotateSecret(t.Context(), "ep-legacy", "ws-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Secret == "" || rotated.Secret == secret || rotated.LegacySigning {
		t.Fatalf("rotation must return a fresh secret and clear legacy_signing, got %+v", rotated)
	}
	if _, err := d.DeliverTest(t.Context(), "ep-legacy", "ws-legacy", nil); err != nil {
		t.Fatal(err)
	}
	sig, body = rcv.last(t)
	if !verify(rotated.Secret, body, sig) {
		t.Fatal("after rotation the signature must verify with the new secret")
	}
}

func TestRotateSecret_OtherWorkspaceIsNotFound(t *testing.T) {
	s := newInternalStore(t)
	ep, err := s.CreateEndpoint(t.Context(), "ws-a", "https://example.com", []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateSecret(t.Context(), ep.ID, "ws-b"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rotating another workspace's endpoint: want sql.ErrNoRows, got %v", err)
	}
	got, err := s.GetEndpoint(t.Context(), ep.ID, "ws-a")
	if err != nil || got.SecretEncrypted != ep.SecretEncrypted {
		t.Fatalf("secret changed by a foreign rotation: err=%v", err)
	}
}

func TestDelivery_UndecryptableSecretIsNotSent(t *testing.T) {
	rcv := newCaptureServer(t)
	s := newInternalStore(t)
	ep, err := s.CreateEndpoint(t.Context(), "ws-bad", rcv.URL, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE webhook_endpoints SET secret_encrypted = 'v2:00000000:AAAA' WHERE id = $1`, ep.ID); err != nil {
		t.Fatal(err)
	}
	d := newOutboxDispatcher(s)
	res, err := d.DeliverTest(t.Context(), ep.ID, "ws-bad", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res["success"] != false {
		t.Fatalf("delivery with an unreadable secret must fail, got %v", res)
	}
	rcv.mu.Lock()
	n := len(rcv.sigs)
	rcv.mu.Unlock()
	if n != 0 {
		t.Fatalf("receiver got %d requests; an unsigned or mis-signed delivery must not go out", n)
	}
}
