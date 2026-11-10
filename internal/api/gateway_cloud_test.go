package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/cloudsign"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

type captured struct {
	method, rawPath, host string
	header                http.Header
	body                  []byte
}

func fakeCloud(t *testing.T) (*httptest.Server, chan captured) {
	t.Helper()
	got := make(chan captured, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- captured{r.Method, r.URL.EscapedPath(), r.Host, r.Header.Clone(), b}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OCTOPUS_LLM_ALLOW_HTTP_HOSTS", "127.0.0.1")
	return srv, got
}

func serveGateway(t *testing.T, res secrets.ResolvedLLM) http.Handler {
	t.Helper()
	d := gatewayFixture(res)
	gw, err := llmgateway.New(func(ctx context.Context, token, provider string) (llmgateway.Target, error) {
		return resolveGatewayTarget(ctx, d, token, provider)
	})
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

func call(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Amz-Security-Token", "from-the-agent")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGatewayBedrock_SignsWithTheProfileKey(t *testing.T) {
	srv, got := fakeCloud(t)
	secret, _ := engine.CloudSecret{AWSAccessKeyID: "AKIAPROFILE", AWSSecretAccessKey: "profile-secret", AWSSessionToken: "sess"}.Encode()
	h := serveGateway(t, secrets.ResolvedLLM{
		Source: "binding", ProfileID: "lp_b", Engine: "claude-code", Provider: "bedrock",
		Region: "eu-west-1", BaseURL: srv.URL, Credential: secret,
	})

	const body = `{"anthropic_version":"bedrock-2023-05-31","messages":[]}`
	rec := call(h, http.MethodPost, "/bedrock/model/anthropic.claude-v2%3A1/invoke-with-response-stream", "run-good", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	c := <-got
	if c.rawPath != "/model/anthropic.claude-v2%3A1/invoke-with-response-stream" {
		t.Errorf("upstream path = %q", c.rawPath)
	}
	if string(c.body) != body {
		t.Errorf("body changed on the way: %q", c.body)
	}
	if c.header.Get("X-Amz-Security-Token") != "sess" {
		t.Errorf("session token = %q, want the profile's", c.header.Get("X-Amz-Security-Token"))
	}
	auth := c.header.Get("Authorization")
	if strings.Contains(auth, "run-good") || !strings.Contains(auth, "Credential=AKIAPROFILE/") ||
		!strings.Contains(auth, "/eu-west-1/bedrock/aws4_request") {
		t.Errorf("Authorization = %q", auth)
	}

	at, err := time.Parse("20060102T150405Z", c.header.Get("X-Amz-Date"))
	if err != nil {
		t.Fatal(err)
	}
	check, _ := http.NewRequest(c.method, "http://"+c.host+c.rawPath, nil)
	check.Header.Set("Content-Type", c.header.Get("Content-Type"))
	if err := cloudsign.SignV4(check, c.body, cloudsign.AWSCreds{
		AccessKeyID: "AKIAPROFILE", SecretAccessKey: "profile-secret", SessionToken: "sess",
	}, "eu-west-1", "bedrock", at); err != nil {
		t.Fatal(err)
	}
	if check.Header.Get("Authorization") != auth {
		t.Errorf("signature does not verify:\n got  %s\n want %s", auth, check.Header.Get("Authorization"))
	}
}

func TestGatewayBedrock_APIKeyGoesAsBearer(t *testing.T) {
	srv, got := fakeCloud(t)
	secret, _ := engine.CloudSecret{BedrockAPIKey: "ABSK-profile"}.Encode()
	h := serveGateway(t, secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Provider: "bedrock", Region: "us-east-1", BaseURL: srv.URL, Credential: secret,
	})
	if rec := call(h, http.MethodPost, "/bedrock/model/m/invoke", "run-good", "{}"); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if c := <-got; c.header.Get("Authorization") != "Bearer ABSK-profile" {
		t.Errorf("Authorization = %q", c.header.Get("Authorization"))
	}
}

func TestGatewayVertex_AmbientIdentity(t *testing.T) {
	srv, got := fakeCloud(t)
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `{"access_token":"ya29.daemon","expires_in":3600}`)
	}))
	defer meta.Close()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GCE_METADATA_HOST", strings.TrimPrefix(meta.URL, "http://"))
	t.Setenv(engine.EnvAmbientCloudIdentity, "1")

	h := serveGateway(t, secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Provider: "vertex", Region: "us-east5", ProjectID: "acme-ai", BaseURL: srv.URL,
	})
	const path = "/vertex/projects/acme-ai/locations/us-east5/publishers/anthropic/models/claude-opus-5-5:streamRawPredict"
	if rec := call(h, http.MethodPost, path, "run-good", "{}"); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	c := <-got
	if c.header.Get("Authorization") != "Bearer ya29.daemon" {
		t.Errorf("Authorization = %q", c.header.Get("Authorization"))
	}
	if c.header.Get("X-Amz-Security-Token") != "" {
		t.Error("a header the agent set reached Google")
	}

	for _, bad := range []string{
		"/vertex/projects/someone-else/locations/us-east5/publishers/anthropic/models/claude:rawPredict",
		"/vertex/projects/acme-ai/locations/europe-west1/publishers/anthropic/models/claude:rawPredict",
		"/vertex/projects/acme-ai/locations/us-east5/publishers/google/models/gemini:generateContent",
		"/vertex/projects/acme-ai/locations/us-east5/endpoints/e1:predict",
	} {
		if rec := call(h, http.MethodPost, bad, "run-good", "{}"); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", bad, rec.Code)
		}
	}
}

func TestGatewayBedrock_OnlyModelCalls(t *testing.T) {
	srv, _ := fakeCloud(t)
	secret, _ := engine.CloudSecret{AWSAccessKeyID: "AKIA", AWSSecretAccessKey: "s"}.Encode()
	h := serveGateway(t, secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Provider: "bedrock", Region: "us-east-1", BaseURL: srv.URL, Credential: secret,
	})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/bedrock/model/m/invoke"},
		{http.MethodPost, "/bedrock/guardrail/g1/version/1/apply"},
		{http.MethodPost, "/bedrock/async-invoke"},
		{http.MethodPost, "/bedrock/model/../invoke"},
		{http.MethodPost, "/bedrock/model/a%2Fb/invoke"},
		{http.MethodGet, "/bedrock/inference-profiles"},
	} {
		if rec := call(h, c.method, c.path, "run-good", "{}"); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: status %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if rec := call(h, http.MethodPost, "/bedrock/model/m/invoke", "", "{}"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no run token: status %d, want 401", rec.Code)
	}
}

func TestResolveGatewayTarget_CloudRefusals(t *testing.T) {
	keys, _ := engine.CloudSecret{AWSAccessKeyID: "AKIA", AWSSecretAccessKey: "s"}.Encode()
	bedrock := secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Provider: "bedrock", Region: "us-east-1", Credential: keys}

	t.Setenv(engine.EnvAmbientCloudIdentity, "")
	cases := []struct {
		name     string
		provider string
		res      func() secrets.ResolvedLLM
	}{
		{"anthropic profile on the bedrock route", "bedrock", func() secrets.ResolvedLLM {
			return secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Credential: "sk-ant-api-x"}
		}},
		{"bedrock profile on the vertex route", "vertex", func() secrets.ResolvedLLM { return bedrock }},
		{"region that would change the hostname", "bedrock", func() secrets.ResolvedLLM {
			r := bedrock
			r.Region = "us-east-1.evil.example"
			return r
		}},
		{"no credential without the operator's opt-in", "bedrock", func() secrets.ResolvedLLM {
			r := bedrock
			r.Credential = ""
			return r
		}},
		{"vertex without a project", "vertex", func() secrets.ResolvedLLM {
			sa, _ := engine.CloudSecret{GCPServiceAccountJSON: `{"client_email":"a","private_key":"k"}`}.Encode()
			return secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Provider: "vertex", Region: "us-east5", Credential: sa}
		}},
		{"engine that cannot skip the cloud's signing", "bedrock", func() secrets.ResolvedLLM {
			r := bedrock
			r.Engine = "codex"
			return r
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveGatewayTarget(t.Context(), gatewayFixture(c.res()), "run-good", c.provider)
			if !errors.Is(err, llmgateway.ErrForbidden) {
				t.Errorf("err = %v, want ErrForbidden", err)
			}
		})
	}

	t.Run("default upstreams", func(t *testing.T) {
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(bedrock), "run-good", "bedrock")
		if err != nil {
			t.Fatal(err)
		}
		if got.Upstream.String() != "https://bedrock-runtime.us-east-1.amazonaws.com" || !got.AllowPrivate {
			t.Errorf("bedrock upstream = %s (private %v)", got.Upstream, got.AllowPrivate)
		}
		sa, _ := engine.CloudSecret{GCPServiceAccountJSON: `{"client_email":"a","private_key":"k"}`}.Encode()
		for region, want := range map[string]string{
			"us-east5": "https://us-east5-aiplatform.googleapis.com/v1",
			"global":   "https://aiplatform.googleapis.com/v1",
		} {
			got, err := resolveGatewayTarget(t.Context(), gatewayFixture(secrets.ResolvedLLM{
				Source: "binding", Engine: "claude-code", Provider: "vertex", Region: region, ProjectID: "p", Credential: sa,
			}), "run-good", "vertex")
			if err != nil {
				t.Fatal(err)
			}
			if got.Upstream.String() != want {
				t.Errorf("vertex %s upstream = %s, want %s", region, got.Upstream, want)
			}
		}
	})
}
