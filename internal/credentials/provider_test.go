package credentials

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/cloudsign"
)

func redirect(t *testing.T, target *string, to string) {
	t.Helper()
	old := *target
	*target = to
	t.Cleanup(func() { *target = old })
}

func TestGithubIntrospect_ValidTokenReadsLoginScopesAndExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token ghp_secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("X-OAuth-Scopes", "repo, workflow, gist")
		w.Header().Set("github-authentication-token-expiration", "2026-09-01 12:00:00 UTC")
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	}))
	defer srv.Close()
	redirect(t, &githubAPI, srv.URL)

	got, err := (&githubProvider{}).Introspect(context.Background(), "github_pat",
		map[string]string{"token": "ghp_secret"}, nil)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if got.State != StateValid || got.Identity != "alice" {
		t.Fatalf("state=%q identity=%q", got.State, got.Identity)
	}
	want := []string{"github:actions:read", "github:actions:write", "github:repo:read", "github:repo:write"}
	if strings.Join(got.Capabilities, ",") != strings.Join(want, ",") {
		t.Errorf("capabilities = %v, want %v", got.Capabilities, want)
	}
	if got.ExpiresAt.IsZero() || got.ExpiresAt.Year() != 2026 || got.ExpiresAt.Month() != time.September {
		t.Errorf("expires_at = %v", got.ExpiresAt)
	}
	if strings.Contains(got.Hint, "ghp_secret") {
		t.Errorf("hint leaks the token: %q", got.Hint)
	}
}

func TestGithubIntrospect_NoScopeHeaderLeavesCapabilitiesUndiscovered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"login":"bot"}`))
	}))
	defer srv.Close()
	redirect(t, &githubAPI, srv.URL)

	got, err := (&githubProvider{}).Introspect(context.Background(), "github_pat",
		map[string]string{"token": "github_pat_x"}, nil)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if len(got.Capabilities) != 0 {
		t.Fatalf("capabilities = %v, want none", got.Capabilities)
	}
	cred := &Credential{Capabilities: got.Capabilities}
	if !cred.HasCapability("github:repo:write") {
		t.Error("undiscovered capabilities must not block a tool")
	}
}

func TestGithubIntrospect_RejectedTokenIsInvalidNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()
	redirect(t, &githubAPI, srv.URL)

	got, err := (&githubProvider{}).Introspect(context.Background(), "github_pat",
		map[string]string{"token": "nope"}, nil)
	if err != nil {
		t.Fatalf("Introspect returned an error for a rejected token: %v", err)
	}
	if got.State != StateInvalid || got.Err != "Bad credentials" {
		t.Errorf("state=%q err=%q", got.State, got.Err)
	}
}

func TestParseGithubExpiry_UnknownFormatIsNoExpiryNotAnError(t *testing.T) {
	if ts := parseGithubExpiry("whenever"); !ts.IsZero() {
		t.Errorf("got %v, want zero", ts)
	}
}

const stsIdentityXML = `<GetCallerIdentityResponse><GetCallerIdentityResult>
	<Arn>arn:aws:iam::123456789012:user/octopus</Arn><Account>123456789012</Account>
	</GetCallerIdentityResult></GetCallerIdentityResponse>`

func TestAWSIntrospect_IdentityAndSimulatedCapabilities(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
			t.Errorf("request was not SigV4-signed: %q", auth)
		}
		_, _ = w.Write([]byte(stsIdentityXML))
	}))
	defer sts.Close()

	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("ActionNames.member.1"); got != "cloudwatch:GetMetricData" {
			t.Errorf("first action = %q", got)
		}
		if got := r.PostForm.Get("PolicySourceArn"); got != "arn:aws:iam::123456789012:user/octopus" {
			t.Errorf("PolicySourceArn = %q", got)
		}
		_, _ = w.Write([]byte(`<SimulatePrincipalPolicyResponse><SimulatePrincipalPolicyResult>
			<EvaluationResults>
			  <member><EvalActionName>cloudwatch:GetMetricData</EvalActionName><EvalDecision>allowed</EvalDecision></member>
			  <member><EvalActionName>logs:StartQuery</EvalActionName><EvalDecision>implicitDeny</EvalDecision></member>
			</EvaluationResults>
			</SimulatePrincipalPolicyResult></SimulatePrincipalPolicyResponse>`))
	}))
	defer iam.Close()

	redirect(t, &awsSTSEndpoint, sts.URL)
	redirect(t, &awsIAMEndpoint, iam.URL)

	got, err := (&awsProvider{}).Introspect(context.Background(), "aws_keys",
		map[string]string{"access_key_id": "AKIA1", "secret_access_key": "s"},
		[]string{"aws:cloudwatch:GetMetricData", "aws:logs:StartQuery", "github:repo:read"})
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if got.State != StateValid || got.Identity != "arn:aws:iam::123456789012:user/octopus" {
		t.Fatalf("state=%q identity=%q", got.State, got.Identity)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "aws:cloudwatch:GetMetricData" {
		t.Errorf("capabilities = %v, want only the allowed one", got.Capabilities)
	}
	if got.Hint != "AKIA1" {
		t.Errorf("hint = %q, want the (public) access key id", got.Hint)
	}
}

func TestAWSIntrospect_SimulationDeniedLeavesCapabilitiesUndiscovered(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(stsIdentityXML))
	}))
	defer sts.Close()
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<ErrorResponse><Error><Code>AccessDenied</Code>
			<Message>not authorized to perform iam:SimulatePrincipalPolicy</Message>
			</Error></ErrorResponse>`))
	}))
	defer iam.Close()
	redirect(t, &awsSTSEndpoint, sts.URL)
	redirect(t, &awsIAMEndpoint, iam.URL)

	got, err := (&awsProvider{}).Introspect(context.Background(), "aws_keys",
		map[string]string{"access_key_id": "AKIA1", "secret_access_key": "s"},
		[]string{"aws:logs:StartQuery"})
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if got.State != StateValid {
		t.Errorf("state = %q, want valid — the key itself works", got.State)
	}
	if len(got.Capabilities) != 0 {
		t.Errorf("capabilities = %v, want undiscovered", got.Capabilities)
	}
	if !strings.Contains(got.Err, "AccessDenied") {
		t.Errorf("err should explain why discovery failed, got %q", got.Err)
	}
}

func TestAWSIntrospect_RejectedKeyIsInvalidNotError(t *testing.T) {
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<ErrorResponse><Error><Code>InvalidClientTokenId</Code>
			<Message>The security token included in the request is invalid.</Message>
			</Error></ErrorResponse>`))
	}))
	defer sts.Close()
	redirect(t, &awsSTSEndpoint, sts.URL)

	got, err := (&awsProvider{}).Introspect(context.Background(), "aws_keys",
		map[string]string{"access_key_id": "AKIA1", "secret_access_key": "bad"}, nil)
	if err != nil {
		t.Fatalf("Introspect returned an error for a rejected key: %v", err)
	}
	if got.State != StateInvalid || !strings.Contains(got.Err, "security token") {
		t.Errorf("state=%q err=%q", got.State, got.Err)
	}
}

func testServiceAccount(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sa := map[string]string{
		"type":         "service_account",
		"project_id":   "demo-project",
		"client_email": "octopus@demo-project.iam.gserviceaccount.com",
		"private_key":  string(pemBytes),
	}
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(raw)
}

func TestGCPIntrospect_MintsTokenAndTestsPermissions(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", got)
		}
		if parts := strings.Split(r.PostForm.Get("assertion"), "."); len(parts) != 3 {
			t.Errorf("assertion is not a signed JWT: %d parts", len(parts))
		}
		_, _ = w.Write([]byte(`{"access_token":"ya29.test","expires_in":3599}`))
	}))
	defer tokenSrv.Close()

	crmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ya29.test" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.Contains(r.URL.Path, "demo-project") {
			t.Errorf("path did not target the project: %s", r.URL.Path)
		}
		var body struct {
			Permissions []string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, p := range body.Permissions {
			if strings.Contains(p, ":") {
				t.Errorf("permission still namespaced: %q", p)
			}
		}
		_, _ = w.Write([]byte(`{"permissions":["logging.logEntries.list"]}`))
	}))
	defer crmSrv.Close()

	redirect(t, &gcpTokenEndpoint, tokenSrv.URL)
	redirect(t, &gcpCRMEndpoint, crmSrv.URL+"/v1/projects/")

	got, err := (&gcpProvider{}).Introspect(context.Background(), "gcp_sa",
		map[string]string{"service_account_json": testServiceAccount(t)},
		[]string{"gcp:logging.logEntries.list", "gcp:run.services.list", "aws:logs:StartQuery"})
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if got.State != StateValid {
		t.Fatalf("state = %q (%s)", got.State, got.Err)
	}
	if got.Identity != "octopus@demo-project.iam.gserviceaccount.com" {
		t.Errorf("identity = %q", got.Identity)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "gcp:logging.logEntries.list" {
		t.Errorf("capabilities = %v", got.Capabilities)
	}
	if !got.ExpiresAt.IsZero() {
		t.Errorf("expires_at = %v, want zero (the KEY does not expire)", got.ExpiresAt)
	}
}

func TestGCPIntrospect_MalformedKeyFileIsInvalidNotError(t *testing.T) {
	got, err := (&gcpProvider{}).Introspect(context.Background(), "gcp_sa",
		map[string]string{"service_account_json": "not json at all"}, nil)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if got.State != StateInvalid {
		t.Errorf("state = %q, want invalid", got.State)
	}
}

func TestGCPAssertion_ClaimsTargetTheTokenEndpoint(t *testing.T) {
	var sa gcpServiceAccount
	if err := json.Unmarshal([]byte(testServiceAccount(t)), &sa); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, err := cloudsign.GCPAssertion(sa.ClientEmail, sa.PrivateKey, time.Now())
	if err != nil {
		t.Fatalf("GCPAssertion: %v", err)
	}
	parts := strings.Split(got, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d parts", len(parts))
	}
	claims := decodeSegment(t, parts[1])
	if claims["aud"] != gcpTokenEndpoint {
		t.Errorf("aud = %v, want %s", claims["aud"], gcpTokenEndpoint)
	}
	if claims["iss"] != sa.ClientEmail {
		t.Errorf("iss = %v", claims["iss"])
	}
	if claims["scope"] != cloudsign.GCPScope {
		t.Errorf("scope = %v", claims["scope"])
	}
}

func decodeSegment(t *testing.T, seg string) map[string]any {
	t.Helper()
	raw, err := b64urlDecode(seg)
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal segment: %v", err)
	}
	return out
}

func TestFigmaIntrospect_ValidTokenReadsIdentityAndLeavesCapabilitiesUndiscovered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" {
			t.Errorf("path = %q, want /v1/me", r.URL.Path)
		}
		if got := r.Header.Get("X-Figma-Token"); got != "figd_secret" {
			t.Errorf("X-Figma-Token = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":"1","handle":"Ana","email":"ana@example.com"}`))
	}))
	defer srv.Close()
	redirect(t, &figmaAPI, srv.URL)

	got, err := (&figmaProvider{}).Introspect(context.Background(), "figma_pat",
		map[string]string{"token": "figd_secret"}, NewCatalog().Permissions())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if got.State != StateValid || got.Identity != "ana@example.com" {
		t.Fatalf("state=%q identity=%q", got.State, got.Identity)
	}
	if len(got.Capabilities) != 0 {
		t.Errorf("capabilities = %v, want none", got.Capabilities)
	}
	cred := &Credential{Capabilities: got.Capabilities}
	if missing := cred.MissingCapabilities([]string{"figma:file_content:read"}); len(missing) != 0 {
		t.Errorf("undiscovered capabilities blocked a tool: missing %v", missing)
	}
	if strings.Contains(got.Hint, "figd_secret") {
		t.Errorf("hint leaks the token: %q", got.Hint)
	}
}

func TestFigmaIntrospect_RejectedTokenIsInvalidWithFigmasWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":403,"err":"Invalid token"}`))
	}))
	defer srv.Close()
	redirect(t, &figmaAPI, srv.URL)

	got, err := (&figmaProvider{}).Introspect(context.Background(), "figma_pat",
		map[string]string{"token": "nope"}, nil)
	if err != nil {
		t.Fatalf("Introspect returned an error for a rejected token: %v", err)
	}
	if got.State != StateInvalid || got.Err != "Invalid token" {
		t.Errorf("state=%q err=%q", got.State, got.Err)
	}
}

func TestFigmaIntrospect_EmptyTokenIsRefusedBeforeAnyCall(t *testing.T) {
	if _, err := (&figmaProvider{}).Introspect(context.Background(), "figma_pat",
		map[string]string{"token": "  "}, nil); err == nil {
		t.Error("accepted an empty token")
	}
}

func TestFigmaErrorMessage_ReadsBothErrorShapes(t *testing.T) {
	for body, want := range map[string]string{
		`{"status":403,"err":"Invalid token"}`:              "Invalid token",
		`{"error":true,"status":404,"message":"Not found"}`: "Not found",
		`not json`: "HTTP 500",
	} {
		if got := figmaErrorMessage([]byte(body), 500); got != want {
			t.Errorf("figmaErrorMessage(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestRegistry_ValidatePayloadRequiresNonOptionalFields(t *testing.T) {
	r := NewRegistry()
	if err := r.ValidatePayload("aws", "aws_keys", map[string]string{"access_key_id": "AKIA"}); err == nil {
		t.Error("accepted an AWS payload with no secret key")
	}
	if err := r.ValidatePayload("aws", "aws_keys", map[string]string{
		"access_key_id": "AKIA", "secret_access_key": "s",
	}); err != nil {
		t.Errorf("rejected a complete payload: %v", err)
	}
	if err := r.ValidatePayload("aws", "nope", map[string]string{}); err == nil {
		t.Error("accepted an unknown kind")
	}
	if err := r.ValidatePayload("nope", "x", map[string]string{}); err == nil {
		t.Error("accepted an unknown provider")
	}
}

func TestRegistry_SpecsCoverEveryCatalogProvider(t *testing.T) {
	r := NewRegistry()
	for _, spec := range NewCatalog().All() {
		for _, provider := range spec.Providers() {
			if _, ok := r.Get(provider); !ok {
				t.Errorf("%s/%s needs provider %q, which is not registered",
					spec.Server, spec.Tool, provider)
			}
		}
	}
}

func TestWantForProvider_StripsNamespaceAndDeduplicates(t *testing.T) {
	got := wantForProvider("aws", []string{
		"aws:logs:StartQuery", "gcp:logging.logEntries.list", "aws:logs:StartQuery", "aws:",
	})
	if len(got) != 1 || got[0] != "logs:StartQuery" {
		t.Errorf("got %v, want [logs:StartQuery]", got)
	}
}

func b64urlDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}
