package automation

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/netguard"
)

func httpStepState() *runState {
	return &runState{
		automation: &Automation{WorkspaceID: "ws1"},
		run:        &Run{ID: "r1"},
		ctx:        map[string]any{"steps": map[string]any{}},
	}
}

func TestHTTPStep_RefusesInternalDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "")

	e := &Engine{}
	for _, u := range []string{srv.URL, "http://169.254.169.254/latest/meta-data/iam/"} {
		_, err := e.stepHTTP(t.Context(), httpStepState(), &Step{ID: "s", HTTP: &HTTPStep{URL: u}})
		if !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("%s: err=%v, want ErrBlocked", u, err)
		}
	}

	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "127.0.0.0/8")
	out, err := e.stepHTTP(t.Context(), httpStepState(), &Step{ID: "s", HTTP: &HTTPStep{URL: srv.URL}})
	if err != nil {
		t.Fatalf("allowlisted internal service: %v", err)
	}
	if out["status_code"] != float64(200) || out["truncated"] != false {
		t.Errorf("output = %v", out)
	}
}

func TestHTTPStep_CapsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxHTTPBody+4096)))
	}))
	defer srv.Close()
	t.Setenv("OCTOPUS_EGRESS_GUARD", "")
	t.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "127.0.0.0/8")

	out, err := (&Engine{}).stepHTTP(t.Context(), httpStepState(), &Step{ID: "s", HTTP: &HTTPStep{URL: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := out["text"].(string); len(text) != maxHTTPBody {
		t.Errorf("body length %d, want capped at %d", len(text), maxHTTPBody)
	}
	if out["truncated"] != true {
		t.Error("an oversized body was not flagged as truncated")
	}
}
