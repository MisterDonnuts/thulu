package cloudsign

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSigningKey_MatchesPublishedVector(t *testing.T) {
	const (
		secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
		want   = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	)
	got := hex.EncodeToString(SigningKey(secret, "20150830", "us-east-1", "iam"))
	if got != want {
		t.Errorf("signing key = %s, want %s", got, want)
	}
}

func TestHexSHA256_EmptyPayload(t *testing.T) {
	const want = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := HexSHA256(nil); got != want {
		t.Errorf("HexSHA256(nil) = %s, want %s", got, want)
	}
}

func TestCanonicalURI_DoubleEncodesTheEscapedPath(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude-v2%3A1/invoke", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonicalURI(req.URL), "/model/anthropic.claude-v2%253A1/invoke"; got != want {
		t.Errorf("canonicalURI = %q, want %q", got, want)
	}
	root, _ := http.NewRequest(http.MethodPost, "https://sts.amazonaws.com", nil)
	if got := canonicalURI(root.URL); got != "/" {
		t.Errorf("empty path = %q, want /", got)
	}
}

func TestCanonicalQuery_SortsAndEncodes(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://x.amazonaws.com/?b=2&a=z+y&a=x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonicalQuery(req.URL), "a=x&a=z%20y&b=2"; got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}

func TestSignV4_SignsOnlyHeadersItControls(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	creds := AWSCreds{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"}
	body := []byte(`{"messages":[]}`)
	req, _ := http.NewRequest(http.MethodPost,
		"https://bedrock-runtime.eu-west-1.amazonaws.com/model/m/invoke", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	if err := SignV4(req, body, creds, "eu-west-1", "bedrock", at); err != nil {
		t.Fatal(err)
	}
	auth := req.Header.Get("Authorization")
	for _, want := range []string{
		"Credential=AKIAEXAMPLE/20260925/eu-west-1/bedrock/aws4_request",
		"SignedHeaders=content-type;host;x-amz-date,",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("Authorization missing %q: %s", want, auth)
		}
	}

	get, _ := http.NewRequest(http.MethodGet, "https://x.amazonaws.com/", nil)
	if err := SignV4(get, nil, creds, "us-east-1", "x", at); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(get.Header.Get("Authorization"), "SignedHeaders=host;x-amz-date,") {
		t.Errorf("unexpected signed headers: %s", get.Header.Get("Authorization"))
	}
}

func TestSignV4_DropsAForeignSessionToken(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://x.amazonaws.com/", nil)
	req.Header.Set("X-Amz-Security-Token", "from-the-client")
	if err := SignV4(req, nil, AWSCreds{AccessKeyID: "A", SecretAccessKey: "s"}, "us-east-1", "x", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-Amz-Security-Token"); got != "" {
		t.Errorf("X-Amz-Security-Token = %q, want none", got)
	}
}
