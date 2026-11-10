package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

func gatewayFixture(res secrets.ResolvedLLM) gatewayDeps {
	return gatewayDeps{
		keyInfo: func(_ context.Context, tok string) *auth.APIKeyInfo {
			switch tok {
			case "run-good":
				return &auth.APIKeyInfo{WorkspaceID: "ws-acme", Scope: auth.RunScope{TaskID: "task1", Agent: "dev", Stage: "impl"}}
			case "run-chat":
				return &auth.APIKeyInfo{WorkspaceID: "ws-acme", Scope: auth.RunScope{ChatID: "chat-1", Agent: "dev", Stage: "chat"}}
			case "run-other-ws":
				return &auth.APIKeyInfo{WorkspaceID: "ws-evil", Scope: auth.RunScope{TaskID: "task1", Agent: "dev"}}
			case "operator":
				return &auth.APIKeyInfo{WorkspaceID: "ws-acme"}
			}
			return nil
		},
		taskWorkspace: func(_ context.Context, id string) (string, bool) {
			if id == "task1" {
				return "ws-acme", true
			}
			return "", false
		},
		agentEngine: func(string, string) string { return "" },
		resolveLLM: func(context.Context, string, string, string) (secrets.ResolvedLLM, error) {
			return res, nil
		},
		upstreams: map[string]string{
			llmgateway.ProviderAnthropic: "https://api.anthropic.com",
			llmgateway.ProviderOpenAI:    "https://api.openai.com/v1",
		},
	}
}

func TestResolveGatewayTarget(t *testing.T) {
	anthropicKey := secrets.ResolvedLLM{Source: "binding", ProfileID: "lp_1", Engine: "claude-code", Credential: "sk-ant-api-real"}

	t.Run("task run gets the workspace key and default upstream", func(t *testing.T) {
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(anthropicKey), "run-good", "anthropic")
		if err != nil {
			t.Fatal(err)
		}
		if got.Credential != "sk-ant-api-real" || got.Upstream.String() != "https://api.anthropic.com" ||
			got.AuthStyle != llmgateway.AuthXAPIKey || got.RunID != "task1" || got.WorkspaceID != "ws-acme" {
			t.Errorf("target = %+v", got)
		}
	})

	t.Run("chat run resolves too", func(t *testing.T) {
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(anthropicKey), "run-chat", "anthropic")
		if err != nil || got.RunID != "chat-1" {
			t.Fatalf("target = %+v, %v", got, err)
		}
	})

	t.Run("profile base_url and bearer header", func(t *testing.T) {
		res := anthropicKey
		res.BaseURL, res.AuthHeader, res.Credential = "https://llm.corp.example/anthropic", "bearer", "litellm-virtual-key"
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(res), "run-good", "anthropic")
		if err != nil {
			t.Fatal(err)
		}
		if got.Upstream.String() != "https://llm.corp.example/anthropic" || got.AuthStyle != llmgateway.AuthBearer {
			t.Errorf("target = %+v", got)
		}
	})

	t.Run("only listed internal hosts may resolve inward", func(t *testing.T) {
		res := anthropicKey
		res.BaseURL = "http://litellm.llm.svc.cluster.local:4000"
		if _, err := resolveGatewayTarget(t.Context(), gatewayFixture(res), "run-good", "anthropic"); !errors.Is(err, llmgateway.ErrForbidden) {
			t.Fatalf("unlisted http base_url: err = %v", err)
		}
		t.Setenv("OCTOPUS_LLM_ALLOW_HTTP_HOSTS", "*.svc.cluster.local")
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(res), "run-good", "anthropic")
		if err != nil || !got.AllowPrivate {
			t.Fatalf("listed host: %+v, %v", got, err)
		}
		res.BaseURL = "https://llm.corp.example"
		got, err = resolveGatewayTarget(t.Context(), gatewayFixture(res), "run-good", "anthropic")
		if err != nil || got.AllowPrivate {
			t.Fatalf("a workspace's https endpoint must stay on the guarded transport: %+v, %v", got, err)
		}
		got, err = resolveGatewayTarget(t.Context(), gatewayFixture(anthropicKey), "run-good", "anthropic")
		if err != nil || !got.AllowPrivate {
			t.Fatalf("the operator's default upstream is trusted: %+v, %v", got, err)
		}
	})

	t.Run("openai carries org and bearer", func(t *testing.T) {
		res := secrets.ResolvedLLM{Source: "workspace_default", Engine: "codex", Credential: "sk-proj-x", OrgID: "org-9"}
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(res), "run-good", "openai")
		if err != nil {
			t.Fatal(err)
		}
		if got.Upstream.String() != "https://api.openai.com/v1" || got.AuthStyle != llmgateway.AuthBearer ||
			got.Headers["OpenAI-Organization"] != "org-9" {
			t.Errorf("target = %+v", got)
		}
	})

	deepseek := secrets.ResolvedLLM{Source: "binding", Engine: "opencode", Provider: "openai-compatible",
		Credential: "sk-deepseek", BaseURL: "https://api.deepseek.com", OrgID: "ignored"}

	t.Run("openai-compatible goes to the profile's endpoint with a bearer key", func(t *testing.T) {
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(deepseek), "run-good", "openai-compatible")
		if err != nil {
			t.Fatal(err)
		}
		if got.Upstream.String() != "https://api.deepseek.com" || got.AuthStyle != llmgateway.AuthBearer ||
			got.Credential != "sk-deepseek" || len(got.Headers) != 0 {
			t.Errorf("target = %+v", got)
		}
	})

	t.Run("opencode on an Anthropic-shaped endpoint", func(t *testing.T) {
		res := deepseek
		res.Provider, res.BaseURL, res.OrgID = "anthropic", "https://api.deepseek.com/anthropic", ""
		got, err := resolveGatewayTarget(t.Context(), gatewayFixture(res), "run-good", "anthropic")
		if err != nil {
			t.Fatal(err)
		}
		if got.Upstream.String() != "https://api.deepseek.com/anthropic" || got.AuthStyle != llmgateway.AuthXAPIKey {
			t.Errorf("target = %+v", got)
		}
	})

	refusals := []struct {
		name     string
		token    string
		provider string
		res      secrets.ResolvedLLM
		want     error
	}{
		{"unknown token", "nope", "anthropic", anthropicKey, llmgateway.ErrUnauthorized},
		{"operator key", "operator", "anthropic", anthropicKey, llmgateway.ErrForbidden},
		{"task in another workspace", "run-other-ws", "anthropic", anthropicKey, llmgateway.ErrUnauthorized},
		{"no workspace credential (never the host's)", "run-good", "anthropic",
			secrets.ResolvedLLM{Source: "host", Engine: "claude-code"}, llmgateway.ErrForbidden},
		{"provider mismatch", "run-good", "openai", anthropicKey, llmgateway.ErrForbidden},
		{"oauth token stays direct", "run-good", "anthropic",
			secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Credential: "sk-ant-oat-x"}, llmgateway.ErrForbidden},
		{"opencode profile asked for another provider", "run-good", "anthropic", deepseek, llmgateway.ErrForbidden},
		{"openai-compatible profile with no endpoint", "run-good", "openai-compatible",
			secrets.ResolvedLLM{Source: "binding", Engine: "opencode", Provider: "openai-compatible", Credential: "sk-x"},
			llmgateway.ErrForbidden},
		{"claude-code has no openai-compatible route", "run-good", "openai-compatible", anthropicKey, llmgateway.ErrForbidden},
		{"cloud profile", "run-good", "anthropic",
			secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Provider: "bedrock", Credential: "{}"}, llmgateway.ErrForbidden},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveGatewayTarget(t.Context(), gatewayFixture(c.res), c.token, c.provider)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestResolveGatewayTarget_AuditorKey(t *testing.T) {
	type call struct{ agent, fallback string }
	resolveWith := func(scope auth.RunScope, res secrets.ResolvedLLM, provider string) (call, llmgateway.Target, error) {
		var got call
		d := gatewayFixture(res)
		d.keyInfo = func(context.Context, string) *auth.APIKeyInfo {
			return &auth.APIKeyInfo{WorkspaceID: "ws-acme", Scope: scope}
		}
		d.agentEngine = func(_, agent string) string {
			if agent == "dev" {
				return "codex"
			}
			return ""
		}
		d.resolveLLM = func(_ context.Context, _, agent, fallback string) (secrets.ResolvedLLM, error) {
			got = call{agent, fallback}
			return res, nil
		}
		tgt, err := resolveGatewayTarget(t.Context(), d, "run-good", provider)
		return got, tgt, err
	}
	codex := secrets.ResolvedLLM{Source: "workspace_default", Engine: "codex", Credential: "sk-proj-x", Provider: "openai"}

	t.Run("on the audited agent's engine it resolves that agent", func(t *testing.T) {
		got, tgt, err := resolveWith(auth.RunScope{TaskID: "task1", Agent: auth.AuditorAgent, LLMAgent: "dev"}, codex, "openai")
		if err != nil {
			t.Fatal(err)
		}
		if got != (call{"dev", "codex"}) || tgt.Agent != auth.AuditorAgent {
			t.Errorf("resolved %+v for agent %q", got, tgt.Agent)
		}
	})

	t.Run("on its own engine it resolves that engine's default", func(t *testing.T) {
		got, tgt, err := resolveWith(auth.RunScope{TaskID: "task1", Agent: auth.AuditorAgent, LLMAgent: "dev", LLMEngine: "codex"}, codex, "openai")
		if err != nil {
			t.Fatal(err)
		}
		if got != (call{auth.AuditorAgent, "codex"}) || tgt.Credential != "sk-proj-x" {
			t.Errorf("resolved %+v, target %+v", got, tgt)
		}
	})

	t.Run("a key minted before these fields still falls back to the default engine", func(t *testing.T) {
		res := secrets.ResolvedLLM{Source: "workspace_default", Engine: "claude-code", Credential: "sk-ant-api-real"}
		got, _, err := resolveWith(auth.RunScope{TaskID: "task1", Agent: auth.AuditorAgent}, res, "anthropic")
		if err != nil {
			t.Fatal(err)
		}
		if got != (call{auth.AuditorAgent, "claude-code"}) {
			t.Errorf("resolved %+v", got)
		}
	})
}

func TestMountLLMGateway(t *testing.T) {
	s := &Server{srv: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})}}
	var gotPath string
	if err := s.MountLLMGateway(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest("POST", "/llm-gateway/anthropic/v1/messages", nil))
	if rec.Code != http.StatusNoContent || gotPath != "/anthropic/v1/messages" {
		t.Errorf("gateway route: code=%d path=%q", rec.Code, gotPath)
	}
	rec = httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/llm-gateway-not/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("non-gateway path reached the gateway: %d", rec.Code)
	}
}
