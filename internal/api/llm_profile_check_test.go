package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

func TestCheckProfileKeyAsksTheModelList(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotVersion = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		switch gotKey {
		case "sk-ant-api-good":
			w.WriteHeader(http.StatusOK)
		case "sk-ant-api-busy":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()

	s := &Server{}
	desc, ok := engineByName(t.Context(), "claude-code")
	if !ok {
		t.Fatal("claude-code not registered")
	}
	p := &secrets.LLMProfile{ID: "lp_1", Engine: "claude-code", Provider: engine.ProviderAnthropic}

	if got := s.checkProfileKey(t.Context(), desc, p, "sk-ant-api-good"); got != (secrets.KeyCheck{State: secrets.KeyUnchecked, Reason: secrets.KeyNoUpstream}) {
		t.Errorf("no upstreams: %+v", got)
	}

	s.SetLLMUpstreams(map[string]string{engine.ProviderAnthropic: upstream.URL})
	if got := s.checkProfileKey(t.Context(), desc, p, "sk-ant-api-good"); got != (secrets.KeyCheck{State: secrets.KeyValid}) {
		t.Errorf("good key: %+v", got)
	}
	if gotPath != "/v1/models" || gotVersion != anthropicAPIVersion {
		t.Errorf("probe path %q version %q", gotPath, gotVersion)
	}
	if got := s.checkProfileKey(t.Context(), desc, p, "sk-ant-api-revoked"); got != (secrets.KeyCheck{State: secrets.KeyInvalid, Reason: secrets.KeyRejected, Status: 401}) {
		t.Errorf("revoked key: %+v", got)
	}
	if got := s.checkProfileKey(t.Context(), desc, p, "sk-ant-api-busy"); got != (secrets.KeyCheck{State: secrets.KeyUnreachable, Reason: secrets.KeyUnexpected, Status: 503}) {
		t.Errorf("provider down: %+v", got)
	}

	gotKey = ""
	if got := s.checkProfileKey(t.Context(), desc, p, "sk-ant-oat01-subscription"); got.State != secrets.KeyUnchecked || got.Reason != secrets.KeyNotCheckable || gotKey != "" {
		t.Errorf("oauth token: %+v, probed with %q", got, gotKey)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	s.SetLLMUpstreams(map[string]string{engine.ProviderAnthropic: closed.URL})
	got := s.checkProfileKey(t.Context(), desc, p, "sk-ant-api-good")
	if got.State != secrets.KeyUnreachable || got.Reason != secrets.KeyNoAnswer || !strings.HasPrefix(got.Host, "127.0.0.1:") {
		t.Errorf("closed host: %+v", got)
	}
}

func TestCheckProfileKeyOpenAIUsesBearerUnderV1(t *testing.T) {
	var gotPath, gotAuth, gotOrg string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotOrg = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("OpenAI-Organization")
	}))
	defer upstream.Close()
	s := &Server{}
	s.SetLLMUpstreams(map[string]string{engine.ProviderOpenAI: upstream.URL + "/v1"})
	desc, ok := engineByName(t.Context(), "codex")
	if !ok {
		t.Fatal("codex not registered")
	}
	p := &secrets.LLMProfile{ID: "lp_2", Engine: "codex", Provider: engine.ProviderOpenAI, OrgID: "org-1"}
	if got := s.checkProfileKey(t.Context(), desc, p, "sk-proj-abc"); got.State != secrets.KeyValid {
		t.Errorf("check = %+v", got)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer sk-proj-abc" || gotOrg != "org-1" {
		t.Errorf("path %q auth %q org %q", gotPath, gotAuth, gotOrg)
	}
}

func TestCheckProfileKeyOpenAICompatibleAtItsEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		if r.Header.Get("Authorization") != "Bearer sk-good" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()
	t.Setenv("OCTOPUS_LLM_ALLOW_HTTP_HOSTS", "127.0.0.1")
	s := &Server{}
	s.SetLLMUpstreams(map[string]string{})
	desc, ok := engineByName(t.Context(), "opencode")
	if !ok {
		t.Fatal("opencode not registered")
	}
	p := &secrets.LLMProfile{ID: "lp_3", Engine: "opencode", Provider: engine.ProviderOpenAICompatible, BaseURL: upstream.URL + "/v1"}
	if got := s.checkProfileKey(t.Context(), desc, p, "sk-good"); got.State != secrets.KeyValid {
		t.Errorf("check = %+v", got)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer sk-good" {
		t.Errorf("path %q auth %q", gotPath, gotAuth)
	}
	if got := s.checkProfileKey(t.Context(), desc, p, "sk-bad"); got.State != secrets.KeyInvalid {
		t.Errorf("a refused key: %+v", got)
	}
}
