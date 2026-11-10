package githubapp

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKeyPEM(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	return string(pem.EncodeToMemory(block)), &key.PublicKey
}

func envService(t *testing.T, apiBase string, keyPEM string) *Service {
	t.Helper()
	t.Setenv("OCTOPUS_JWT_SECRET", "githubapp-test-master-secret")
	t.Setenv(envAppID, "123456")
	t.Setenv(envSlug, "octopus-test")
	t.Setenv(envClientID, "Iv23liTEST")
	t.Setenv(envClientSecret, "client-secret")
	t.Setenv(envPrivateKey, keyPEM)
	t.Setenv(envWebhookSecret, "hook-secret")
	svc := NewService(nil)
	if apiBase != "" {
		svc.apiBase = apiBase
		svc.oauthBase = apiBase
	}
	return svc
}

func TestAppJWTShapeAndSignature(t *testing.T) {
	keyPEM, pub := testKeyPEM(t)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	tok, err := appJWT(123456, keyPEM, now)
	if err != nil {
		t.Fatalf("appJWT: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(parts))
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.Iss != "123456" {
		t.Errorf("iss = %q, want 123456", claims.Iss)
	}
	if claims.Iat != now.Add(-60*time.Second).Unix() {
		t.Errorf("iat = %d, want backdated 60s", claims.Iat)
	}
	if claims.Exp-claims.Iat > 600 {
		t.Errorf("jwt lifetime %ds exceeds GitHub's 10-minute cap", claims.Exp-claims.Iat)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("signature does not verify: %v", err)
	}
}

func TestParseRSAPrivateKeyPKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := parseRSAPrivateKey(pemStr); err != nil {
		t.Errorf("PKCS#8 key rejected: %v", err)
	}
	if _, err := parseRSAPrivateKey("not a pem"); err == nil {
		t.Error("garbage accepted as private key")
	}
}

func TestInstallationTokenMintAndCache(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	var mints int
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("want Bearer app JWT, got %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Repositories []string `json:"repositories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mints++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_test%d","expires_at":%q}`, mints, time.Now().Add(time.Hour).Format(time.RFC3339))
	}))
	defer fake.Close()

	svc := envService(t, fake.URL, keyPEM)
	tok1, err := svc.InstallationTokenForRepo(t.Context(), 42, "octopus")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tok2, err := svc.InstallationTokenForRepo(t.Context(), 42, "octopus")
	if err != nil {
		t.Fatalf("cached mint: %v", err)
	}
	if tok1 != tok2 || mints != 1 {
		t.Errorf("expected cache hit: tok1=%q tok2=%q mints=%d", tok1, tok2, mints)
	}
	if _, err := svc.InstallationTokenForRepo(t.Context(), 42, "other"); err != nil {
		t.Fatalf("second repo mint: %v", err)
	}
	if mints != 2 {
		t.Errorf("down-scoped tokens must not share cache entries (mints=%d)", mints)
	}
	svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := svc.InstallationTokenForRepo(t.Context(), 42, "octopus"); err != nil {
		t.Fatalf("re-mint after expiry: %v", err)
	}
	if mints != 3 {
		t.Errorf("expired token not refreshed (mints=%d)", mints)
	}
}

func TestValidateConfigChecksAppIdentity(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"id":999,"slug":"someone-elses-app","name":"Nope"}`)
	}))
	defer fake.Close()
	svc := envService(t, fake.URL, keyPEM)
	_, err := svc.ValidateConfig(t.Context(), AppConfig{AppID: 123456, PrivateKey: keyPEM})
	if err == nil || !strings.Contains(err.Error(), "belongs to app 999") {
		t.Errorf("mismatched app id accepted: %v", err)
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, "", keyPEM)
	payload := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, []byte("hook-secret"))
	mac.Write(payload)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !svc.VerifyWebhookSignature(t.Context(), payload, good) {
		t.Error("valid signature rejected")
	}
	if svc.VerifyWebhookSignature(t.Context(), payload, "sha256=deadbeef") {
		t.Error("bad signature accepted")
	}
	if svc.VerifyWebhookSignature(t.Context(), payload, "") {
		t.Error("missing signature accepted")
	}
	if svc.VerifyWebhookSignature(t.Context(), []byte(`tampered`), good) {
		t.Error("tampered payload accepted")
	}
}

func TestInstallURLWithState(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, "", keyPEM)
	link, err := svc.InstallURLWithState(t.Context(), "some+state/value")
	if err != nil {
		t.Fatalf("install url: %v", err)
	}
	if !strings.HasPrefix(link, "https://github.com/apps/octopus-test/installations/new?state=") {
		t.Errorf("unexpected install url: %s", link)
	}
	if strings.Contains(link, "some+state/value") {
		t.Errorf("state not query-escaped: %s", link)
	}
}

func TestStatusNeverLeaksKeyMaterial(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	svc := envService(t, "", keyPEM)
	raw, err := json.Marshal(svc.Status(t.Context()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, needle := range []string{"PRIVATE KEY", "client-secret", "hook-secret"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("status leaks %q: %s", needle, raw)
		}
	}
	if !svc.Configured(t.Context()) {
		t.Error("env-configured service reports unconfigured")
	}
}
