package llmgateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeSendsTheCredentialLikeARun(t *testing.T) {
	var gotPath, gotKey, gotAuth, gotVersion string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotAuth = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("Authorization")
		gotVersion = r.Header.Get("anthropic-version")
		if gotKey != "sk-good" && gotAuth != "Bearer sk-good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()

	target := Target{
		Upstream: mustURL(t, upstream.URL+"/v1/"), Credential: "sk-good", AuthStyle: AuthXAPIKey,
		Headers: map[string]string{"anthropic-version": "2023-06-01"}, AllowPrivate: true,
	}
	status, err := Probe(t.Context(), target, "/models")
	if err != nil || status != http.StatusOK {
		t.Fatalf("Probe = %d, %v", status, err)
	}
	if gotPath != "/v1/models" || gotKey != "sk-good" || gotVersion != "2023-06-01" {
		t.Errorf("path %q key %q version %q", gotPath, gotKey, gotVersion)
	}

	target.Credential = "sk-bad"
	target.AuthStyle = AuthBearer
	if status, err := Probe(t.Context(), target, "models"); err != nil || status != http.StatusUnauthorized {
		t.Errorf("bad key: Probe = %d, %v", status, err)
	}
	if gotAuth != "Bearer sk-bad" || gotKey != "" {
		t.Errorf("bearer probe sent auth %q key %q", gotAuth, gotKey)
	}
}

func TestProbeRefusesAnInternalUpstreamNobodyVouchedFor(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the probe reached a loopback upstream")
	}))
	defer upstream.Close()
	_, err := Probe(t.Context(), Target{Upstream: mustURL(t, upstream.URL), Credential: "sk"}, "/v1/models")
	if err == nil {
		t.Fatal("probe of a loopback upstream without AllowPrivate went through")
	}
	if _, err := Probe(t.Context(), Target{Upstream: mustURL(t, upstream.URL)}, "/v1/models"); !errors.Is(err, ErrForbidden) {
		t.Errorf("probe without a credential: err = %v", err)
	}
}

func TestOutcomeHearsTheUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	g, err := New(func(context.Context, string, string) (Target, error) {
		return Target{Upstream: mustURL(t, upstream.URL), Credential: "sk", AllowPrivate: true, ProfileID: "lp_1"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g.logf = func(string, ...any) {}
	type outcome struct {
		profile string
		status  int
	}
	heard := make(chan outcome, 1)
	g.OnOutcome(func(_ context.Context, tgt Target, status int) { heard <- outcome{tgt.ProfileID, status} })
	srv := httptest.NewServer(g)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/anthropic/v1/messages", nil)
	req.Header.Set(RunKeyHeader, "run-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case got := <-heard:
		if got.profile != "lp_1" || got.status != http.StatusUnauthorized {
			t.Errorf("outcome = %q %d", got.profile, got.status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway never reported the outcome")
	}
}

func TestSendGuardsInternalUpstreams(t *testing.T) {
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()
	newReq := func() *http.Request {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, upstream.URL+"/v1/messages", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	if _, err := Send(t.Context(), newReq(), false); err == nil || hits != 0 {
		t.Fatalf("send to loopback without AllowPrivate: err %v, hits %d", err, hits)
	}
	resp, err := Send(t.Context(), newReq(), true)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits != 1 {
		t.Fatalf("vouched send: status %d, hits %d", resp.StatusCode, hits)
	}
}
