package credentials

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderEndpoint(t *testing.T) {
	const def = "https://sts.amazonaws.com/"
	cases := []struct {
		val, want string
		wantErr   bool
	}{
		{"", def, false},
		{"  ", def, false},
		{"https://sts.us-gov-west-1.amazonaws.com/", "https://sts.us-gov-west-1.amazonaws.com/", false},
		{"https://vpce-1.sts.eu-west-1.vpce.amazonaws.com", "https://vpce-1.sts.eu-west-1.vpce.amazonaws.com", false},
		{"http://sts.internal/", "", true},
		{"https://user:pw@sts.internal/", "", true},
		{"sts.internal", "", true},
	}
	for _, c := range cases {
		got, err := providerEndpoint(func(string) string { return c.val }, envAWSSTSEndpoint, def)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%q: got (%q, %v), want (%q, err=%v)", c.val, got, err, c.want, c.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), envAWSSTSEndpoint) {
			t.Errorf("%q: error does not name the variable: %v", c.val, err)
		}
	}
}

func TestAWSSigningRegionFor(t *testing.T) {
	if got := awsSigningRegionFor(func(string) string { return "" }); got != "us-east-1" {
		t.Errorf("default = %q", got)
	}
	if got := awsSigningRegionFor(func(string) string { return "us-gov-west-1" }); got != "us-gov-west-1" {
		t.Errorf("override = %q", got)
	}
}

func TestGCPCRMURLAddsTrailingSlash(t *testing.T) {
	t.Setenv(envGCPCRMEndpoint, "https://crm.p.googleapis.com/v1/projects")
	got, err := gcpCRMURL()
	if err != nil || got != "https://crm.p.googleapis.com/v1/projects/" {
		t.Errorf("got (%q, %v)", got, err)
	}
}

func TestAWSIntrospectUsesTheEndpointOverride(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult>
			<Arn>arn:aws-us-gov:iam::1:user/octopus</Arn><Account>1</Account>
			</GetCallerIdentityResult></GetCallerIdentityResponse>`))
	}))
	defer srv.Close()
	old := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() { httpClient = old })
	t.Setenv(envAWSSTSEndpoint, srv.URL+"/")
	t.Setenv(envAWSSigningRegion, "us-gov-west-1")

	creds := awsCreds{AccessKeyID: "AKIA1", SecretAccessKey: "s"}
	id, err := awsCallerIdentity(context.Background(), creds)
	if err != nil {
		t.Fatalf("awsCallerIdentity: %v", err)
	}
	if id != "arn:aws-us-gov:iam::1:user/octopus" {
		t.Errorf("identity = %q", id)
	}
	if !strings.Contains(gotAuth, "/us-gov-west-1/sts/aws4_request") {
		t.Errorf("request not signed for the override region: %s", gotAuth)
	}

	t.Setenv(envAWSSTSEndpoint, "http://sts.internal/")
	if _, err := awsCallerIdentity(context.Background(), creds); err == nil {
		t.Error("a plain-http override was used")
	}
}
