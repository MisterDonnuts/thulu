package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"net/http"
	"slices"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

// Every other key describes the host all tenants share and is operator-only to read. The ingest URL
// is what tenants point their telemetry at.
var tenantReadableSettings = map[string]bool{
	settings.IngestPublicURLKey: true,
}

func (s *Server) handleListSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := s.settingsStore.List(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	operator := s.callerIsOperator(r)
	out := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		if !operator && !tenantReadableSettings[k] {
			continue
		}
		out[k] = json.RawMessage(v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !tenantReadableSettings[key] &&
		!s.requireOperator(w, r, "api.scope.operator_surfaces.host_settings", "api.scope.operator_verbs.read") {
		return
	}
	raw, err := s.settingsStore.GetRaw(r.Context(), key)
	if errors.Is(err, settings.ErrNotFound) {
		writeErrKey(w, r, http.StatusNotFound, "api.settings.errors.not_found")
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(raw))
}

func (s *Server) handlePutSetting(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r, "api.scope.operator_surfaces.host_settings", "api.scope.operator_verbs.change") {
		return
	}
	key := r.PathValue("key")
	var raw json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	if err := validateSetting(key, raw); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if err := s.settingsStore.PutRaw(r.Context(), key, string(raw)); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.afterSettingsWrite(r.Context(), key)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r, "api.scope.operator_surfaces.host_settings", "api.scope.operator_verbs.change") {
		return
	}
	var body map[string]json.RawMessage
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body) == 0 {
		writeErrKey(w, r, http.StatusBadRequest, "api.settings.errors.batch_empty")
		return
	}
	values := make(map[string]string, len(body))
	for _, key := range slices.Sorted(maps.Keys(body)) {
		if err := validateSetting(key, body[key]); err != nil {
			writeJSON(w, http.StatusBadRequest, errRefusal(r.Context(), map[string]any{"setting": key}, err))
			return
		}
		values[key] = string(body[key])
	}
	if err := s.settingsStore.PutRawMany(r.Context(), values); err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	for key := range values {
		s.afterSettingsWrite(r.Context(), key)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) afterSettingsWrite(ctx context.Context, key string) {
	if key != settings.SandboxPrioritiesKey || s.sandbox.mgr == nil {
		return
	}
	_, err := s.sandbox.mgr.Queue().Reprioritize(ctx)
	logx.Warn(err, "api.settings.log.reprioritize_failed")
}

// Unknown keys are refused: otherwise this is a write channel into a table GET /settings serves to
// every caller, and nothing would ever clean the key up.
func validateSetting(key string, raw json.RawMessage) error {
	if !settings.KnownKey(key) {
		return apperr.Invalid("api.settings.errors.unknown_setting", "setting", key)
	}
	if numeric, err := validateNumericSetting(key, raw); numeric {
		return err
	}
	switch key {
	case settings.SandboxPrioritiesKey:
		var v map[string]int
		if err := json.Unmarshal(raw, &v); err != nil {
			return apperr.Invalid("api.settings.errors.priorities_shape")
		}
	case settings.IngestPublicURLKey:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return apperr.Invalid("api.settings.errors.public_url_string")
		}
		// Same rule as the typed setter. A schemeless address is accepted downstream and only shows
		// up weeks later as a panel that never fills.
		if _, err := settings.NormalizeIngestPublicURL(v); err != nil {
			return err
		}
	}
	return nil
}

func validateNumericSetting(key string, raw json.RawMessage) (numeric bool, err error) {
	switch key {
	case settings.SandboxMaxKey:
		return true, intInRange(raw, 1, noMax, apperr.Invalid("api.settings.errors.sandbox_max_positive"))
	case settings.SandboxIdleEvictSecondsKey, settings.SandboxAcquireBlockMsKey,
		settings.AgentMaxRunMinutesKey, settings.ChatMemoryBytesKey,
		settings.ChatNanoCPUsKey, settings.ChatPidsLimitKey,
		settings.AgentMaxContainersKey, settings.MentionMaxContainersKey:
		return true, intInRange(raw, 0, noMax, apperr.Invalid("api.settings.errors.non_negative_int", "setting", key))
	case settings.ChatMaxKey:
		// 0 would refuse every chat: the cap counts live containers against it.
		return true, intInRange(raw, 1, noMax, apperr.Invalid("api.settings.errors.positive_int", "setting", key))
	case settings.AgentMaxRunMinutesCeilingKey:
		// 0 means the deployment's default ceiling; above MaxRunMinutesCeiling a run would outlive
		// its own key.
		return true, intInRange(raw, 0, settings.MaxRunMinutesCeiling,
			apperr.Invalid("api.settings.errors.int_range", "setting", key, "min", 0, "max", settings.MaxRunMinutesCeiling))
	case settings.AgentMaxQueueMinutesKey:
		return true, intInRange(raw, 1, settings.MaxQueueMinutes,
			apperr.Invalid("api.settings.errors.int_range", "setting", key, "min", 1, "max", settings.MaxQueueMinutes))
	case settings.SandboxMaxExecTimeoutMsKey:
		return true, intInRange(raw, 1, noMax, apperr.Invalid("api.settings.errors.positive_int", "setting", key))
	case settings.RetentionRunLogsDaysKey, settings.RetentionSessionsDaysKey,
		settings.RetentionWebhookDeliveriesDaysKey, settings.RetentionRunKeysDaysKey,
		settings.RetentionSandboxRowsDaysKey, settings.RetentionStuckWorktreeDaysKey,
		settings.RetentionCacheMetricsDaysKey, settings.AuditRetentionDaysKey:
		return true, intInRange(raw, 0, noMax, apperr.Invalid("api.settings.errors.retention_days", "setting", key))
	case settings.JanitorDiskFreeMinPercentKey:
		return true, intInRange(raw, 0, 100, apperr.Invalid("api.settings.errors.percent_range", "setting", key))
	}
	return false, nil
}

const noMax = math.MaxInt64

// null is refused explicitly: json.Unmarshal leaves n at 0 without error, and an idle eviction of 0
// switches the idle sweep off.
func intInRange(raw json.RawMessage, lo, hi int64, refusal error) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return refusal
	}
	var n int64
	if json.Unmarshal(raw, &n) != nil || n < lo || n > hi {
		return refusal
	}
	return nil
}
