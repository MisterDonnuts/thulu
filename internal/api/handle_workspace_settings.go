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

// workspaceSetting is one value a workspace admin may set: how to read its current view and the
// error a value out of 1..ceiling answers.
type workspaceSetting struct {
	read     func(s *Server, r *http.Request, wid string) (settings.RunLimit, error)
	rangeKey string
	// The audit entries for a value set and cleared, and how they spell the value in effect.
	auditSet, auditCleared string
	text                   func(n int) string
}

var workspaceSettingsTable = map[string]workspaceSetting{
	settings.AgentMaxRunMinutesKey: {
		read: func(s *Server, r *http.Request, wid string) (settings.RunLimit, error) {
			return s.settingsStore.WorkspaceRunLimit(r.Context(), wid, entitlements.SaaSMode())
		},
		rangeKey:     "api.workspace_settings.errors.run_minutes_range",
		auditSet:     "api.workspace_settings.audit.set",
		auditCleared: "api.workspace_settings.audit.cleared",
		text:         minutesText,
	},
	settings.AgentTurnsPerThreadKey: {
		read: func(s *Server, r *http.Request, wid string) (settings.RunLimit, error) {
			return s.settingsStore.WorkspaceAgentTurns(r.Context(), wid)
		},
		rangeKey:     "api.workspace_settings.errors.agent_turns_range",
		auditSet:     "api.workspace_settings.audit.agent_turns_set",
		auditCleared: "api.workspace_settings.audit.agent_turns_cleared",
		text:         repliesText,
	},
}

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
	view := workspaceSettingsView{}
	for key, def := range workspaceSettingsTable {
		limit, err := def.read(s, r, wid)
		if err != nil {
			return nil, err
		}
		view[key] = limit
	}
	return view, nil
}

func (s *Server) handlePutWorkspaceSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	def, known := workspaceSettingsTable[key]
	if !settings.KnownWorkspaceKey(key) || !known {
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
	before, err := def.read(s, r, wid)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return
	}
	value := ""
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		err = s.settingsStore.DeleteWorkspaceSetting(r.Context(), wid, key)
	} else {
		var n int64
		n, err = settingValue(raw, before.Ceiling, def.rangeKey)
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
	s.auditWorkspaceSetting(r, wid, key, before.Value, value, view[key].Effective, def)
	writeJSON(w, http.StatusOK, view)
}

func settingValue(raw json.RawMessage, ceiling int, rangeKey string) (int64, error) {
	var n int64
	if json.Unmarshal(raw, &n) != nil || n < 1 || n > int64(ceiling) {
		return 0, apperr.Invalid(rangeKey, "min", 1, "max", ceiling)
	}
	return n, nil
}

func (s *Server) auditWorkspaceSetting(r *http.Request, wid, key string, before *int, after string, effective int,
	def workspaceSetting,
) {
	if s.auditStore == nil {
		return
	}
	prev := ""
	if before != nil {
		prev = strconv.Itoa(*before)
	}
	summary := i18n.Text(def.auditCleared, "setting", key, "effective", def.text(effective))
	if after != "" {
		summary = i18n.Text(def.auditSet, "setting", key, "value", after, "effective", def.text(effective))
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

func repliesText(n int) string {
	return i18n.Text("api.workspace_settings.audit.replies", "n", n)
}
