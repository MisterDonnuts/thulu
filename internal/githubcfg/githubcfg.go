package githubcfg

import (
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const (
	EnvWebURL = "OCTOPUS_GITHUB_URL"
	EnvAPIURL = "OCTOPUS_GITHUB_API_URL"
)

const (
	DefaultWebURL = "https://github.com"
	DefaultAPIURL = "https://api.github.com"
)

func WebURL() string { return webURL(os.Getenv) }

func webURL(getenv func(string) string) string {
	if v, ok := parseBase(getenv, EnvWebURL); ok {
		return v
	}
	return DefaultWebURL
}

func APIURL() string { return apiURL(os.Getenv) }

func apiURL(getenv func(string) string) string {
	if v, ok := parseBase(getenv, EnvAPIURL); ok {
		return v
	}
	if web := webURL(getenv); !strings.EqualFold(web, DefaultWebURL) {
		return web + "/api/v3"
	}
	return DefaultAPIURL
}

func Host() string {
	u, err := url.Parse(WebURL())
	if err != nil {
		return "github.com"
	}
	return u.Host
}

func IsDotCom() bool {
	return strings.EqualFold(WebURL(), DefaultWebURL)
}

func OAuthAuthorizeURL() string { return WebURL() + "/login/oauth/authorize" }

func OAuthTokenURL() string { return WebURL() + "/login/oauth/access_token" }

// GHES serves Apps under /github-apps/, github.com under /apps/.
func AppInstallURL(slug string) string {
	prefix := "/apps/"
	if !IsDotCom() {
		prefix = "/github-apps/"
	}
	return WebURL() + prefix + url.PathEscape(slug) + "/installations/new"
}

func RepoHTTPSURL(owner, repo string) string {
	return WebURL() + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func IsGitHubHost(host string) bool {
	return strings.EqualFold(host, Host())
}

// GITHUB_API_URL and GITHUB_SERVER_URL are the names thulu-github reads.
func Env() map[string]string { return EnvFrom(os.Getenv) }

func EnvFrom(getenv func(string) string) map[string]string {
	return map[string]string{
		"GITHUB_API_URL":    apiURL(getenv),
		"GITHUB_SERVER_URL": webURL(getenv),
	}
}

func parseBase(getenv func(string) string, key string) (string, bool) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		// Keep the value out of the log: it may carry a credential.
		log.Printf("[githubcfg] %s", i18n.Log("githubcfg.log.invalid_base_url", "name", key))
		return "", false
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), true
}
