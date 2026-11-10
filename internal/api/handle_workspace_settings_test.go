package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

const runLimitPath = "/workspace-settings/" + settings.AgentMaxRunMinutesKey

func runLimitOf(t *testing.T, w *httptest.ResponseRecorder) settings.RunLimit {
	t.Helper()
	var view map[string]settings.RunLimit
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	lim, ok := view[settings.AgentMaxRunMinutesKey]
	if !ok {
		t.Fatalf("no %s in %s", settings.AgentMaxRunMinutesKey, w.Body)
	}
	return lim
}

func TestWorkspaceSettings_GetShape(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, _ := mintWorkspaceUserAs(t, srv, "wss-get@test.com", "WG", auth.RoleAdmin)
	viewer := reissueAs(t, admin, auth.RoleViewer)

	w := getJSON(t, h, "/workspace-settings", viewer)
	if w.Code != http.StatusOK {
		t.Fatalf("viewer GET = %d: %s", w.Code, w.Body)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	got := raw[settings.AgentMaxRunMinutesKey]
	for _, k := range []string{"value", "default", "ceiling", "effective"} {
		if _, ok := got[k]; !ok {
			t.Errorf("GET is missing %q: %s", k, w.Body)
		}
	}
	if got["value"] != nil {
		t.Errorf("value before any choice = %v, want null", got["value"])
	}
	lim := runLimitOf(t, w)
	want := settings.SelfHostedDefaultMaxRunMinutes
	if lim.Default != want || lim.Ceiling != settings.MaxRunMinutesCeiling || lim.Effective != want {
		t.Errorf("defaults = %+v", lim)
	}
}

func TestWorkspaceSettings_AdminSetsAndClears(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, ws := mintWorkspaceUserAs(t, srv, "wss-put@test.com", "WP", auth.RoleAdmin)

	w := putJSON(t, h, runLimitPath, 180, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("admin PUT = %d: %s", w.Code, w.Body)
	}
	if lim := runLimitOf(t, w); lim.Value == nil || *lim.Value != 180 || lim.Effective != 180 {
		t.Fatalf("after PUT = %+v", lim)
	}
	if d := srv.runDeadline(ws); d != 3*time.Hour {
		t.Errorf("run deadline = %s, want 3h", d)
	}
	if d, want := srv.runDeadline("someone-else"), time.Duration(settings.SelfHostedDefaultMaxRunMinutes)*time.Minute; d != want {
		t.Errorf("other workspace's deadline = %s, want the install default %s", d, want)
	}
	entries, err := srv.auditStore.List(audit.ListOptions{Kind: audit.KindWorkspaceSettingChanged})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].WorkspaceID != ws || entries[0].Metadata["value"] != "180" ||
		entries[0].Metadata["setting"] != settings.AgentMaxRunMinutesKey || entries[0].ActorID == "" {
		t.Fatalf("audit entries = %+v", entries)
	}

	w = putJSON(t, h, runLimitPath, nil, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT null = %d: %s", w.Code, w.Body)
	}
	if lim := runLimitOf(t, w); lim.Value != nil || lim.Effective != lim.Default {
		t.Fatalf("after clear = %+v", lim)
	}
	if _, err := srv.settingsStore.GetWorkspaceRaw(t.Context(), ws, settings.AgentMaxRunMinutesKey); err == nil {
		t.Error("the row survived a clear")
	}
	entries, _ = srv.auditStore.List(audit.ListOptions{Kind: audit.KindWorkspaceSettingChanged})
	if len(entries) != 2 {
		t.Fatalf("audit entries after clear = %d, want 2", len(entries))
	}
}

func TestWorkspaceSettings_Roles(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, _ := mintWorkspaceUserAs(t, srv, "wss-roles@test.com", "WR", auth.RoleAdmin)
	if w := putJSON(t, h, runLimitPath, 30, reissueAs(t, admin, auth.RoleDeveloper)); w.Code != http.StatusForbidden {
		t.Errorf("developer PUT = %d, want 403: %s", w.Code, w.Body)
	}
	if w := putJSON(t, h, runLimitPath, 30, reissueAs(t, admin, auth.RoleViewer)); w.Code != http.StatusForbidden {
		t.Errorf("viewer PUT = %d, want 403", w.Code)
	}
	if w := putJSON(t, h, runLimitPath, 30, reissueAs(t, admin, auth.RoleOwner)); w.Code != http.StatusOK {
		t.Errorf("owner PUT = %d, want 200: %s", w.Code, w.Body)
	}
}

func TestWorkspaceSettings_Refusals(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, ws := mintWorkspaceUserAs(t, srv, "wss-bad@test.com", "WB", auth.RoleAdmin)
	if err := srv.settingsStore.PutJSON(t.Context(), settings.AgentMaxRunMinutesCeilingKey, 120); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{121, 0, -5, 30.5, "60", true, map[string]int{"minutes": 60}} {
		w := putJSON(t, h, runLimitPath, body, admin)
		if w.Code != http.StatusBadRequest {
			t.Errorf("PUT %v = %d, want 400: %s", body, w.Code, w.Body)
			continue
		}
		if !strings.Contains(w.Body.String(), "120") {
			t.Errorf("PUT %v: the refusal does not name the ceiling: %s", body, w.Body)
		}
	}
	if _, err := srv.settingsStore.GetWorkspaceRaw(t.Context(), ws, settings.AgentMaxRunMinutesKey); err == nil {
		t.Error("a refused value was stored")
	}
	if w := putJSON(t, h, runLimitPath, 120, admin); w.Code != http.StatusOK {
		t.Errorf("PUT at the ceiling = %d: %s", w.Code, w.Body)
	}
	if w := putJSON(t, h, "/workspace-settings/"+settings.SandboxMaxKey, 50, admin); w.Code != http.StatusBadRequest {
		t.Errorf("PUT a host key = %d, want 400", w.Code)
	}
	r := httptest.NewRequest(http.MethodPut, runLimitPath, strings.NewReader("500"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+admin)
	r.Header.Set("Accept-Language", "es")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "minutos") {
		t.Errorf("es refusal = %d: %s", w.Code, w.Body)
	}
}

func TestWorkspaceSettings_LoweredCeilingCaps(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	admin, ws := mintWorkspaceUserAs(t, srv, "wss-cap@test.com", "WC", auth.RoleAdmin)
	if w := putJSON(t, h, runLimitPath, 600, admin); w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body)
	}
	if err := srv.settingsStore.PutJSON(t.Context(), settings.AgentMaxRunMinutesCeilingKey, 90); err != nil {
		t.Fatal(err)
	}
	lim := runLimitOf(t, getJSON(t, h, "/workspace-settings", admin))
	if lim.Value == nil || *lim.Value != 600 || lim.Ceiling != 90 || lim.Effective != 90 {
		t.Errorf("after the ceiling dropped = %+v", lim)
	}
	if d := srv.runDeadline(ws); d != 90*time.Minute {
		t.Errorf("run deadline = %s, want 1h30m", d)
	}
}

func TestSettings_CeilingValidation(t *testing.T) {
	for raw, ok := range map[string]bool{"0": true, "240": true, "1440": true, "1441": false, "-1": false, "null": false, `"4h"`: false} {
		err := validateSetting(settings.AgentMaxRunMinutesCeilingKey, json.RawMessage(raw))
		if (err == nil) != ok {
			t.Errorf("%s: err = %v, want ok=%v", raw, err, ok)
		}
	}
}
