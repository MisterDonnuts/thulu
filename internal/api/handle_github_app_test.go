package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGithubAppStatusUnconfigured(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/github/app", nil))
	if w.Code != 200 {
		t.Fatalf("GET /github/app = %d: %s", w.Code, w.Body.String())
	}
	var st struct {
		Configured bool `json:"configured"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.Configured {
		t.Error("fresh deployment reports a configured app")
	}
}

func TestGithubWebhookRejectsUnsigned(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	body := `{"action":"deleted","installation":{"id":42}}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body)))
	if w.Code != 401 {
		t.Errorf("unsigned webhook = %d, want 401", w.Code)
	}
	mac := hmac.New(sha256.New, []byte("attacker-guess"))
	mac.Write([]byte(body))
	req := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", "installation")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("forged webhook = %d, want 401", w.Code)
	}
}

func TestGithubSetupUnconfigured(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/github/setup?installation_id=1&setup_action=install", nil))
	if w.Code != 503 {
		t.Errorf("setup without app = %d, want 503", w.Code)
	}
}

func TestGithubSetupRequestFlow(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	t.Setenv("OCTOPUS_GITHUB_APP_ID", "123456")
	t.Setenv("OCTOPUS_GITHUB_APP_SLUG", "test-app")
	t.Setenv("OCTOPUS_GITHUB_APP_PRIVATE_KEY", pemStr)
	_, h := newIntegrationServer(t)

	for _, path := range []string{
		"/github/setup?setup_action=request&state=whatever",
		"/github/oauth/callback?setup_action=request",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 302 {
			t.Errorf("%s = %d, want 302", path, w.Code)
			continue
		}
		if loc := w.Header().Get("Location"); loc != "/projects/?github=requested" {
			t.Errorf("%s redirected to %q, want /projects/?github=requested", path, loc)
		}
	}
}

func TestCreateProjectRejectsTokenAndInstallation(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	body := `{"name":"p1","repo_url":"https://github.com/o/r","github_token":"ghp_x","github_installation_id":5}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "mutually exclusive") {
		t.Errorf("token+installation create = %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateProjectUnknownInstallation(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	body := `{"name":"p2","repo_url":"https://github.com/o/r","github_installation_id":424242}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("unknown installation create = %d: %s", w.Code, w.Body.String())
	}
}

func TestListInstallationsEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/github/installations", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("empty installations list = %d: %q", w.Code, w.Body.String())
	}
}
