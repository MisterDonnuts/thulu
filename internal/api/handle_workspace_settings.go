package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

func (s *Server) gatedSettings(h http.HandlerFunc) http.HandlerFunc {
	return gated(s.settingsStore != nil, "api.server.errors.settings_store_unavailable", h)
}

type workspaceSettingsView map[string]settings.RunLimit

func (s *Server) handleGetWorkspaceSettings(w http.ResponseWriter, r *http.Request) {
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	view, err := s.workspaceSettingsView(r, wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) workspaceSettingsView(r *http.Request, wid string) (workspaceSettingsView, error) {
	limit, err := s.settingsStore.WorkspaceRunLimit(r.Context(), wid, entitlements.SaaSMode())
	if err != nil {
		return nil, err
	}
	return workspaceSettingsView{settings.AgentMaxRunMinutesKey: limit}, nil
}

func (s *Server) handlePutWorkspaceSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !settings.KnownWorkspaceKey(key) {
		writeError(w, r, http.StatusBadRequest, apperr.Invalid("api.settings.errors.unknown_setting", "setting", key))
		return
	}
	wid, ok := s.workspaceIDFromRequest(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	if !decodeBody(w, r, &raw) {
		return
	}
	before, err := s.settingsStore.WorkspaceRunLimit(r.Context(), wid, entitlements.SaaSMode())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	value := ""
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		err = s.settingsStore.DeleteWorkspaceSetting(r.Context(), wid, key)
	} else {
		var n int64
		n, err = runMinutesValue(raw, before.Ceiling)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, err)
			return
		}
		value = strconv.FormatInt(n, 10)
		err = s.settingsStore.PutWorkspaceRaw(r.Context(), wid, key, value)
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	view, err := s.workspaceSettingsView(r, wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.auditWorkspaceSetting(r, wid, key, before.Value, value, view[key].Effective)
	writeJSON(w, http.StatusOK, view)
}

func runMinutesValue(raw json.RawMessage, ceiling int) (int64, error) {
	var n int64
	if json.Unmarshal(raw, &n) != nil || n < 1 || n > int64(ceiling) {
		return 0, apperr.Invalid("api.workspace_settings.errors.run_minutes_range", "min", 1, "max", ceiling)
	}
	return n, nil
}

func (s *Server) auditWorkspaceSetting(r *http.Request, wid, key string, before *int, after string, effective int) {
	if s.auditStore == nil {
		return
	}
	prev := ""
	if before != nil {
		prev = strconv.Itoa(*before)
	}
	summary := i18n.Text("api.workspace_settings.audit.cleared", "setting", key, "effective", minutesText(effective))
	if after != "" {
		summary = i18n.Text("api.workspace_settings.audit.set", "setting", key, "value", after, "effective", minutesText(effective))
	}
	s.auditStore.LogRequestIn(r, wid, audit.KindWorkspaceSettingChanged, wid, "workspace", summary, map[string]string{
		"setting":   key,
		"previous":  prev,
		"value":     after,
		"effective": strconv.Itoa(effective),
	})
}

func minutesText(n int) string {
	if n <= 0 {
		return i18n.Text("api.workspace_settings.audit.no_limit")
	}
	return i18n.Text("api.workspace_settings.audit.minutes", "n", n)
}
