package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	oidclib "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func discoveryServer(t *testing.T, endSession string) *oidclib.Provider {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		doc := map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/oauth/token",
			"jwks_uri":               srv.URL + "/keys",
		}
		if endSession != "" {
			if endSession == "{issuer}" {
				doc["end_session_endpoint"] = srv.URL + "/end_session"
			} else {
				doc["end_session_endpoint"] = endSession
			}
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	p, err := oidclib.NewProvider(t.Context(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscoverEndSession(t *testing.T) {
	if got := discoverEndSession(discoveryServer(t, "")); got != "" {
		t.Errorf("no end_session_endpoint: got %q", got)
	}
	if got := discoverEndSession(discoveryServer(t, "javascript:alert(1)")); got != "" {
		t.Errorf("non-http endpoint accepted: %q", got)
	}
	p := discoveryServer(t, "{issuer}")
	if got := discoverEndSession(p); got == "" {
		t.Error("end_session_endpoint not picked up")
	}
}

func TestLogout_ReturnsIdPLogoutURL(t *testing.T) {
	t.Setenv("OCTOPUS_FRONTEND_ORIGIN", "https://octopus.example")
	t.Setenv("OCTOPUS_OIDC_POST_LOGOUT_REDIRECT_URI", "")
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	t.Setenv("OCTOPUS_OIDC_IDP_LOGOUT", "")
	p := discoveryServer(t, "{issuer}")
	h := NewHandler(newSSOTestStore(t)).WithSSO(map[string]*ssoProvider{
		ssoKindOIDC: {
			info:          SSOProviderInfo{Key: ssoKindOIDC, Kind: ssoKindOIDC},
			oidcProvider:  p,
			endSessionURL: discoverEndSession(p),
			oauth2Config:  oauth2.Config{ClientID: "octopus-client"},
		},
	})

	logout := func() map[string]string {
		t.Helper()
		w := httptest.NewRecorder()
		h.handleLogout(w, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("logout: %d", w.Code)
		}
		var out map[string]string
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	got, err := url.Parse(logout()["logout_url"])
	if err != nil || got.Path != "/end_session" {
		t.Fatalf("logout_url = %v (err %v), want the provider's end_session", got, err)
	}
	q := got.Query()
	if q.Get("client_id") != "octopus-client" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("post_logout_redirect_uri") != "https://octopus.example/login/" {
		t.Errorf("post_logout_redirect_uri = %q", q.Get("post_logout_redirect_uri"))
	}
	if q.Has("id_token_hint") {
		t.Error("id_token_hint sent: Thulu keeps no ID tokens")
	}

	t.Setenv("OCTOPUS_OIDC_POST_LOGOUT_REDIRECT_URI", "https://octopus.example/bye")
	got, _ = url.Parse(logout()["logout_url"])
	if got.Query().Get("post_logout_redirect_uri") != "https://octopus.example/bye" {
		t.Errorf("override ignored: %q", got.Query().Get("post_logout_redirect_uri"))
	}

	t.Setenv("OCTOPUS_SAAS_MODE", "")
	if u := logout()["logout_url"]; u != "" {
		t.Errorf("self-hosted default: logout_url = %q, want none", u)
	}
	t.Setenv("OCTOPUS_OIDC_IDP_LOGOUT", "1")
	if u := logout()["logout_url"]; u == "" {
		t.Error("OCTOPUS_OIDC_IDP_LOGOUT=1 ignored")
	}
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	t.Setenv("OCTOPUS_OIDC_IDP_LOGOUT", "0")
	if u := logout()["logout_url"]; u != "" {
		t.Errorf("OCTOPUS_OIDC_IDP_LOGOUT=0: logout_url = %q", u)
	}
}
