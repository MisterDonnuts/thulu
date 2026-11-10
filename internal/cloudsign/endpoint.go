package cloudsign

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

var httpClient = &http.Client{Timeout: 20 * time.Second}

// Link-local metadata services answer only from the host: never proxy them, and keep the timeout
// short so a daemon off that cloud does not hang.
var metadataClient = &http.Client{
	Timeout:   3 * time.Second,
	Transport: &http.Transport{Proxy: nil},
}

// A non-https override is an error, not a fallback to the public endpoint: an install that sets one
// either cannot or must not reach it.
func Endpoint(getenv func(string) string, env, def string) (string, error) {
	raw := strings.TrimSpace(getenv(env))
	if raw == "" {
		return def, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", apperr.Invalid("cloudsign.errors.endpoint_not_https", "env", env)
	}
	return raw, nil
}
