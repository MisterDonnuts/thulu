package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	_ "github.com/MisterDonnuts/thulu/internal/engine/codex"
	_ "github.com/MisterDonnuts/thulu/internal/engine/opencode"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func resolverReturning(res secrets.ResolvedLLM, err error) LLMResolver {
	return func(context.Context, string, string, string) (secrets.ResolvedLLM, error) { return res, err }
}

var claudeAgent = &types.AgentType{Engine: "claude-code"}

func TestApplyLLMAuth_HostCredentialOffByDefault(t *testing.T) {
	d := &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{Source: "host", Engine: "claude-code"}, nil)}
	env := map[string]string{"ENGINE": "claude-code"}
	err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "reviewer", &Task{WorkspaceID: "ws-1"})
	if err == nil || apperr.Code(err) != "task.errors.no_llm_credential" || !strings.Contains(err.Error(), "AI engines") {
		t.Fatalf("err = %v, want a refusal that sends the reader to AI engines", err)
	}
	if strings.Contains(err.Error(), "OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL") {
		t.Errorf("err = %v names the operator's variable", err)
	}

	d.WithHostLLMCredential(true)
	if err := d.applyLLMAuth(t.Context(), map[string]string{"ENGINE": "claude-code"}, "ws-1", claudeAgent, "reviewer", nil); err != nil {
		t.Fatalf("host credential allowed: %v", err)
	}

	d.WithHostLLMCredential(false)
	if err := d.applyLLMAuth(t.Context(), map[string]string{}, "", claudeAgent, "reviewer", nil); err == nil {
		t.Error("a workspace-less run fell back to the host credential")
	}
}

func TestHostLLMCredentialAllowed(t *testing.T) {
	d := &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{}, nil)}
	if d.HostLLMCredentialAllowed() {
		t.Error("allowed without the opt-in")
	}
	d.WithHostLLMCredential(true)
	if !d.HostLLMCredentialAllowed() {
		t.Error("refused with the opt-in")
	}
	if !(&Dispatcher{}).HostLLMCredentialAllowed() {
		t.Error("a dispatcher without stores refused the host credential")
	}
}

func TestApplyLLMAuth_ResolverErrorFailsTheDispatch(t *testing.T) {
	d := &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{}, errors.New("decrypt: bad key"))}
	d.WithHostLLMCredential(true)
	env := map[string]string{"ENGINE": "claude-code"}
	err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "reviewer", &Task{WorkspaceID: "ws-1"})
	if err == nil || !strings.Contains(err.Error(), "decrypt: bad key") {
		t.Fatalf("err = %v, want the resolver error", err)
	}
	if env[engine.LLMSourceEnv] != "" {
		t.Errorf("a failed resolution recorded source %q", env[engine.LLMSourceEnv])
	}
}

func TestApplyLLMAuth_NoStoreKeepsHostCredential(t *testing.T) {
	d := &Dispatcher{}
	if err := d.applyLLMAuth(t.Context(), map[string]string{}, "ws-1", claudeAgent, "reviewer", nil); err != nil {
		t.Fatal(err)
	}
}

func TestLLMReady_MatchesTheDispatch(t *testing.T) {
	d := &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{Source: "host", Engine: "claude-code"}, nil)}
	if err := d.LLMReady(t.Context(), "ws-1", claudeAgent, "qa"); err == nil || apperr.Code(err) != "task.errors.no_llm_credential" {
		t.Fatalf("no credential: err = %v, want the refusal", err)
	}
	d.WithHostLLMCredential(true)
	if err := d.LLMReady(t.Context(), "ws-1", claudeAgent, "qa"); err != nil {
		t.Fatalf("host allowed: %v", err)
	}
	d = &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{Source: "workspace_default", Engine: "claude-code", Credential: "sk-ant-x"}, nil)}
	if err := d.LLMReady(t.Context(), "ws-1", claudeAgent, "qa"); err != nil {
		t.Fatalf("resolved profile: %v", err)
	}
	d = &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{}, errors.New("decrypt: bad key"))}
	if err := d.LLMReady(t.Context(), "ws-1", claudeAgent, "qa"); err == nil {
		t.Fatal("a resolver error read as ready")
	}
}

func gatewayDispatcher(res secrets.ResolvedLLM) *Dispatcher {
	d := &Dispatcher{llmResolver: resolverReturning(res, nil), keys: stubKeys{}, agentBaseURL: "http://octopus.octopus.svc:8585"}
	return d.WithLLMGateway("")
}

type stubKeys struct{}

func (stubKeys) MintRunKey(context.Context, auth.RunScope, string, time.Duration) (string, string, error) {
	return "oct_run", "key-1", nil
}
func (stubKeys) RevokeAPIKey(context.Context, string) error { return nil }

func TestApplyLLMAuth_GatewayKeepsTheKeyOut(t *testing.T) {
	const real = "sk-ant-api-REAL"
	d := gatewayDispatcher(secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Credential: real,
		BaseURL: "https://litellm.corp", AuthHeader: "bearer", OrgID: "org-x",
	})
	env := map[string]string{"ENGINE": "claude-code", "OCTOPUS_API_KEY": "oct_run_token", "CLAUDE_CODE_OAUTH_TOKEN": "stale"}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", &Task{WorkspaceID: "ws-1"}); err != nil {
		t.Fatal(err)
	}
	for k, v := range env {
		if strings.Contains(v, real) {
			t.Errorf("%s carries the workspace key", k)
		}
	}
	if env["ANTHROPIC_BASE_URL"] != "http://octopus.octopus.svc:8585/llm-gateway/anthropic" {
		t.Errorf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_API_KEY"] != "oct_run_token" || !engine.ViaGateway(env) {
		t.Errorf("gateway env = %v", env)
	}
	for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s survived the gateway route", k)
		}
	}
}

func TestApplyLLMAuth_GatewayRoutes(t *testing.T) {
	t.Run("codex through /openai", func(t *testing.T) {
		d := gatewayDispatcher(secrets.ResolvedLLM{Source: "binding", Engine: "codex", Credential: "sk-proj-real"})
		d.WithLLMGateway("https://gw.example/llm-gateway/")
		env := map[string]string{"ENGINE": "codex", "OCTOPUS_API_KEY": "oct_run"}
		if err := d.applyLLMAuth(t.Context(), env, "ws-1", &types.AgentType{Engine: "codex"}, "dev", &Task{}); err != nil {
			t.Fatal(err)
		}
		if env["OPENAI_BASE_URL"] != "https://gw.example/llm-gateway/openai" || env["OPENAI_API_KEY"] != "oct_run" {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("oauth token stays direct", func(t *testing.T) {
		d := gatewayDispatcher(secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Credential: "sk-ant-oat-sub"})
		env := map[string]string{"ENGINE": "claude-code", "OCTOPUS_API_KEY": "oct_run"}
		if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", &Task{}); err != nil {
			t.Fatal(err)
		}
		if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat-sub" || engine.ViaGateway(env) {
			t.Errorf("env = %v", env)
		}
	})
	t.Run("task run without a run key refuses", func(t *testing.T) {
		d := gatewayDispatcher(secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Credential: "sk-ant-api-x"})
		err := d.applyLLMAuth(t.Context(), map[string]string{"ENGINE": "claude-code"}, "ws-1", claudeAgent, "dev", &Task{})
		if err == nil {
			t.Fatal("a task run without a token was sent to the gateway")
		}
	})
	t.Run("chat turn without the key still marks the gateway", func(t *testing.T) {
		d := gatewayDispatcher(secrets.ResolvedLLM{Source: "binding", Engine: "claude-code", Credential: "sk-ant-api-x"})
		env := map[string]string{"ENGINE": "claude-code"}
		if err := d.ApplyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev"); err != nil {
			t.Fatal(err)
		}
		if !engine.ViaGateway(env) || env["ANTHROPIC_API_KEY"] != "" {
			t.Errorf("env = %v", env)
		}
	})
}

func TestApplyLLMAuth_OpenCodeProviders(t *testing.T) {
	agent := &types.AgentType{Engine: "opencode"}
	deepseek := secrets.ResolvedLLM{Source: "binding", Engine: "opencode", Provider: "openai-compatible",
		Credential: "sk-deepseek-real", BaseURL: "https://api.deepseek.com"}

	t.Run("through the gateway", func(t *testing.T) {
		for _, c := range []struct{ provider, base string }{
			{"openai-compatible", "https://api.deepseek.com"},
			{"anthropic", "https://api.deepseek.com/anthropic"},
			{"openai", ""},
			{"", "https://api.deepseek.com"},
		} {
			res := deepseek
			res.Provider, res.BaseURL = c.provider, c.base
			d := gatewayDispatcher(res)
			env := map[string]string{"ENGINE": "opencode", "OCTOPUS_API_KEY": "oct_run", "OCTOPUS_LLM_PROVIDER": "stale"}
			if err := d.applyLLMAuth(t.Context(), env, "ws-1", agent, "dev", &Task{}); err != nil {
				t.Fatal(err)
			}
			want := c.provider
			if want == "" {
				want = "openai-compatible"
			}
			if env["OCTOPUS_LLM_BASE_URL"] != "http://octopus.octopus.svc:8585/llm-gateway/"+want ||
				env["OCTOPUS_LLM_PROVIDER"] != want || env["LLM_API_KEY"] != "oct_run" || !engine.ViaGateway(env) {
				t.Errorf("provider %q: env = %v", c.provider, env)
			}
			for k, v := range env {
				if strings.Contains(v, "sk-deepseek-real") {
					t.Errorf("provider %q: %s carries the workspace key", c.provider, k)
				}
			}
		}
	})

	t.Run("direct", func(t *testing.T) {
		d := &Dispatcher{llmResolver: resolverReturning(deepseek, nil)}
		env := map[string]string{"ENGINE": "opencode"}
		if err := d.applyLLMAuth(t.Context(), env, "ws-1", agent, "dev", nil); err != nil {
			t.Fatal(err)
		}
		if env["OCTOPUS_LLM_BASE_URL"] != "https://api.deepseek.com" || env["OCTOPUS_LLM_PROVIDER"] != "openai-compatible" ||
			env["LLM_API_KEY"] != "sk-deepseek-real" {
			t.Errorf("env = %v", env)
		}
	})

	t.Run("a profile on another engine clears the provider", func(t *testing.T) {
		d := &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{
			Source: "binding", Engine: "claude-code", Credential: "sk-ant-api-x",
		}, nil)}
		env := map[string]string{"ENGINE": "opencode", "OCTOPUS_LLM_PROVIDER": "anthropic", "OCTOPUS_LLM_BASE_URL": "https://x"}
		if err := d.applyLLMAuth(t.Context(), env, "ws-1", agent, "dev", nil); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"OCTOPUS_LLM_PROVIDER", "OCTOPUS_LLM_BASE_URL"} {
			if _, ok := env[k]; ok {
				t.Errorf("%s survived the switch to claude-code: %v", k, env)
			}
		}
	})
}

func TestApplyLLMAuth_CustomEndpointAuthHeader(t *testing.T) {
	for _, c := range []struct {
		header, wantVar, goneVar string
	}{
		{"bearer", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"},
		{"", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		{"x-api-key", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
	} {
		d := &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{
			Source: "binding", Engine: "claude-code", Credential: "litellm-virtual-123",
			BaseURL: "https://litellm.corp", AuthHeader: c.header,
		}, nil)}
		env := map[string]string{"ENGINE": "claude-code", c.goneVar: "stale"}
		if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", nil); err != nil {
			t.Fatal(err)
		}
		if env[c.wantVar] != "litellm-virtual-123" {
			t.Errorf("auth_header=%q: %s = %q", c.header, c.wantVar, env[c.wantVar])
		}
		if _, ok := env[c.goneVar]; ok {
			t.Errorf("auth_header=%q: %s not cleared", c.header, c.goneVar)
		}
		if _, ok := env["LLM_API_KEY"]; ok {
			t.Errorf("auth_header=%q: fell back to LLM_API_KEY", c.header)
		}
	}
}

func TestApplyLLMAuth_CloudProfiles(t *testing.T) {
	secret, _ := engine.CloudSecret{AWSAccessKeyID: "AKIA1", AWSSecretAccessKey: "s3cr3t"}.Encode()
	bedrock := secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Provider: "bedrock", Region: "eu-west-1", Credential: secret,
	}

	d := &Dispatcher{llmResolver: resolverReturning(bedrock, nil)}
	env := map[string]string{"ENGINE": "claude-code", "ANTHROPIC_API_KEY": "stale"}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", nil); err != nil {
		t.Fatal(err)
	}
	if env["CLAUDE_CODE_USE_BEDROCK"] != "1" || env["AWS_REGION"] != "eu-west-1" || env["AWS_ACCESS_KEY_ID"] != "AKIA1" {
		t.Errorf("bedrock env = %v", env)
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok || engine.ViaGateway(env) {
		t.Errorf("direct bedrock run kept a provider key or claims the gateway: %v", env)
	}

	d = &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{
		Source: "binding", Engine: "codex", Provider: "bedrock", Region: "us-east-1", Credential: secret,
	}, nil)}
	if err := d.applyLLMAuth(t.Context(), map[string]string{}, "ws-1", claudeAgent, "dev", nil); err == nil {
		t.Error("codex accepted a bedrock profile")
	}
}

func TestApplyLLMAuth_CloudProfileThroughTheGateway(t *testing.T) {
	secret, _ := engine.CloudSecret{
		AWSAccessKeyID: "AKIA1", AWSSecretAccessKey: "s3cr3t", AWSSessionToken: "tok",
	}.Encode()
	d := &Dispatcher{llmResolver: resolverReturning(secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Provider: "bedrock", Region: "eu-west-1", Credential: secret,
	}, nil)}
	d.keys = stubKeys{}
	d.WithLLMGateway("http://gw/llm-gateway")
	env := map[string]string{"ENGINE": "claude-code", "ANTHROPIC_API_KEY": "stale", "OCTOPUS_API_KEY": "oct_run"}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", &Task{}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"CLAUDE_CODE_USE_BEDROCK":       "1",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH": "1",
		"ANTHROPIC_BEDROCK_BASE_URL":    "http://gw/llm-gateway/bedrock",
		"AWS_REGION":                    "eu-west-1",
		"ANTHROPIC_AUTH_TOKEN":          "oct_run",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK", "ANTHROPIC_API_KEY"} {
		if v, ok := env[k]; ok {
			t.Errorf("%s reached the run: %q", k, v)
		}
	}
	if !engine.ViaGateway(env) {
		t.Error("run not marked as served by the gateway")
	}

	sa := `{"type":"service_account","client_email":"a@p.iam.gserviceaccount.com","private_key":"k"}`
	vsecret, _ := engine.CloudSecret{GCPServiceAccountJSON: sa}.Encode()
	d.llmResolver = resolverReturning(secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Provider: "vertex", Region: "us-east5", ProjectID: "p1", Credential: vsecret,
	}, nil)
	env = map[string]string{"ENGINE": "claude-code", "OCTOPUS_API_KEY": "oct_run"}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", &Task{}); err != nil {
		t.Fatal(err)
	}
	if env["CLAUDE_CODE_SKIP_VERTEX_AUTH"] != "1" || env["ANTHROPIC_VERTEX_BASE_URL"] != "http://gw/llm-gateway/vertex" ||
		env["ANTHROPIC_VERTEX_PROJECT_ID"] != "p1" || env["CLOUD_ML_REGION"] != "us-east5" {
		t.Errorf("vertex env = %v", env)
	}
	if _, ok := env[claudecode.VertexKeyFileEnv]; ok {
		t.Error("the service account key reached the run")
	}
}

func TestApplyLLMAuth_AmbientCloudIdentityNeedsOptIn(t *testing.T) {
	ambient := secrets.ResolvedLLM{
		Source: "binding", Engine: "claude-code", Provider: "vertex", Region: "us-east5", ProjectID: "p1", ProfileName: "vx",
	}
	d := &Dispatcher{llmResolver: resolverReturning(ambient, nil)}
	t.Setenv(engine.EnvAmbientCloudIdentity, "")
	err := d.applyLLMAuth(t.Context(), map[string]string{"ENGINE": "claude-code"}, "ws-1", claudeAgent, "dev", nil)
	if err == nil || !strings.Contains(err.Error(), engine.EnvAmbientCloudIdentity) {
		t.Fatalf("err = %v, want a refusal naming %s", err, engine.EnvAmbientCloudIdentity)
	}

	t.Setenv(engine.EnvAmbientCloudIdentity, "1")
	env := map[string]string{"ENGINE": "claude-code"}
	if err := d.applyLLMAuth(t.Context(), env, "ws-1", claudeAgent, "dev", nil); err != nil {
		t.Fatal(err)
	}
	if env["CLAUDE_CODE_USE_VERTEX"] != "1" || env["ANTHROPIC_VERTEX_PROJECT_ID"] != "p1" {
		t.Errorf("vertex env = %v", env)
	}
}

func TestMentionLaunchFailsWhenNoLLMCredential(t *testing.T) {
	d, store, l := newMentionDispatcher(t)
	d.WithLLMResolver(resolverReturning(secrets.ResolvedLLM{Source: "host", Engine: "claude-code"}, nil), nil, nil)
	tk := &Task{WorkspaceID: testWS, Title: "no key"}
	if err := store.Create(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	full, _ := store.Get(t.Context(), tk.ID)
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c1", Body: "hola"})
	if n := len(l.launches()); n != 0 {
		t.Fatalf("launched %d runs without a credential", n)
	}
	comments, err := store.ListComments(t.Context(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range comments {
		if strings.Contains(c.Body, "AI engines") && !strings.Contains(c.Body, "OCTOPUS_ALLOW_HOST_LLM_CREDENTIAL") {
			found = true
		}
	}
	if !found {
		t.Errorf("no comment explains the failure: %+v", comments)
	}

	d.WithHostLLMCredential(true)
	d.HandleMention(context.Background(), full, mentionAgent(), &Comment{ID: "c2", Body: "what now?"})
	if n := len(l.launches()); n != 1 {
		t.Fatalf("launches after opt-in = %d, want 1", n)
	}
}

func TestAgentEgressEnv(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	t.Run("claude-code telemetry off by default", func(t *testing.T) {
		out := agentEgressEnv(env(nil), "claude-code")
		for _, k := range []string{"DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING", "OCTOPUS_DISABLE_NONESSENTIAL_TRAFFIC"} {
			if out[k] != "1" {
				t.Errorf("%s = %q", k, out[k])
			}
		}
		if _, ok := out["HTTP_PROXY"]; ok {
			t.Error("proxy set with none configured")
		}
	})
	t.Run("opt out", func(t *testing.T) {
		out := agentEgressEnv(env(map[string]string{"OCTOPUS_CLAUDE_CODE_NONESSENTIAL_TRAFFIC": "1"}), "claude-code")
		if len(out) != 0 {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("codex gets the shell switch", func(t *testing.T) {
		out := agentEgressEnv(env(nil), "codex")
		if out["OCTOPUS_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("inherits the daemon proxy and exempts the daemon", func(t *testing.T) {
		out := agentEgressEnv(env(map[string]string{
			"https_proxy": "http://proxy.corp:3128",
			"NO_PROXY":    "internal.corp,localhost",
		}), "claude-code", "http://octopus.octopus.svc:8585", "http://octopus.octopus.svc:8585/llm-gateway")
		if out["HTTPS_PROXY"] != "http://proxy.corp:3128" || out["https_proxy"] != "http://proxy.corp:3128" {
			t.Errorf("https proxy = %q/%q", out["HTTPS_PROXY"], out["https_proxy"])
		}
		want := "internal.corp,localhost,octopus.octopus.svc,127.0.0.1,::1,host.docker.internal,.svc,.cluster.local"
		if out["NO_PROXY"] != want || out["no_proxy"] != want {
			t.Errorf("NO_PROXY = %q, want %q", out["NO_PROXY"], want)
		}
	})
	t.Run("explicit agent proxy wins; inheritance can be cut", func(t *testing.T) {
		out := agentEgressEnv(env(map[string]string{
			"HTTP_PROXY": "http://daemon-proxy", "OCTOPUS_AGENT_HTTP_PROXY": "http://agent-proxy",
		}), "claude-code")
		if out["HTTP_PROXY"] != "http://agent-proxy" {
			t.Errorf("HTTP_PROXY = %q", out["HTTP_PROXY"])
		}
		out = agentEgressEnv(env(map[string]string{"HTTP_PROXY": "http://daemon-proxy", "OCTOPUS_AGENT_PROXY_INHERIT": "0"}), "claude-code")
		if _, ok := out["HTTP_PROXY"]; ok {
			t.Error("inherited a proxy with inheritance off")
		}
	})
	t.Run("CA bundle and GitHub Enterprise", func(t *testing.T) {
		out := agentEgressEnv(env(map[string]string{
			"OCTOPUS_EXTRA_CA_BUNDLE": "/etc/ssl/corp.pem",
			"OCTOPUS_GITHUB_API_URL":  "https://ghe.corp/api/v3/",
		}), "claude-code")
		if out["NODE_EXTRA_CA_CERTS"] != "/etc/octopus/ca.crt" || out["GITHUB_API_URL"] != "https://ghe.corp/api/v3" {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("GitHub Enterprise from the web URL alone", func(t *testing.T) {
		out := agentEgressEnv(env(map[string]string{"OCTOPUS_GITHUB_URL": "https://ghe.corp/"}), "claude-code")
		if out["GITHUB_API_URL"] != "https://ghe.corp/api/v3" || out["GITHUB_SERVER_URL"] != "https://ghe.corp" {
			t.Errorf("out = %v", out)
		}
	})
	t.Run("nothing forwarded for github.com", func(t *testing.T) {
		out := agentEgressEnv(env(nil), "claude-code")
		if _, ok := out["GITHUB_API_URL"]; ok {
			t.Errorf("out = %v", out)
		}
	})
}

func TestLLMCredentialReady(t *testing.T) {
	touched := 0
	d := &Dispatcher{
		llmResolver:  resolverReturning(secrets.ResolvedLLM{Source: "host", Engine: "claude-code"}, nil),
		llmProfTouch: func(context.Context, string, string) { touched++ },
	}
	if err := d.LLMCredentialReady(t.Context(), "ws-1", claudeAgent, "reviewer"); err == nil {
		t.Fatal("a workspace with no credential reported ready")
	}

	d.llmResolver = resolverReturning(secrets.ResolvedLLM{
		Source: "profile", Engine: "claude-code", ProfileID: "p-1", Credential: "sk-ant-api-test",
	}, nil)
	if err := d.LLMCredentialReady(t.Context(), "ws-1", claudeAgent, "reviewer"); err != nil {
		t.Fatalf("a workspace with a profile: %v", err)
	}
	if touched != 0 {
		t.Errorf("the check touched the profile %d times", touched)
	}
}
