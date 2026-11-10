package cloudsign

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	// Vertex AI accepts nothing narrower.
	GCPScope = "https://www.googleapis.com/auth/cloud-platform"
	// Google checks the audience against its own endpoint, even when EnvGCPOAuth2Endpoint routes
	// the request elsewhere.
	GCPTokenAudience = "https://oauth2.googleapis.com/token" //nolint:gosec // G101: a public OAuth endpoint, not a credential

	// The credential check reads the same variable.
	EnvGCPOAuth2Endpoint = "OCTOPUS_GCP_OAUTH2_ENDPOINT"
)

// token_uri is deliberately not read: honoring it would let a stored credential send the daemon's
// outbound call anywhere.
type GCPServiceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

func ParseGCPServiceAccount(raw string) (GCPServiceAccount, error) {
	var sa GCPServiceAccount
	if err := json.Unmarshal([]byte(raw), &sa); err != nil {
		return sa, apperr.Invalid("cloudsign.errors.sa_key_not_json")
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return sa, apperr.Invalid("cloudsign.errors.sa_key_incomplete")
	}
	return sa, nil
}

func GCPAssertion(clientEmail, privateKeyPEM string, now time.Time) (string, error) {
	key, err := parseRSAKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   clientEmail,
		"scope": GCPScope,
		"aud":   GCPTokenAudience,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64url(headerJSON) + "." + b64url(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", apperr.Wrap(err, "cloudsign.errors.sign_assertion")
	}
	return signingInput + "." + b64url(sig), nil
}

func parseRSAKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, apperr.Invalid("cloudsign.errors.private_key_not_pem")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, apperr.Invalid("cloudsign.errors.private_key_not_rsa")
		}
		return rsaKey, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindInvalid, err, "cloudsign.errors.parse_private_key")
	}
	return key, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

const tokenMargin = 5 * time.Minute

type cachedToken struct {
	mu      sync.Mutex
	token   string
	expires time.Time
}

var (
	gcpTokensMu sync.Mutex
	gcpTokens   = map[string]*cachedToken{}
)

func (c *cachedToken) get(now time.Time, fetch func() (string, time.Time, error)) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && now.Add(tokenMargin).Before(c.expires) {
		return c.token, nil
	}
	tok, exp, err := fetch()
	if err != nil {
		return "", err
	}
	c.token, c.expires = tok, exp
	return tok, nil
}

func tokenEntry(key string) *cachedToken {
	gcpTokensMu.Lock()
	defer gcpTokensMu.Unlock()
	e, ok := gcpTokens[key]
	if !ok {
		e = &cachedToken{}
		gcpTokens[key] = e
	}
	return e
}

func GCPServiceAccountToken(ctx context.Context, keyJSON string) (string, error) {
	sa, err := ParseGCPServiceAccount(keyJSON)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(keyJSON))
	return tokenEntry("sa:"+b64url(sum[:])).get(time.Now(), func() (string, time.Time, error) {
		return exchangeJWT(ctx, sa, time.Now())
	})
}

func exchangeJWT(ctx context.Context, sa GCPServiceAccount, now time.Time) (string, time.Time, error) {
	assertion, err := GCPAssertion(sa.ClientEmail, sa.PrivateKey, now)
	if err != nil {
		return "", time.Time{}, err
	}
	endpoint, err := Endpoint(os.Getenv, EnvGCPOAuth2Endpoint, GCPTokenAudience)
	if err != nil {
		return "", time.Time{}, err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, //i18n:ignore OAuth grant type URN
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return readGCPToken(httpClient, req, now)
}

func metadataTokenURL(getenv func(string) string) string {
	host := strings.TrimSpace(getenv("GCE_METADATA_HOST"))
	if host == "" {
		host = "metadata.google.internal"
	}
	return "http://" + host + "/computeMetadata/v1/instance/service-accounts/default/token"
}

func AmbientGCPToken(ctx context.Context) (string, error) {
	if path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")); path != "" {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: the operator's own credential path
		if err != nil {
			return "", apperr.Wrap(err, "cloudsign.errors.read_env_file", "env", "GOOGLE_APPLICATION_CREDENTIALS")
		}
		return GCPServiceAccountToken(ctx, string(raw))
	}
	endpoint := metadataTokenURL(os.Getenv)
	return tokenEntry("metadata:"+endpoint).get(time.Now(), func() (string, time.Time, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
		if err != nil {
			return "", time.Time{}, err
		}
		req.Header.Set("Metadata-Flavor", "Google") //i18n:ignore HTTP header value the metadata server requires
		tok, exp, err := readGCPToken(metadataClient, req, time.Now())
		if err != nil {
			return "", time.Time{}, apperr.WrapKind(apperr.KindUnavailable, err, "cloudsign.errors.no_google_identity")
		}
		return tok, exp, nil
	})
}

func readGCPToken(client *http.Client, req *http.Request, now time.Time) (string, time.Time, error) {
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", time.Time{}, apperr.Wrap(err, "cloudsign.errors.read_token_response")
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	decodeErr := json.Unmarshal(body, &tok)
	if resp.StatusCode != http.StatusOK {
		if tok.Error != "" {
			return "", time.Time{}, apperr.Unavailable("cloudsign.errors.google_token_error", "error", tok.Error, "description", tok.Description)
		}
		return "", time.Time{}, apperr.Unavailable("cloudsign.errors.google_token_status", "status", resp.StatusCode)
	}
	if decodeErr != nil {
		return "", time.Time{}, apperr.Wrap(decodeErr, "cloudsign.errors.decode_token_response")
	}
	if tok.AccessToken == "" {
		return "", time.Time{}, apperr.Unavailable("cloudsign.errors.google_no_access_token")
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600
	}
	return tok.AccessToken, now.Add(time.Duration(tok.ExpiresIn) * time.Second), nil
}
