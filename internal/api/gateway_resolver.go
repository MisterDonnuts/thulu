package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

type gatewayDeps struct {
	keyInfo       func(ctx context.Context, token string) *auth.APIKeyInfo
	taskWorkspace func(ctx context.Context, taskID string) (string, bool)
	agentEngine   func(workspaceID, agent string) string
	resolveLLM    func(ctx context.Context, workspaceID, agent, fallbackEngine string) (secrets.ResolvedLLM, error)
	upstreams     map[string]string
}

// Fails closed at every link and never falls back to the host credential: a run with no workspace
// key gets 403.
// Liveness is the key being active: the dispatcher revokes it on run exit, with a TTL for lost
// exits.
func resolveGatewayTarget(ctx context.Context, d gatewayDeps, token, provider string) (llmgateway.Target, error) {
	info, err := liveRunKey(ctx, d, token)
	if err != nil {
		return llmgateway.Target{}, err
	}
	scope := info.Scope
	llmAgent := firstNonEmpty(scope.LLMAgent, scope.Agent)
	if scope.LLMEngine != "" {
		// On another engine the audited agent's bound profile does not apply.
		llmAgent = scope.Agent
	}
	fallback := firstNonEmpty(scope.LLMEngine, d.agentEngine(info.WorkspaceID, llmAgent), engine.DefaultName)
	res, err := d.resolveLLM(ctx, info.WorkspaceID, llmAgent, fallback)
	if err != nil {
		return llmgateway.Target{}, apperr.Wrap(err, "api.gateway.errors.resolve_llm_failed", "agent", scope.Agent)
	}
	if res.Source == "host" || (res.Credential == "" && !engine.IsCloudProvider(res.Provider)) {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.no_llm_credential")
	}
	engineName := res.Engine
	if engineName == "" {
		engineName = fallback
	}
	eng, err := engine.Get(engineName)
	if err != nil {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden,
			"api.gateway.errors.unknown_engine", "engine", engineName)
	}
	caps := eng.Capabilities()
	if engine.IsCloudProvider(res.Provider) || engine.IsCloudProvider(provider) {
		return resolveCloudGatewayTarget(res, provider, caps, llmgateway.Target{
			WorkspaceID: info.WorkspaceID,
			RunID:       firstNonEmpty(scope.TaskID, scope.ChatID),
			Agent:       scope.Agent,
			ProfileID:   res.ProfileID,
		})
	}
	if profileProvider := caps.ProfileProvider(res.Provider); profileProvider != provider {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.profile_wrong_provider",
			"profile_provider", profileProvider, "provider", provider)
	}
	if !caps.GatewayCanPresent(res.Provider, res.Credential, res.BaseURL) {
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.credential_not_served")
	}
	if engine.ProviderNeedsBaseURL(provider) && strings.TrimSpace(res.BaseURL) == "" {
		// Profile validation refuses this now; rows written before it still exist.
		return llmgateway.Target{}, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden,
			"api.gateway.errors.base_url_required", "provider", provider)
	}
	upstream, allowPrivate, err := gatewayUpstream(res.BaseURL, d.upstreams[provider])
	if err != nil {
		return llmgateway.Target{}, err
	}

	t := llmgateway.Target{
		Upstream:     upstream,
		AllowPrivate: allowPrivate,
		Credential:   res.Credential,
		AuthStyle:    llmgateway.AuthXAPIKey,
		WorkspaceID:  info.WorkspaceID,
		RunID:        firstNonEmpty(scope.TaskID, scope.ChatID),
		Agent:        scope.Agent,
		ProfileID:    res.ProfileID,
	}
	switch provider {
	case llmgateway.ProviderOpenAI:
		t.AuthStyle = llmgateway.AuthBearer
		if res.OrgID != "" {
			t.Headers = map[string]string{"OpenAI-Organization": res.OrgID}
		}
	case llmgateway.ProviderOpenAICompatible:
		t.AuthStyle = llmgateway.AuthBearer
	default:
		if res.AuthHeader == engine.AuthHeaderBearer {
			t.AuthStyle = llmgateway.AuthBearer
		}
	}
	return t, nil
}

func liveRunKey(ctx context.Context, d gatewayDeps, token string) (*auth.APIKeyInfo, error) {
	info := d.keyInfo(ctx, token)
	if info == nil {
		return nil, llmgateway.ErrUnauthorized
	}
	if !info.Scope.IsRun() || info.Scope.Agent == "" || info.WorkspaceID == "" {
		// Operator keys have no agent or profile; letting them through would let any API key spend
		// a workspace's model budget.
		return nil, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.run_keys_only")
	}
	if info.Scope.TaskID != "" {
		ws, ok := d.taskWorkspace(ctx, info.Scope.TaskID)
		if !ok || ws != info.WorkspaceID {
			return nil, llmgateway.ErrUnauthorized
		}
	}
	return info, nil
}

// Re-validated per request so removing a host from OCTOPUS_LLM_ALLOW_HTTP_HOSTS takes effect at
// once.
// Operator defaults may reach private addresses; a workspace admin's base_url only if its host is
// listed.
func gatewayUpstream(profileBase, providerDefault string) (u *url.URL, allowPrivate bool, err error) {
	base := strings.TrimSpace(profileBase)
	if base == "" {
		u, err = url.Parse(providerDefault)
		if err != nil || u.Host == "" {
			return nil, false, apperr.Internal("api.gateway.errors.default_upstream_invalid", "url", providerDefault)
		}
		return u, true, nil
	}
	if verr := validateProfileBaseURL(base); verr != nil {
		return nil, false, fmt.Errorf("%w: %w", llmgateway.ErrForbidden, verr)
	}
	u, err = url.Parse(base)
	if err != nil {
		return nil, false, apperr.WrapKind(apperr.KindForbidden, llmgateway.ErrForbidden, "api.gateway.errors.endpoint_invalid")
	}
	return u, llmHostListed(u.Hostname()), nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) LLMGatewayResolver(upstreams map[string]string) llmgateway.Resolver {
	d := gatewayDeps{
		keyInfo: func(ctx context.Context, token string) *auth.APIKeyInfo {
			if s.authStore == nil {
				return nil
			}
			return s.authStore.LookupAPIKeyInfo(ctx, token)
		},
		taskWorkspace: func(ctx context.Context, taskID string) (string, bool) {
			if s.taskStore == nil {
				return "", false
			}
			t, err := s.taskStore.Get(ctx, taskID)
			if err != nil || t == nil {
				return "", false
			}
			return t.WorkspaceID, true
		},
		agentEngine: func(workspaceID, agent string) string {
			if at := s.lookupAgentType(workspaceID, agent); at != nil {
				return at.Engine
			}
			return ""
		},
		resolveLLM: s.resolveLLMForAgent,
		upstreams:  upstreams,
	}
	return func(ctx context.Context, token, provider string) (llmgateway.Target, error) {
		return resolveGatewayTarget(ctx, d, token, provider)
	}
}

// Mounted ahead of the API middleware: the operator-key gate would refuse run tokens, the request
// timeout would cut model streams,
// and the per-IP rate limit would count every agent on a Docker host as one client.
func (s *Server) MountLLMGateway(h http.Handler) error {
	if s.srv == nil || s.srv.Handler == nil {
		return apperr.Internal("api.gateway.errors.server_not_built")
	}
	api := s.srv.Handler
	gw := http.StripPrefix(llmgateway.MountPath, h)
	prefix := llmgateway.MountPath + "/"
	s.srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, prefix) {
			gw.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
	return nil
}
