package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/secrets"
)

const anthropicAPIVersion = "2023-06-01"

func (s *Server) SetLLMUpstreams(upstreams map[string]string) {
	s.llmUpstreams = upstreams
}

func (s *Server) profileKeyTarget(desc engineDescriptor, p *secrets.LLMProfile, rawKey string) (llmgateway.Target, string, bool, error) {
	provider := p.Provider
	if provider == "" {
		provider = desc.Provider
	}
	if rawKey == "" || engine.IsCloudProvider(provider) {
		return llmgateway.Target{}, "", false, nil
	}
	if eng, err := engine.Get(desc.Name); err != nil || !eng.Capabilities().GatewayCanPresent(provider, rawKey, p.BaseURL) {
		return llmgateway.Target{}, "", false, err
	}
	upstream, allowPrivate, err := gatewayUpstream(p.BaseURL, s.llmUpstreams[provider])
	if err != nil {
		return llmgateway.Target{}, "", false, err
	}
	t := llmgateway.Target{Upstream: upstream, AllowPrivate: allowPrivate, Credential: rawKey, AuthStyle: llmgateway.AuthXAPIKey}
	switch provider {
	case engine.ProviderOpenAI:
		t.AuthStyle = llmgateway.AuthBearer
		if p.OrgID != "" {
			t.Headers = map[string]string{"OpenAI-Organization": p.OrgID}
		}
		// The OpenAI upstream base already ends in /v1.
		return t, "/models", true, nil
	case engine.ProviderOpenAICompatible:
		// base_url is the API root that /chat/completions hangs off; compatible servers (DeepSeek,
		// OpenRouter, vLLM, Ollama) serve /models beside it.
		t.AuthStyle = llmgateway.AuthBearer
		return t, "/models", true, nil
	case engine.ProviderAnthropic:
		if p.AuthHeader == engine.AuthHeaderBearer {
			t.AuthStyle = llmgateway.AuthBearer
		}
		t.Headers = map[string]string{"anthropic-version": anthropicAPIVersion}
		return t, "/v1/models", true, nil
	}
	return llmgateway.Target{}, "", false, nil
}

func (s *Server) checkProfileKey(ctx context.Context, desc engineDescriptor, p *secrets.LLMProfile, rawKey string) secrets.KeyCheck {
	if s.llmUpstreams == nil {
		return secrets.KeyCheck{State: secrets.KeyUnchecked, Reason: secrets.KeyNoUpstream}
	}
	target, path, ok, err := s.profileKeyTarget(desc, p, rawKey)
	if err != nil {
		return secrets.KeyCheck{State: secrets.KeyUnreachable, Reason: secrets.KeyEndpointRefused}
	}
	if !ok {
		return secrets.KeyCheck{State: secrets.KeyUnchecked, Reason: secrets.KeyNotCheckable}
	}
	status, err := llmgateway.Probe(ctx, target, path)
	switch {
	case err != nil:
		logx.Warn(err, "api.llm.log.key_check_failed", "profile", p.ID, "host", target.Upstream.Host)
		return secrets.KeyCheck{State: secrets.KeyUnreachable, Reason: secrets.KeyNoAnswer, Host: target.Upstream.Host}
	case status >= 200 && status < 300:
		return secrets.KeyCheck{State: secrets.KeyValid}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return secrets.KeyCheck{State: secrets.KeyInvalid, Reason: secrets.KeyRejected, Status: status}
	default:
		return secrets.KeyCheck{State: secrets.KeyUnreachable, Reason: secrets.KeyUnexpected, Status: status}
	}
}

func (s *Server) recordProfileKeyCheck(ctx context.Context, desc engineDescriptor, p *secrets.LLMProfile, rawKey string) *secrets.LLMProfile {
	if err := s.secretsStore.SetLLMProfileKeyCheck(ctx, p.WorkspaceID, p.ID, s.checkProfileKey(ctx, desc, p, rawKey)); err != nil {
		logx.Warn(err, "api.llm.log.key_check_store_failed", "profile", p.ID)
		return p
	}
	if fresh, err := s.secretsStore.GetLLMProfile(ctx, p.WorkspaceID, p.ID); err == nil {
		return fresh
	}
	return p
}

func (s *Server) recheckUpdatedProfile(
	ctx context.Context, desc engineDescriptor, before, after *secrets.LLMProfile, rawKey string,
) *secrets.LLMProfile {
	moved := after.BaseURL != before.BaseURL || after.OrgID != before.OrgID ||
		after.Provider != before.Provider || after.AuthHeader != before.AuthHeader
	if rawKey == "" && !moved {
		return after
	}
	if rawKey == "" && after.HasKey {
		var err error
		if rawKey, err = s.secretsStore.GetLLMProfileCredential(ctx, after.WorkspaceID, after.ID); err != nil {
			logx.Warn(err, "api.llm.log.key_check_read_failed", "profile", after.ID)
		}
	}
	return s.recordProfileKeyCheck(ctx, desc, after, rawKey)
}

func (s *Server) handleCheckLLMProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wsID, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	prof, err := s.secretsStore.GetLLMProfile(r.Context(), wsID, id)
	if errors.Is(err, secrets.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.llm.errors.profile_not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	desc, ok := engineByName(r.Context(), prof.Engine)
	if !ok {
		writeErrKey(w, r, http.StatusBadRequest, "api.llm.errors.unknown_engine", "engine", prof.Engine)
		return
	}
	rawKey := ""
	if prof.HasKey {
		if rawKey, err = s.secretsStore.GetLLMProfileCredential(r.Context(), wsID, id); err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.recordProfileKeyCheck(r.Context(), desc, prof, rawKey))
}

// A profile not seen since startup counts as maybe-refused: the stored refusal may predate the
// restart.
type profileUsage struct {
	mu      sync.Mutex
	refused map[string]bool
}

func (s *Server) LLMGatewayOutcome(ctx context.Context, t llmgateway.Target, status int) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
	case status >= 200 && status < 300:
		status = 0
	default:
		return
	}
	s.RecordLLMProfileRefusal(ctx, t.WorkspaceID, t.ProfileID, status)
}

func (s *Server) RecordLLMProfileRefusal(ctx context.Context, workspaceID, profileID string, status int) {
	if profileID == "" || workspaceID == "" || s.secretsStore == nil {
		return
	}
	refused := status != 0
	u := &s.llmUsage
	u.mu.Lock()
	known, seen := u.refused[profileID]
	u.mu.Unlock()
	if seen && known == refused {
		return
	}
	// Detached: the run's request may be gone by the time the response finished streaming.
	if _, err := s.secretsStore.SetLLMProfileRefusal(context.WithoutCancel(ctx), workspaceID, profileID, status); err != nil {
		logx.Warn(err, "api.llm.log.refusal_store_failed", "profile", profileID)
		return
	}
	u.mu.Lock()
	if u.refused == nil {
		u.refused = map[string]bool{}
	}
	u.refused[profileID] = refused
	u.mu.Unlock()
}
