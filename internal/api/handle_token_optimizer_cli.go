package api

import (
	"math"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/tokenopt"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type prepareRequest struct {
	Kind string `json:"kind"`
}

type prepareResponse struct {
	ID              string `json:"id"`
	Model           string `json:"model"`
	SystemPrompt    string `json:"system_prompt"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

func (s *Server) handleTokenOptimizerPrepare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope, ws, ok := s.delegatingRun(w, r)
	if !ok {
		return
	}
	if s.usageStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.token_optimizer.errors.usage_unavailable")
		return
	}
	var body prepareRequest
	if !decodeBody(w, r, &body) {
		return
	}
	kind := strings.TrimSpace(body.Kind)
	if err := tokenopt.ValidKind(kind); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
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
		Agent: scope.Agent, Stage: stage, Kind: kind,
	}
	fail := func(status int, err error) {
		row.Status, row.ErrorCode = usage.DelegationFailed, delegationErrorCode(err)
		s.recordDelegation(ctx, row)
		writeError(w, r, status, err)
	}
	if !res.On() {
		failTokenOptOff(fail, res, worker)
		return
	}
	row.ProfileID, row.Model = worker.info.ProfileID, res.ReadModel
	if res.ReadVia != tokenopt.ViaCLI {
		fail(http.StatusFailedDependency, tokenopt.ErrWorkerNotInPod)
		return
	}
	row.Provider, row.Status = tokenopt.ProviderClaudeCode, usage.DelegationStarted
	// Unlike recordDelegation, a failed write is answered: finish needs the row.
	if err := s.usageStore.AddDelegation(ctx, row); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, prepareResponse{
		ID: row.ID, Model: res.ReadModel, SystemPrompt: tokenopt.Instructions(), MaxOutputTokens: tokenopt.MaxOutputTokens,
	})
}

type finishRequest struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	ErrorCode     string  `json:"error_code"`
	Files         int     `json:"files"`
	RequestBytes  int64   `json:"request_bytes"`
	ResponseBytes int64   `json:"response_bytes"`
	InputTokens   int64   `json:"input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	DurationMs    int64   `json:"duration_ms"`
}

// Only a catalog key, never a message or output that could carry file contents.
const maxFinishErrorCode = 128

func (f *finishRequest) validate() error {
	f.ID, f.Status, f.ErrorCode = strings.TrimSpace(f.ID), strings.TrimSpace(f.Status), strings.TrimSpace(f.ErrorCode)
	if f.ID == "" {
		return apperr.Invalid("tokenopt.errors.id_required")
	}
	switch f.Status {
	case usage.DelegationOK, usage.DelegationFailed, usage.DelegationTimeout:
	default:
		return apperr.Invalid("tokenopt.errors.invalid_status", "status", f.Status)
	}
	if f.ErrorCode != "" && !validErrorCode(f.ErrorCode) {
		return apperr.Invalid("tokenopt.errors.invalid_error_code", "max", maxFinishErrorCode)
	}
	for _, n := range []struct {
		field string
		value int64
	}{
		{"files", int64(f.Files)}, {"request_bytes", f.RequestBytes}, {"response_bytes", f.ResponseBytes},
		{"input_tokens", f.InputTokens}, {"output_tokens", f.OutputTokens}, {"duration_ms", f.DurationMs},
	} {
		if n.value < 0 {
			return apperr.Invalid("tokenopt.errors.negative_field", "field", n.field)
		}
	}
	if f.CostUSD < 0 || math.IsNaN(f.CostUSD) || math.IsInf(f.CostUSD, 0) {
		return apperr.Invalid("tokenopt.errors.negative_field", "field", "cost_usd")
	}
	return nil
}

func validErrorCode(code string) bool {
	const prefix = "tokenopt.errors."
	if len(code) > maxFinishErrorCode || !strings.HasPrefix(code, prefix) || len(code) == len(prefix) {
		return false
	}
	for _, c := range code {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// Only the preparing run may close the row, once. No policy check on purpose: the call was paid for
// regardless.
func (s *Server) handleTokenOptimizerFinish(w http.ResponseWriter, r *http.Request) {
	scope, _, ok := s.delegatingRun(w, r)
	if !ok {
		return
	}
	if s.usageStore == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.token_optimizer.errors.usage_unavailable")
		return
	}
	var body finishRequest
	if !decodeBody(w, r, &body) {
		return
	}
	if err := body.validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	found, err := s.usageStore.FinishDelegation(r.Context(), usage.Delegation{
		ID: body.ID, TaskID: scope.TaskID, ChatID: scope.ChatID, Agent: scope.Agent,
		Status: body.Status, ErrorCode: body.ErrorCode, Files: body.Files,
		RequestBytes: body.RequestBytes, ResponseBytes: body.ResponseBytes,
		InputTokens: body.InputTokens, OutputTokens: body.OutputTokens,
		CostUSD: body.CostUSD, DurationMs: body.DurationMs,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeErrKey(w, r, http.StatusNotFound, "api.token_optimizer.errors.delegation_not_found", "id", body.ID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": body.ID})
}
