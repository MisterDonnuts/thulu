package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubLoginFollowsEnterpriseServer(t *testing.T) {
	var paths []string
	ghes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v3/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "octo", "name": "Octo Cat"})
		case "/api/v3/user/emails":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"email": "octo@corp.example", "primary": true, "verified": true}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghes.Close()

	t.Setenv("OCTOPUS_GITHUB_URL", ghes.URL)
	t.Setenv("OCTOPUS_GITHUB_API_URL", "")
	t.Setenv("OCTOPUS_OIDC_ISSUER", "")
	t.Setenv("OCTOPUS_GITHUB_CLIENT_ID", "cid")
	t.Setenv("OCTOPUS_GITHUB_CLIENT_SECRET", "csecret")
	t.Setenv("OCTOPUS_SSO_ALLOWED_DOMAINS", "")

	providers, err := LoadSSOProviders(t.Context())
	if err != nil {
		t.Fatalf("LoadSSOProviders: %v", err)
	}
	ep := providers[ssoKindGitHub].oauth2Config.Endpoint
	if ep.AuthURL != ghes.URL+"/login/oauth/authorize" || ep.TokenURL != ghes.URL+"/login/oauth/access_token" {
		t.Errorf("endpoints = %+v, want the enterprise server's", ep)
	}

	id, err := extractGitHubUser(t.Context(), "tok")
	if err != nil {
		t.Fatalf("extractGitHubUser: %v (requests: %v)", err, paths)
	}
	if id.Email != "octo@corp.example" || id.Sub != "github:7" || id.Name != "Octo Cat" {
		t.Errorf("identity = %+v", id)
	}
}
