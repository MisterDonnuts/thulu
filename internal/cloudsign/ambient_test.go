package cloudsign

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestAmbientAWS_EnvironmentKeysComeFirst(t *testing.T) {
	c, err := ambientAWS(context.Background(), envMap(map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIAENV", "AWS_SECRET_ACCESS_KEY": "s", "AWS_SESSION_TOKEN": "t",
		"AWS_WEB_IDENTITY_TOKEN_FILE": "/nope", "AWS_ROLE_ARN": "arn:aws:iam::1:role/r",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "AKIAENV" || c.SessionToken != "t" {
		t.Errorf("creds = %+v", c)
	}
}

func TestAmbientAWS_WebIdentity(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("jwt-from-eks\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var form map[string][]string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("AssumeRoleWithWebIdentity was signed")
		}
		_ = r.ParseForm()
		form = r.PostForm
		fmt.Fprint(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials>
			<AccessKeyId>ASIAIRSA</AccessKeyId><SecretAccessKey>sk</SecretAccessKey>
			<SessionToken>st</SessionToken><Expiration>2030-01-01T00:00:00Z</Expiration>
			</Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`)
	}))
	defer srv.Close()
	old := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() { httpClient = old })

	c, err := ambientAWS(context.Background(), envMap(map[string]string{
		"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
		"AWS_ROLE_ARN":                "arn:aws:iam::1:role/octopus",
		EnvAWSSTSEndpoint:             srv.URL + "/",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "ASIAIRSA" || c.SessionToken != "st" || c.Expires.Year() != 2030 {
		t.Errorf("creds = %+v", c)
	}
	if got := form["WebIdentityToken"]; len(got) != 1 || got[0] != "jwt-from-eks" {
		t.Errorf("WebIdentityToken = %v", got)
	}
	if got := form["RoleArn"]; len(got) != 1 || got[0] != "arn:aws:iam::1:role/octopus" {
		t.Errorf("RoleArn = %v", got)
	}
}

func TestAmbientAWS_ContainerCredentials(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth")
	if err := os.WriteFile(authFile, []byte("pod-identity-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "pod-identity-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"AccessKeyId":"ASIAPOD","SecretAccessKey":"sk","Token":"st","Expiration":"2030-01-01T00:00:00Z"}`)
	}))
	defer srv.Close()
	c, err := ambientAWS(context.Background(), envMap(map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     srv.URL + "/v1/credentials",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": authFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "ASIAPOD" || c.SessionToken != "st" {
		t.Errorf("creds = %+v", c)
	}
}

func TestAmbientAWS_InstanceRoleUsesIMDSv2(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /latest/api/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "imds-session")
	})
	mux.HandleFunc("GET /latest/meta-data/iam/security-credentials/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-aws-ec2-metadata-token") != "imds-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, "octopus-node\n")
	})
	mux.HandleFunc("GET /latest/meta-data/iam/security-credentials/octopus-node", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-aws-ec2-metadata-token") != "imds-session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"AccessKeyId":"ASIAEC2","SecretAccessKey":"sk","Token":"st","Expiration":"2030-01-01T00:00:00Z"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := ambientAWS(context.Background(), envMap(map[string]string{"AWS_EC2_METADATA_SERVICE_ENDPOINT": srv.URL}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "ASIAEC2" {
		t.Errorf("creds = %+v", c)
	}
}

func TestAmbientAWS_NothingConfigured(t *testing.T) {
	_, err := ambientAWS(context.Background(), envMap(map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}))
	if err == nil || !strings.Contains(err.Error(), "no AWS identity") {
		t.Errorf("err = %v", err)
	}
}

func TestValidRegion(t *testing.T) {
	for _, ok := range []string{"us-east-1", "europe-west4", "global", "us"} {
		if !ValidRegion(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "us-east-1.evil.com", "US-EAST-1", "a/b", "-x", "x-", "us east"} {
		if ValidRegion(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func testKeyFile(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "octopus@p.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGCPServiceAccountToken_CachesPerKey(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.PostForm.Get("assertion") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"access_token":"ya29.token-%d","expires_in":3600}`, n)
	}))
	defer srv.Close()
	old := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() { httpClient = old })
	t.Setenv(EnvGCPOAuth2Endpoint, srv.URL+"/token")

	keyA, keyB := testKeyFile(t), testKeyFile(t)
	ctx := context.Background()
	a1, err := GCPServiceAccountToken(ctx, keyA)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := GCPServiceAccountToken(ctx, keyA)
	b1, _ := GCPServiceAccountToken(ctx, keyB)
	if a1 != a2 {
		t.Errorf("token not reused: %s then %s", a1, a2)
	}
	if b1 == a1 {
		t.Error("a second key got the first key's token")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("token endpoint called %d times, want 2", got)
	}
}

func TestCachedToken_RefreshesNearExpiry(t *testing.T) {
	var c cachedToken
	now := time.Now()
	fetches := 0
	fetch := func() (string, time.Time, error) {
		fetches++
		return fmt.Sprintf("t%d", fetches), now.Add(10 * time.Minute), nil
	}
	if tok, _ := c.get(now, fetch); tok != "t1" {
		t.Fatalf("first = %s", tok)
	}
	if tok, _ := c.get(now.Add(time.Minute), fetch); tok != "t1" {
		t.Errorf("fresh token replaced: %s", tok)
	}
	if tok, _ := c.get(now.Add(6*time.Minute), fetch); tok != "t2" {
		t.Errorf("token inside the margin reused: %s", tok)
	}
}

func TestAmbientGCPToken_MetadataServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" ||
			r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `{"access_token":"ya29.wi","expires_in":3599,"token_type":"Bearer"}`)
	}))
	defer srv.Close()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(srv.URL, "http://"))
	tok, err := AmbientGCPToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "ya29.wi" {
		t.Errorf("token = %q", tok)
	}
}
