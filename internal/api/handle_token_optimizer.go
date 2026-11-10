package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/llmgateway"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/types"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type tokenOptDeps struct {
	worker tokenopt.Worker
}

func (s *Server) SetTokenOptimizer() {
	if s.tokenOpt.worker.Send == nil {
		s.tokenOpt.worker.Send = llmgateway.Send
	}
}

type tokenOptWorker struct {
	info     tokenopt.WorkerInfo
	oauth    string
	unusable error
}

func (s *Server) lookupTokenOptWorker(ctx context.Context, ws, engineName string) (tokenOptWorker, error) {
	w := tokenOptWorker{info: tokenopt.WorkerInfo{Engine: engineName}}
	var p *secrets.LLMProfile
	if s.secretsStore != nil && engineName != "" {
		var err error
		p, err = s.secretsStore.DefaultLLMProfile(ctx, ws, engineName)
		if err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return w, err
		}
	}
	if p == nil {
		w.info.Reason = tokenopt.WorkerNoDefaultProfile
		w.unusable = apperr.Wrap(tokenopt.ErrNoWorker, "api.token_optimizer.errors.no_default_profile", "engine", engineName)
		return w, nil
	}
	w.info.ProfileID, w.info.ProfileName, w.info.Provider = p.ID, p.Name, profileProvider(ctx, p)
	via, err := s.workerVia(ctx, ws, p.ID)
	if err != nil {
		w.info.Reason, w.unusable = tokenopt.WorkerUnsupported, err
		return w, nil
	}
	if via == tokenopt.ViaCLI {
		if w.oauth, err = s.secretsStore.GetLLMProfileCredential(ctx, ws, p.ID); err != nil {
			return w, err
		}
	}
	w.info.Available, w.info.Via = true, via
	return w, nil
}

func (s *Server) resolveTokenOptimizer(ctx context.Context, ws string, at *types.AgentType) (tokenopt.Resolved, tokenOptWorker, error) {
	if at == nil || !at.TokenOptimizer.On() {
		return tokenopt.Resolve(at, "", tokenopt.WorkerInfo{}), tokenOptWorker{}, nil
	}
	w, err := s.lookupTokenOptWorker(ctx, ws, at.TokenOptimizer.Engine)
	if err != nil {
		return tokenopt.Resolve(nil, "", tokenopt.WorkerInfo{}), w, err
	}
	return tokenopt.Resolve(at, s.runEngine(ctx, ws, at), w.info), w, nil
}

func (s *Server) tokenOptimizerWorker(ctx context.Context, ws, engineName string) (tokenopt.WorkerInfo, string, error) {
	w, err := s.lookupTokenOptWorker(ctx, ws, engineName)
	return w.info, w.oauth, err
}

type workersResponse struct {
	Workers []tokenopt.WorkerInfo `json:"workers"`
}

func (s *Server) handleTokenOptimizerWorkers(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	engines := availableEngines(r.Context())
	out := workersResponse{Workers: make([]tokenopt.WorkerInfo, 0, len(engines))}
	for i := range engines {
		worker, err := s.lookupTokenOptWorker(r.Context(), ws, engines[i].Name)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		out.Workers = append(out.Workers, worker.info)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) workspaceProfile(ctx context.Context, ws, id string) *secrets.LLMProfile {
	if s.secretsStore == nil || id == "" {
		return nil
	}
	p, err := s.secretsStore.GetLLMProfile(ctx, ws, id)
	if err != nil {
		return nil
	}
	return p
}

func profileProvider(ctx context.Context, p *secrets.LLMProfile) string {
	if p.Provider != "" {
		return p.Provider
	}
	desc, _ := engineByName(ctx, p.Engine)
	return desc.Provider
}

func (s *Server) workerVia(ctx context.Context, ws, id string) (string, error) {
	p := s.workspaceProfile(ctx, ws, id)
	if p == nil {
		return "", apperr.Wrap(tokenopt.ErrNoWorker, "api.token_optimizer.errors.profile_not_found", "profile", id)
	}
	if s.claudeSubscriptionKey(ctx, ws, p) {
		// On another engine the daemon would send the subscription token to that engine's API.
		if p.Engine != "" && p.Engine != tokenopt.CLIEngine {
			return "", tokenopt.ErrProfileNotUsable
		}
		return tokenopt.ViaCLI, nil
	}
	if _, _, _, err := s.workerProfile(ctx, ws, id); err != nil {
		return "", err
	}
	return tokenopt.ViaAPI, nil
}

func (s *Server) claudeSubscriptionKey(ctx context.Context, ws string, p *secrets.LLMProfile) bool {
	eng, err := engine.Get(tokenopt.CLIEngine)
	if err != nil {
		return false
	}
	rawKey, err := s.secretsStore.GetLLMProfileCredential(ctx, ws, p.ID)
	if err != nil || rawKey == "" {
		return false
	}
	kind, ok := engine.MatchKeyKind(eng.Capabilities().KeyKinds, rawKey)
	return ok && kind.NoGateway
}

func (s *Server) workerProfile(ctx context.Context, ws, id string) (*secrets.LLMProfile, string, string, error) {
	p := s.workspaceProfile(ctx, ws, id)
	if p == nil {
		return nil, "", "", apperr.Wrap(tokenopt.ErrNoWorker, "api.token_optimizer.errors.profile_not_found", "profile", id)
	}
	provider := profileProvider(ctx, p)
	if !tokenopt.SupportedProvider(provider) {
		return nil, "", "", apperr.Wrap(tokenopt.ErrProviderUnsupported, "tokenopt.errors.provider_named", "provider", provider)
	}
	rawKey, err := s.secretsStore.GetLLMProfileCredential(ctx, ws, id)
	if err != nil || rawKey == "" || !workerCanSend(p, provider, rawKey) {
		return nil, "", "", tokenopt.ErrProfileNotUsable
	}
	return p, provider, rawKey, nil
}

// A Claude subscription OAuth token is bound to the Claude Code client; the daemon calling the API
// with it is a different client, base_url or not.
func workerCanSend(p *secrets.LLMProfile, provider, rawKey string) bool {
	eng, err := engine.Get(p.Engine)
	if err != nil {
		return false
	}
	caps := eng.Capabilities()
	if kind, ok := engine.MatchKeyKind(caps.KeyKinds, rawKey); ok && kind.NoGateway {
		return false
	}
	return caps.GatewayCanPresent(provider, rawKey, p.BaseURL)
}

func (s *Server) tokenOptimizerOn(ctx context.Context, ws string, at *types.AgentType) bool {
	res, _, err := s.resolveTokenOptimizer(ctx, ws, at)
	return err == nil && res.On()
}

func (s *Server) handleResolveTokenOptimizer(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	var at *types.AgentType
	if name := strings.TrimSpace(r.URL.Query().Get("agent")); name != "" {
		found, ok := s.agentTypeByName(ws, name)
		if !ok {
			writeErrKey(w, r, http.StatusNotFound, "api.agents.errors.not_in_catalog")
			return
		}
		at = &found
	}
	res, _, err := s.resolveTokenOptimizer(r.Context(), ws, at)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) runEngine(ctx context.Context, ws string, at *types.AgentType) string {
	if at == nil {
		return engine.DefaultName
	}
	fallback := at.Engine
	if fallback == "" {
		fallback = engine.DefaultName
	}
	if res, err := s.resolveLLMForAgent(ctx, ws, at.Name, fallback); err == nil && res.Engine != "" {
		return res.Engine
	}
	return fallback
}

type delegateResponse struct {
	ID           string  `json:"id"`
	Text         string  `json:"text"`
	Model        string  `json:"model"`
	Provider     string  `json:"provider"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	DurationMs   int64   `json:"duration_ms"`
}

// Policy is resolved again here, not trusted from the run's env, so turning the optimizer off stops
// a run already going.
// Every well-formed call leaves a delegations row, refused or not.
func (s *Server) handleTokenOptimizerDelegate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ws, ok := s.delegatingRun(w, r)
	if !ok {
		return
	}
	req, size, ok := readDelegation(w, r)
	if !ok {
		return
	}
	at, stage := s.delegationAgent(ws, scope)
	res, worker, err := s.resolveTokenOptimizer(ctx, ws, at)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	row := usage.Delegation{
		ID: newDelegationID(), WorkspaceID: ws, TaskID: scope.TaskID, ChatID: scope.ChatID,
		Agent: scope.Agent, Stage: stage, Kind: req.Kind, Files: len(req.Files), RequestBytes: size,
	}
	fail := func(status int, err error) {
		row.Status, row.ErrorCode = usage.DelegationFailed, delegationErrorCode(err)
		if errors.Is(err, tokenopt.ErrTimeout) {
			row.Status = usage.DelegationTimeout
		}
		s.recordDelegation(ctx, row)
		writeError(w, r, status, err)
	}
	if !res.On() {
		failTokenOptOff(fail, res, worker)
		return
	}
	row.ProfileID, row.Model = worker.info.ProfileID, res.ReadModel
	if res.ReadVia == tokenopt.ViaCLI {
		fail(http.StatusFailedDependency, tokenopt.ErrWorkerInPod)
		return
	}

	started := time.Now()
	target, err := s.tokenOptTarget(ctx, ws, worker.info.ProfileID)
	var ans tokenopt.Answer
	if err == nil {
		row.Provider = target.Provider
		ans, err = s.tokenOpt.worker.Call(ctx, target, res.ReadModel, req)
	}
	row.DurationMs = time.Since(started).Milliseconds()
	if err != nil {
		fail(delegationStatus(err), err)
		return
	}

	row.Status = usage.DelegationOK
	row.Model = ans.Model
	row.RequestBytes, row.ResponseBytes = ans.RequestBytes, ans.ResponseBytes
	row.InputTokens, row.OutputTokens = ans.InputTokens, ans.OutputTokens
	row.CostUSD = ans.CostUSD
	if row.CostUSD == 0 && s.usageStore != nil {
		if p, ok := s.usageStore.PricingForModel(ctx, ans.Model); ok {
			row.CostUSD = p.Cost(int(ans.InputTokens), int(ans.OutputTokens), 0, 0)
			row.CostEstimated = row.CostUSD > 0
		}
	}
	s.recordDelegation(ctx, row)
	writeJSON(w, http.StatusOK, delegateResponse{
		ID: row.ID, Text: ans.Text, Model: row.Model, Provider: row.Provider,
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CostUSD: row.CostUSD, DurationMs: row.DurationMs,
	})
}

func (s *Server) delegationAgent(ws string, scope auth.RunScope) (*types.AgentType, string) {
	stage := scope.Stage
	if scope.ChatID != "" {
		stage = string(types.StageChat)
	}
	if found, known := s.agentTypeByName(ws, scope.Agent); known {
		return &found, stage
	}
	return nil, stage
}

func failTokenOptOff(fail func(int, error), res tokenopt.Resolved, worker tokenOptWorker) {
	if res.Reason != tokenopt.ReasonNoWorker {
		fail(http.StatusForbidden, tokenopt.ErrDisabled)
		return
	}
	if worker.unusable != nil {
		fail(http.StatusFailedDependency, worker.unusable)
		return
	}
	fail(http.StatusFailedDependency, tokenopt.ErrNoWorker)
}

func (s *Server) delegatingRun(w http.ResponseWriter, r *http.Request) (auth.RunScope, string, bool) {
	ctx := r.Context()
	scope := auth.APIKeyRunScope(ctx)
	ws := auth.APIKeyWorkspace(ctx)
	ok := auth.IsAPIKeyAuthed(ctx) && scope.IsRun() && scope.Agent != "" && ws != ""
	if ok && scope.TaskID != "" && s.taskStore != nil {
		t, err := s.taskStore.GetRow(ctx, scope.TaskID)
		ok = err == nil && t.WorkspaceID == ws
	}
	if !ok {
		writeErrKey(w, r, http.StatusForbidden, "api.token_optimizer.errors.run_keys_only")
	}
	return scope, ws, ok
}

func readDelegation(w http.ResponseWriter, r *http.Request) (tokenopt.Request, int64, bool) {
	var req tokenopt.Request
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tokenopt.MaxRequestBytes))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.token_optimizer.errors.body_too_large", "max", tokenopt.MaxRequestBytes)
		return req, 0, false
	case err == nil:
		err = json.Unmarshal(raw, &req)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body"))
		return req, 0, false
	}
	if err := req.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return req, 0, false
	}
	return req, int64(len(raw)), true
}

// WithoutCancel: the row is kept even if the run hung up; the call was paid for.
func (s *Server) recordDelegation(ctx context.Context, row usage.Delegation) {
	if s.usageStore == nil {
		return
	}
	logx.Warn(s.usageStore.AddDelegation(context.WithoutCancel(ctx), row), "api.token_optimizer.log.record_failed", "id", row.ID)
}

func delegationStatus(err error) int {
	switch {
	case errors.Is(err, tokenopt.ErrTimeout):
		return http.StatusGatewayTimeout
	case apperr.KindOf(err) == apperr.KindPrecondition:
		return http.StatusFailedDependency
	default:
		return http.StatusBadGateway
	}
}

func delegationErrorCode(err error) string {
	for _, known := range []error{
		tokenopt.ErrTimeout, tokenopt.ErrDisabled, tokenopt.ErrNoWorker, tokenopt.ErrProviderUnsupported, tokenopt.ErrProfileNotUsable,
		tokenopt.ErrWorkerInPod, tokenopt.ErrWorkerNotInPod,
	} {
		if errors.Is(err, known) {
			return apperr.Code(known)
		}
	}
	return apperr.Code(err)
}

func newDelegationID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "dlg_" + hex.EncodeToString(b[:])
}

func (s *Server) tokenOptTarget(ctx context.Context, ws, profileID string) (tokenopt.Target, error) {
	p, provider, rawKey, err := s.workerProfile(ctx, ws, profileID)
	if err != nil {
		return tokenopt.Target{}, err
	}
	if engine.ProviderNeedsBaseURL(provider) && strings.TrimSpace(p.BaseURL) == "" {
		return tokenopt.Target{}, apperr.Precondition("api.token_optimizer.errors.base_url_required", "provider", provider)
	}
	upstream, allowPrivate, err := gatewayUpstream(p.BaseURL, s.llmUpstreams[provider])
	if err != nil {
		return tokenopt.Target{}, apperr.WrapKind(apperr.KindPrecondition, err, "api.token_optimizer.errors.endpoint_refused")
	}
	t := tokenopt.Target{Provider: provider, BaseURL: upstream.String(), Credential: rawKey, AllowPrivate: allowPrivate}
	switch provider {
	case tokenopt.ProviderOpenAI:
		if p.OrgID != "" {
			t.Headers = map[string]string{"OpenAI-Organization": p.OrgID}
		}
	case tokenopt.ProviderAnthropic:
		t.Bearer = p.AuthHeader == engine.AuthHeaderBearer
	}
	return t, nil
}
