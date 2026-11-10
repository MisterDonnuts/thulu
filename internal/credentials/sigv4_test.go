package credentials

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func newSignedRequest(t *testing.T, creds awsCreds) *http.Request {
	t.Helper()
	body := []byte("Action=GetCallerIdentity&Version=2011-06-15")
	req, err := http.NewRequest(http.MethodPost, awsSTSEndpoint, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	if err := signV4(req, body, creds, "us-east-1", "sts", at); err != nil {
		t.Fatalf("signV4: %v", err)
	}
	return req
}

func TestSignV4_SetsCredentialScopeAndSignedHeaders(t *testing.T) {
	req := newSignedRequest(t, awsCreds{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"})

	auth := req.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 ",
		"Credential=AKIAEXAMPLE/20260803/us-east-1/sts/aws4_request",
		"SignedHeaders=content-type;host;x-amz-date",
		"Signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("Authorization missing %q\ngot: %s", want, auth)
		}
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20260803T120000Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
	if req.Header.Get("X-Amz-Security-Token") != "" {
		t.Error("session-token header set for static credentials")
	}
}

func TestSignV4_SessionTokenIsSentAndSigned(t *testing.T) {
	req := newSignedRequest(t, awsCreds{
		AccessKeyID: "ASIAEXAMPLE", SecretAccessKey: "secret", SessionToken: "tok123",
	})
	if got := req.Header.Get("X-Amz-Security-Token"); got != "tok123" {
		t.Errorf("X-Amz-Security-Token = %q, want tok123", got)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "SignedHeaders=content-type;host;x-amz-date;x-amz-security-token") {
		t.Errorf("session token not in SignedHeaders: %s", req.Header.Get("Authorization"))
	}
}

func TestSignV4_RegionAndServiceReachTheCredentialScope(t *testing.T) {
	creds := awsCreds{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"}
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	body := []byte("Action=GetCallerIdentity&Version=2011-06-15")

	sign := func(region, service string) string {
		req, err := http.NewRequest(http.MethodPost, awsSTSEndpoint, strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if err := signV4(req, body, creds, region, service, at); err != nil {
			t.Fatalf("signV4: %v", err)
		}
		return req.Header.Get("Authorization")
	}

	east := sign("us-east-1", "sts")
	west := sign("eu-west-2", "sts")
	iam := sign("us-east-1", "iam")

	if !strings.Contains(west, "/20260803/eu-west-2/sts/aws4_request") {
		t.Errorf("region missing from the credential scope: %s", west)
	}
	if !strings.Contains(iam, "/20260803/us-east-1/iam/aws4_request") {
		t.Errorf("service missing from the credential scope: %s", iam)
	}
	if east == west || east == iam {
		t.Error("region/service do not affect the signature — a scope mismatch would go unnoticed")
	}
}

func TestSignV4_DifferentBodiesProduceDifferentSignatures(t *testing.T) {
	creds := awsCreds{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"}
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	sign := func(body string) string {
		req, err := http.NewRequest(http.MethodPost, awsSTSEndpoint, strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if err := signV4(req, []byte(body), creds, "us-east-1", "sts", at); err != nil {
			t.Fatalf("signV4: %v", err)
		}
		return req.Header.Get("Authorization")
	}
	if sign("Action=A") == sign("Action=B") {
		t.Error("payload is not covered by the signature — a tampered body would still verify")
	}
}

func TestSignV4_RejectsIncompleteCredentials(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, awsSTSEndpoint, strings.NewReader(""))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := signV4(req, nil, awsCreds{AccessKeyID: "AKIA"}, "us-east-1", "sts", time.Now()); err == nil {
		t.Error("signing succeeded without a secret key")
	}
}

func TestPolicySourceARN_RewritesAssumedRole(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			in:   "arn:aws:sts::123456789012:assumed-role/OctopusReader/session-name",
			want: "arn:aws:iam::123456789012:role/OctopusReader",
		},
		{
			in:   "arn:aws-us-gov:sts::123456789012:assumed-role/Role/abc",
			want: "arn:aws-us-gov:iam::123456789012:role/Role",
		},
		{
			in:   "arn:aws:iam::123456789012:user/octopus",
			want: "arn:aws:iam::123456789012:user/octopus",
		},
	}
	for _, c := range cases {
		if got := policySourceARN(c.in); got != c.want {
			t.Errorf("policySourceARN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
