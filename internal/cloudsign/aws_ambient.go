package cloudsign

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Sources are tried in the AWS SDKs' order. Shared config files and SSO profiles are deliberately
// not read.

// The credential check reads the same variable.
const EnvAWSSTSEndpoint = "OCTOPUS_AWS_STS_ENDPOINT"

var (
	imdsEndpoint                  = "http://169.254.169.254"
	containerRelativeHost         = "http://169.254.170.2"
	errNoAmbientAWSIdentity error = apperr.Unavailable("cloudsign.errors.no_aws_identity")
)

var (
	ambientAWSMu    sync.Mutex
	ambientAWSCache AWSCreds
)

func AmbientAWS(ctx context.Context) (AWSCreds, error) {
	ambientAWSMu.Lock()
	defer ambientAWSMu.Unlock()
	now := time.Now()
	if c := ambientAWSCache; c.AccessKeyID != "" && (c.Expires.IsZero() || now.Add(tokenMargin).Before(c.Expires)) {
		return c, nil
	}
	c, err := ambientAWS(ctx, os.Getenv)
	if err != nil {
		return AWSCreds{}, err
	}
	ambientAWSCache = c
	return c, nil
}

func ambientAWS(ctx context.Context, getenv func(string) string) (AWSCreds, error) {
	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	if id, secret := env("AWS_ACCESS_KEY_ID"), env("AWS_SECRET_ACCESS_KEY"); id != "" && secret != "" {
		return AWSCreds{AccessKeyID: id, SecretAccessKey: secret, SessionToken: env("AWS_SESSION_TOKEN")}, nil
	}
	if file, role := env("AWS_WEB_IDENTITY_TOKEN_FILE"), env("AWS_ROLE_ARN"); file != "" && role != "" {
		return webIdentityCreds(ctx, getenv, file, role)
	}
	if full, rel := env("AWS_CONTAINER_CREDENTIALS_FULL_URI"), env("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); full != "" || rel != "" {
		return containerCreds(ctx, getenv, full, rel)
	}
	if strings.EqualFold(env("AWS_EC2_METADATA_DISABLED"), "true") {
		return AWSCreds{}, errNoAmbientAWSIdentity
	}
	c, err := imdsCreds(ctx, getenv)
	if err != nil {
		return AWSCreds{}, apperr.WrapKind(apperr.KindUnavailable,
			apperr.Wrap(err, "cloudsign.errors.instance_metadata"), "cloudsign.errors.no_aws_identity")
	}
	return c, nil
}

// AssumeRoleWithWebIdentity is unsigned by design.
func webIdentityCreds(ctx context.Context, getenv func(string) string, tokenFile, roleARN string) (AWSCreds, error) {
	token, err := os.ReadFile(tokenFile) //nolint:gosec // G304: the path EKS injects into the daemon's own pod
	if err != nil {
		return AWSCreds{}, apperr.Wrap(err, "cloudsign.errors.read_env_file", "env", "AWS_WEB_IDENTITY_TOKEN_FILE")
	}
	def := "https://sts.amazonaws.com/"
	if region := strings.TrimSpace(getenv("AWS_REGION")); ValidRegion(region) {
		def = "https://sts." + region + ".amazonaws.com/"
	}
	endpoint, err := Endpoint(getenv, EnvAWSSTSEndpoint, def)
	if err != nil {
		return AWSCreds{}, err
	}
	session := strings.TrimSpace(getenv("AWS_ROLE_SESSION_NAME"))
	if session == "" {
		session = "octopus-daemon"
	}
	form := url.Values{
		//i18n:ignore STS query parameter names, not messages
		"Action":           {"AssumeRoleWithWebIdentity"},
		"Version":          {"2011-06-15"}, //i18n:ignore STS query parameter name
		"RoleArn":          {roleARN},
		"RoleSessionName":  {session},
		"WebIdentityToken": {strings.TrimSpace(string(token))},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return AWSCreds{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8") //i18n:ignore HTTP header value
	req.Header.Set("Accept", "application/xml")
	body, err := doLimited(httpClient, req)
	if err != nil {
		return AWSCreds{}, apperr.Wrap(err, "cloudsign.errors.sts_assume_role")
	}
	var doc struct {
		Result struct {
			Credentials struct {
				AccessKeyID     string    `xml:"AccessKeyId"`
				SecretAccessKey string    `xml:"SecretAccessKey"`
				SessionToken    string    `xml:"SessionToken"`
				Expiration      time.Time `xml:"Expiration"`
			} `xml:"Credentials"`
		} `xml:"AssumeRoleWithWebIdentityResult"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return AWSCreds{}, apperr.Wrap(err, "cloudsign.errors.decode_assume_role")
	}
	c := doc.Result.Credentials
	return complete(AWSCreds{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expires: c.Expiration})
}

func containerCreds(ctx context.Context, getenv func(string) string, full, rel string) (AWSCreds, error) {
	endpoint := full
	if endpoint == "" {
		endpoint = containerRelativeHost + rel
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return AWSCreds{}, err
	}
	auth := strings.TrimSpace(getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN"))
	if file := strings.TrimSpace(getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")); file != "" {
		b, rerr := os.ReadFile(file) //nolint:gosec // G304: the path EKS injects into the daemon's own pod
		if rerr != nil {
			return AWSCreds{}, apperr.Wrap(rerr, "cloudsign.errors.read_env_file", "env", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE")
		}
		auth = strings.TrimSpace(string(b))
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	body, err := doLimited(metadataClient, req)
	if err != nil {
		return AWSCreds{}, apperr.Wrap(err, "cloudsign.errors.container_credentials")
	}
	return decodeJSONCreds(body)
}

func imdsCreds(ctx context.Context, getenv func(string) string) (AWSCreds, error) {
	base := imdsEndpoint
	if v := strings.TrimSpace(getenv("AWS_EC2_METADATA_SERVICE_ENDPOINT")); v != "" {
		base = strings.TrimRight(v, "/")
	}
	tokReq, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/latest/api/token", http.NoBody)
	if err != nil {
		return AWSCreds{}, err
	}
	tokReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	tok, err := doLimited(metadataClient, tokReq)
	if err != nil {
		return AWSCreds{}, err
	}
	get := func(path string) ([]byte, error) {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, base+path, http.NoBody)
		if rerr != nil {
			return nil, rerr
		}
		req.Header.Set("X-aws-ec2-metadata-token", strings.TrimSpace(string(tok)))
		return doLimited(metadataClient, req)
	}
	const rolesPath = "/latest/meta-data/iam/security-credentials/"
	roles, err := get(rolesPath)
	if err != nil {
		return AWSCreds{}, err
	}
	role, _, _ := strings.Cut(strings.TrimSpace(string(roles)), "\n")
	if role == "" {
		return AWSCreds{}, apperr.Unavailable("cloudsign.errors.instance_no_role")
	}
	body, err := get(rolesPath + url.PathEscape(strings.TrimSpace(role)))
	if err != nil {
		return AWSCreds{}, err
	}
	return decodeJSONCreds(body)
}

func decodeJSONCreds(body []byte) (AWSCreds, error) {
	var doc struct {
		AccessKeyID     string    `json:"AccessKeyId"`
		SecretAccessKey string    `json:"SecretAccessKey"`
		Token           string    `json:"Token"`
		Expiration      time.Time `json:"Expiration"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return AWSCreds{}, apperr.Wrap(err, "cloudsign.errors.decode_credentials")
	}
	return complete(AWSCreds{AccessKeyID: doc.AccessKeyID, SecretAccessKey: doc.SecretAccessKey, SessionToken: doc.Token, Expires: doc.Expiration})
}

func complete(c AWSCreds) (AWSCreds, error) {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return AWSCreds{}, apperr.Unavailable("cloudsign.errors.no_keys_returned")
	}
	return c, nil
}

func doLimited(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.Unavailable("cloudsign.errors.http_status", "status", resp.StatusCode)
	}
	return body, nil
}

// A region ends up in a hostname, so anything else is refused rather than escaped.
func ValidRegion(r string) bool {
	if r == "" || len(r) > 32 {
		return false
	}
	for i := 0; i < len(r); i++ {
		c := r[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return r[0] != '-' && r[len(r)-1] != '-'
}
