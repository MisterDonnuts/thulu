package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// A preference is a small JSON value the dashboard keeps per person. Only the keys below exist:
// each one validates what it receives and rewrites it in its canonical form, so a stored value is
// always one the current UI reads.
type preferenceSpec struct {
	normalize func(raw []byte) (any, error)
}

var preferenceSpecs = map[string]preferenceSpec{
	"tasks.view": {normalize: normalizeTasksView},
}

// A value larger than this is not a view setting.
const maxPreferenceBody = 4 << 10

// Must match TasksViewPref in web/src/lib/task-list.ts.
type tasksViewPref struct {
	Layout string `json:"layout"`
	Group  string `json:"group"`
	Sort   string `json:"sort"`
	Dir    string `json:"dir"`
}

var (
	tasksLayouts = []string{"board", "list"}
	// "none" first: the list opens flat, like an issue list; grouping by stage is a choice.
	tasksGroups = []string{"none", "stage"}
	// "" keeps the board's own order.
	tasksSorts = []string{"", "title", "stage", "status", "priority", "updated"}
	tasksDirs  = []string{"asc", "desc"}
)

func normalizeTasksView(raw []byte) (any, error) {
	var v tasksViewPref
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return nil, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body")
	}
	if dec.More() {
		return nil, apperr.Invalid("api.server.errors.invalid_body")
	}
	if v.Group == "" {
		v.Group = "none"
	}
	if v.Dir == "" {
		v.Dir = "asc"
	}
	for _, f := range []struct {
		name, value string
		allowed     []string
	}{
		{"layout", v.Layout, tasksLayouts},
		{"group", v.Group, tasksGroups},
		{"sort", v.Sort, tasksSorts},
		{"dir", v.Dir, tasksDirs},
	} {
		if !slices.Contains(f.allowed, f.value) {
			return nil, apperr.Invalid("api.preferences.errors.invalid_value",
				"field", f.name, "allowed", strings.Join(nonEmpty(f.allowed), ", "))
		}
	}
	return v, nil
}

func nonEmpty(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// preferenceOwner is the person the preferences belong to. A key has no person behind it, so it
// gets a 403 rather than a shared set every holder of the key would overwrite.
func preferenceOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := personID(r.Context())
	if !ok {
		writeErrKey(w, r, http.StatusForbidden, "api.chats.errors.person_required",
			"what", i18n.TCtx(r.Context(), "api.preferences.labels.saving"))
		return "", false
	}
	return id, true
}

// A stored value that no longer validates (an older shape, a key since removed) is left out, and
// the dashboard falls back to its default.
func (s *Server) handleGetPreferences(w http.ResponseWriter, r *http.Request) {
	owner, ok := preferenceOwner(w, r)
	if !ok {
		return
	}
	stored, err := s.authStore.Preferences(r.Context(), owner)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	out := map[string]any{}
	for key, raw := range stored {
		spec, known := preferenceSpecs[key]
		if !known {
			continue
		}
		if v, err := spec.normalize([]byte(raw)); err == nil {
			out[key] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"preferences": out})
}

func (s *Server) handlePutPreference(w http.ResponseWriter, r *http.Request) {
	owner, ok := preferenceOwner(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	spec, known := preferenceSpecs[key]
	if !known {
		writeErrKey(w, r, http.StatusNotFound, "api.preferences.errors.unknown_key", "key", key)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPreferenceBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.preferences.errors.too_large")
			return
		}
		writeError(w, r, http.StatusBadRequest, apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body"))
		return
	}
	v, err := spec.normalize(raw)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	canon, err := json.Marshal(v)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	if err := s.authStore.SetPreference(r.Context(), owner, key, string(canon)); err != nil {
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "value": v})
}
