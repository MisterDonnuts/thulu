package githubcfg

import "testing"

func TestDefaultsAreGitHubDotCom(t *testing.T) {
	t.Setenv(EnvWebURL, "")
	t.Setenv(EnvAPIURL, "")
	if got := WebURL(); got != "https://github.com" {
		t.Errorf("WebURL = %q", got)
	}
	if got := APIURL(); got != "https://api.github.com" {
		t.Errorf("APIURL = %q", got)
	}
	if got := Host(); got != "github.com" {
		t.Errorf("Host = %q", got)
	}
	if !IsDotCom() {
		t.Error("IsDotCom = false")
	}
	if got := AppInstallURL("my app"); got != "https://github.com/apps/my%20app/installations/new" {
		t.Errorf("AppInstallURL = %q", got)
	}
}

func TestEnterpriseServerDerivesAPI(t *testing.T) {
	t.Setenv(EnvWebURL, "https://ghe.example.com/")
	t.Setenv(EnvAPIURL, "")
	if got := WebURL(); got != "https://ghe.example.com" {
		t.Errorf("WebURL = %q", got)
	}
	if got := APIURL(); got != "https://ghe.example.com/api/v3" {
		t.Errorf("APIURL = %q", got)
	}
	if !IsGitHubHost("GHE.example.com") || IsGitHubHost("github.com") {
		t.Error("IsGitHubHost mismatch")
	}
	if got := AppInstallURL("octo"); got != "https://ghe.example.com/github-apps/octo/installations/new" {
		t.Errorf("AppInstallURL = %q", got)
	}
	if got := OAuthTokenURL(); got != "https://ghe.example.com/login/oauth/access_token" {
		t.Errorf("OAuthTokenURL = %q", got)
	}
	if got := Env()["GITHUB_API_URL"]; got != "https://ghe.example.com/api/v3" {
		t.Errorf("Env GITHUB_API_URL = %q", got)
	}
}

func TestExplicitAPIWins(t *testing.T) {
	t.Setenv(EnvWebURL, "https://ghe.example.com")
	t.Setenv(EnvAPIURL, "https://api.ghe.example.com")
	if got := APIURL(); got != "https://api.ghe.example.com" {
		t.Errorf("APIURL = %q", got)
	}
}

func TestInvalidValuesFallBack(t *testing.T) {
	for _, bad := range []string{
		"ghe.example.com",
		"ftp://ghe.example.com",
		"https://user:tok@ghe.example.com",
		"https://ghe.example.com/?x=1",
		"file:///etc",
	} {
		t.Setenv(EnvWebURL, bad)
		t.Setenv(EnvAPIURL, bad)
		if got := WebURL(); got != DefaultWebURL {
			t.Errorf("WebURL(%q) = %q, want default", bad, got)
		}
		if got := APIURL(); got != DefaultAPIURL {
			t.Errorf("APIURL(%q) = %q, want default", bad, got)
		}
	}
}
