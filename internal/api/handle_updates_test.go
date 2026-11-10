package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/kept"
	"github.com/MisterDonnuts/thulu/internal/release"
	"github.com/MisterDonnuts/thulu/internal/updatecheck"
	"github.com/MisterDonnuts/thulu/internal/updatecheck/checktest"
)

func updatesRig(t *testing.T) (*operatorRig, *checktest.Source) {
	t.Helper()
	rig := newOperatorRig(t)
	t.Setenv("OCTOPUS_AGENT_RUNTIME", "")
	setVersion(t, "0.2.0")

	signer := checktest.NewSigner(t)
	src := &checktest.Source{}
	src.Publish(t, signer, checktest.Manifest("0.2.0", release.ChannelStable), release.ChannelStable)
	src.Publish(t, signer, checktest.Manifest("0.3.0", release.ChannelStable), release.ChannelStable)
	c := updatecheck.New(updatecheck.Config{
		Source:   src,
		Channels: map[string]bool{release.ChannelStable: true},
		Keys:     signer.Keys,
		Current:  "0.2.0",
	})
	if err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	rig.srv.SetUpdateChecker(c)
	return rig, src
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	orig := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = orig })
}

func decodeInto(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func TestUpdates_OnlyTheOperatorReads(t *testing.T) {
	rig, _ := updatesRig(t)
	viewerToken, _ := mintWorkspaceUserAs(t, rig.srv, "updates-viewer@test.com", "UV", auth.RoleViewer)
	noPermKey := mintKey(t, rig.srv, rig.opWS, auth.RoleAdmin, auth.PermTasksRead)
	readKey := mintKey(t, rig.srv, rig.opWS, auth.RoleAdmin, auth.PermSettingsRead)

	for _, path := range []string{"/updates/status", "/updates/versions", "/updates/history", "/updates/package?to=0.3.0"} {
		if w := callAs(t, rig.h, http.MethodGet, path, rig.opToken, "", nil); w.Code != http.StatusOK {
			t.Errorf("operator GET %s = %d: %s", path, w.Code, w.Body.String())
		}
		if w := callAs(t, rig.h, http.MethodGet, path, rig.tenantToken, "", nil); w.Code != http.StatusForbidden {
			t.Errorf("tenant admin GET %s = %d, want 403", path, w.Code)
		}
		if w := callAs(t, rig.h, http.MethodGet, path, viewerToken, "", nil); w.Code != http.StatusForbidden {
			t.Errorf("viewer GET %s = %d, want 403", path, w.Code)
		}
		if w := callAs(t, rig.h, http.MethodGet, path, "", rig.tenantAdminKey, nil); w.Code != http.StatusForbidden {
			t.Errorf("tenant key GET %s = %d, want 403", path, w.Code)
		}
		if w := callAs(t, rig.h, http.MethodGet, path, "", noPermKey, nil); w.Code != http.StatusForbidden {
			t.Errorf("tasks:read key GET %s = %d, want 403", path, w.Code)
		}
		if w := callAs(t, rig.h, http.MethodGet, path, "", readKey, nil); w.Code != http.StatusOK {
			t.Errorf("settings:read key GET %s = %d: %s", path, w.Code, w.Body.String())
		}
	}

	ev := validUpdateEvent()
	if w := callAs(t, rig.h, http.MethodPost, "/updates/events", "", readKey, ev); w.Code != http.StatusForbidden {
		t.Errorf("settings:read key POST /updates/events = %d, want 403", w.Code)
	}
	if w := callAs(t, rig.h, http.MethodPost, "/updates/events", "", rig.tenantAdminKey, ev); w.Code != http.StatusForbidden {
		t.Errorf("tenant key POST /updates/events = %d, want 403", w.Code)
	}
	if w := callAs(t, rig.h, http.MethodPost, "/updates/events", rig.opToken, "", ev); w.Code != http.StatusForbidden {
		t.Errorf("operator session POST /updates/events = %d, want 403", w.Code)
	}

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, path := range []string{"/updates/apply", "/updates/status", "/updates/rollback"} {
			w := callAs(t, rig.h, m, path, rig.opToken, "", map[string]string{"version": "0.3.0"})
			if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 404/405", m, path, w.Code)
			}
		}
	}
}

func TestUpdates_StatusOffersTheVerifiedRelease(t *testing.T) {
	rig, _ := updatesRig(t)
	w := callAs(t, rig.h, http.MethodGet, "/updates/status", rig.opToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var st updateStatusView
	decodeInto(t, w.Body.Bytes(), &st)
	if st.Version != "0.2.0" || !st.UpdateAvailable || len(st.Available) != 1 || st.Mode != modeDocker {
		t.Fatalf("status = %+v", st)
	}
	rel := st.Available[0]
	if rel.Version != "0.3.0" || rel.Command != "thulu update -version 0.3.0" || rel.Unverified != "" {
		t.Errorf("release = %+v", rel)
	}
	if !rel.CanUpgrade || !rel.Rollback.WithoutRestore || rel.Migrations.Added != 6 {
		t.Errorf("release facts = %+v", rel)
	}
	if len(st.Advisories) != 1 || st.Advisories[0].FixedIn != "0.3.0" {
		t.Errorf("advisories = %+v", st.Advisories)
	}
	if st.RollbackTo != "" {
		t.Errorf("0.2.0's manifest names no rollback, got %q", st.RollbackTo)
	}
}

func TestUpdates_StatusWithoutAChecker(t *testing.T) {
	rig := newOperatorRig(t)
	if w := callAs(t, rig.h, http.MethodGet, "/updates/status", rig.opToken, "", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("status without a checker = %d, want 503", w.Code)
	}
	if w := callAs(t, rig.h, http.MethodPost, "/updates/check", rig.opToken, "", nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("check without a checker = %d, want 503", w.Code)
	}
}

func TestUpdates_CheckIsRateLimitedAndRefusedWhenOff(t *testing.T) {
	rig, src := updatesRig(t)
	lists := src.Lists.Load()
	w := callAs(t, rig.h, http.MethodPost, "/updates/check", rig.opToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("first check = %d: %s", w.Code, w.Body.String())
	}
	if src.Lists.Load() != lists+1 {
		t.Errorf("the check did not reach the source")
	}
	w = callAs(t, rig.h, http.MethodPost, "/updates/check", rig.opToken, "", nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("second check = %d (Retry-After %q), want 429", w.Code, w.Header().Get("Retry-After"))
	}

	rig.srv.SetUpdateChecker(updatecheck.New(updatecheck.Config{SourceErr: release.ErrSourceOff, Current: "0.2.0"}))
	if w := callAs(t, rig.h, http.MethodPost, "/updates/check", rig.opToken, "", nil); w.Code != http.StatusConflict {
		t.Errorf("check with lookups off = %d, want 409", w.Code)
	}
}

func TestUpdates_PackageOnlyForOfferedVersions(t *testing.T) {
	rig, _ := updatesRig(t)
	for _, to := range []string{"0.1.0", "0.2.0", "9.9.9", "../etc"} {
		if w := callAs(t, rig.h, http.MethodGet, "/updates/package?to="+to, rig.opToken, "", nil); w.Code != http.StatusNotFound {
			t.Errorf("package to=%s = %d, want 404", to, w.Code)
		}
	}
	if w := callAs(t, rig.h, http.MethodGet, "/updates/package", rig.opToken, "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("package without to = %d, want 400", w.Code)
	}
	if w := callAs(t, rig.h, http.MethodGet, "/updates/package?to=0.3.0&format=html", rig.opToken, "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("package format=html = %d, want 400", w.Code)
	}

	w := callAs(t, rig.h, http.MethodGet, "/updates/package?to=0.3.0", rig.opToken, "", nil)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("md package = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "0.3.0") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "thulu update -version 0.3.0") {
		t.Errorf("md package headers %v body:\n%s", w.Header(), w.Body.String())
	}

	w = callAs(t, rig.h, http.MethodGet, "/updates/package?to=0.3.0&format=json", rig.opToken, "", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("json package = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	var pkg map[string]any
	decodeInto(t, w.Body.Bytes(), &pkg)
	if pkg["to"] != "0.3.0" || pkg["from"] != "0.2.0" {
		t.Errorf("json package = %v", pkg)
	}

	entries := auditKinds(t, rig, string(audit.KindUpdatePackage))
	if entries < 2 {
		t.Errorf("%d update_package audit entries, want 2", entries)
	}
}

func TestUpdates_PackageFollowsTheRequestLocale(t *testing.T) {
	rig, _ := updatesRig(t)
	for locale, title := range map[string]string{
		"es": "# Paquete de cambio de Thulu: 0.2.0 → 0.3.0",
		"en": "# Thulu change package: 0.2.0 → 0.3.0",
	} {
		r := httptest.NewRequest(http.MethodGet, "/updates/package?to=0.3.0", nil)
		r.Header.Set("Authorization", "Bearer "+rig.opToken)
		r.AddCookie(&http.Cookie{Name: i18n.LocaleCookie, Value: locale})
		w := httptest.NewRecorder()
		rig.h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), title) {
			t.Errorf("%s package = %d, starts %q", locale, w.Code, firstLine(w.Body.String()))
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestUpdates_PackageReportsMissingRequiredEnv(t *testing.T) {
	rig, _ := updatesRig(t)
	t.Setenv("OCTOPUS_SECRETBOX_KEY", "")
	w := callAs(t, rig.h, http.MethodGet, "/updates/status", rig.opToken, "", nil)
	var st updateStatusView
	decodeInto(t, w.Body.Bytes(), &st)
	if len(st.Available) != 1 || !strings.Contains(strings.Join(st.Available[0].Blockers, "\n"), "OCTOPUS_SECRETBOX_KEY") {
		t.Errorf("blockers = %+v", st.Available)
	}
	t.Setenv("OCTOPUS_SECRETBOX_KEY", "set")
	w = callAs(t, rig.h, http.MethodGet, "/updates/status", rig.opToken, "", nil)
	decodeInto(t, w.Body.Bytes(), &st)
	if len(st.Available[0].Blockers) != 0 {
		t.Errorf("blockers with the variable set = %v", st.Available[0].Blockers)
	}
}

func TestUpdates_UnverifiedPreviewShowsOnlyTheListing(t *testing.T) {
	rig := newOperatorRig(t)
	setVersion(t, "0.3.0")
	orig := release.Cosign
	t.Cleanup(func() { release.Cosign = orig })
	release.Cosign = func(_ context.Context, _ ...string) ([]byte, error) { return nil, release.ErrNoCosign }

	src := &checktest.Source{}
	src.PublishPreview(t, "0.3.1-dev.gabc1234")
	c := updatecheck.New(updatecheck.Config{
		Source:   src,
		Channels: map[string]bool{release.ChannelStable: true, release.ChannelPreview: true},
		Keys:     checktest.NewSigner(t).Keys,
		Current:  "0.3.0",
	})
	if err := c.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	rig.srv.SetUpdateChecker(c)

	w := callAs(t, rig.h, http.MethodGet, "/updates/status", rig.opToken, "", nil)
	var st updateStatusView
	decodeInto(t, w.Body.Bytes(), &st)
	if len(st.Available) != 1 {
		t.Fatalf("available = %+v", st.Available)
	}
	rel := st.Available[0]
	if rel.Unverified == "" || rel.Command != "thulu update -version 0.3.1-dev.gabc1234" ||
		rel.Commit != "" || rel.Migrations.Added != 0 || len(rel.Requires) != 0 || rel.UpgradeFrom != "" {
		t.Errorf("unverified preview shows manifest facts: %+v", rel)
	}
	if w := callAs(t, rig.h, http.MethodGet, "/updates/package?to=0.3.1-dev.gabc1234", rig.opToken, "", nil); w.Code != http.StatusNotFound {
		t.Errorf("package for an unverified preview = %d, want 404", w.Code)
	}
}

func TestUpdates_VersionsKeptOnTheDockerHost(t *testing.T) {
	rig, _ := updatesRig(t)
	setVersion(t, "0.3.0")
	t.Setenv(kept.EnvVersions, "0.3.0,0.2.0,0.1.0")
	t.Setenv(kept.EnvSnapshots, "0.2.0=octopus_20261001_120000.sql")

	w := callAs(t, rig.h, http.MethodGet, "/updates/versions", rig.opToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("versions = %d: %s", w.Code, w.Body.String())
	}
	var out keptVersionsView
	decodeInto(t, w.Body.Bytes(), &out)
	if out.Mode != modeDocker || len(out.Versions) != 3 {
		t.Fatalf("versions = %+v", out)
	}
	cur, prev, old := out.Versions[0], out.Versions[1], out.Versions[2]
	if !cur.Current || cur.Command != "" {
		t.Errorf("current row = %+v", cur)
	}
	if prev.Command != "thulu update -to 0.2.0 -restore octopus_20261001_120000.sql" || prev.SnapshotMissing {
		t.Errorf("0.2.0 row = %+v", prev)
	}
	if !old.SnapshotMissing || !strings.Contains(old.Command, "<snapshot>") {
		t.Errorf("0.1.0 row = %+v", old)
	}

	t.Setenv(kept.EnvVersions, "")
	w = callAs(t, rig.h, http.MethodGet, "/updates/versions", rig.opToken, "", nil)
	decodeInto(t, w.Body.Bytes(), &out)
	if len(out.Versions) != 1 || out.Versions[0].Version != "0.3.0" || !out.Versions[0].Current {
		t.Errorf("versions with no list = %+v", out.Versions)
	}

	t.Setenv("OCTOPUS_AGENT_RUNTIME", "kubernetes")
	w = callAs(t, rig.h, http.MethodGet, "/updates/versions", rig.opToken, "", nil)
	decodeInto(t, w.Body.Bytes(), &out)
	if out.Mode != modeKubernetes || len(out.Versions) != 0 {
		t.Errorf("kubernetes versions = %+v", out)
	}
}

func validUpdateEvent() map[string]string {
	now := time.Now().UTC()
	return map[string]string{
		"from": "0.2.0", "to": "0.3.0", "result": "ok", "mode": "docker",
		"operator":    "ops@host-1",
		"started_at":  now.Add(-3 * time.Minute).Format(time.RFC3339),
		"finished_at": now.Format(time.RFC3339),
		"detail":      "pulled 2 images\nmigrations 17→19",
	}
}

func TestUpdates_EventsAreValidatedAndRecorded(t *testing.T) {
	rig, _ := updatesRig(t)
	writeKey := mintKey(t, rig.srv, rig.opWS, auth.RoleAdmin, auth.PermSettingsWrite)

	with := func(k, v string) map[string]string {
		ev := validUpdateEvent()
		ev[k] = v
		return ev
	}
	now := time.Now().UTC()
	bad := map[string]any{
		"bad result":           with("result", "maybe"),
		"bad mode":             with("mode", "nomad"),
		"bad to":               with("to", "0.3.0; rm -rf /"),
		"missing to":           with("to", ""),
		"control chars":        with("operator", "ops\x1b[31m@host"),
		"newline in operator":  with("operator", "ops\n@host"),
		"oversize detail":      with("detail", strings.Repeat("x", maxUpdateDetail+1)),
		"bad date":             with("started_at", "yesterday"),
		"finished before":      with("finished_at", now.Add(-time.Hour).Format(time.RFC3339)),
		"finished in a future": with("finished_at", now.Add(3*time.Hour).Format(time.RFC3339)),
		"unknown field":        `{"to":"0.3.0","result":"ok","mode":"docker","started_at":"` + now.Format(time.RFC3339) + `","finished_at":"` + now.Format(time.RFC3339) + `","image":"evil"}`,
		"not json":             "nope",
	}
	for name, body := range bad {
		if w := callAs(t, rig.h, http.MethodPost, "/updates/events", "", writeKey, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, w.Code, w.Body.String())
		}
	}

	ev := validUpdateEvent()
	if w := callAs(t, rig.h, http.MethodPost, "/updates/events", "", writeKey, ev); w.Code != http.StatusCreated {
		t.Fatalf("valid event = %d: %s", w.Code, w.Body.String())
	}
	first := with("from", "")
	first["result"] = "failed"
	if w := callAs(t, rig.h, http.MethodPost, "/updates/events", "", writeKey, first); w.Code != http.StatusCreated {
		t.Fatalf("event without from = %d: %s", w.Code, w.Body.String())
	}

	w := callAs(t, rig.h, http.MethodGet, "/updates/history", rig.opToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("history = %d: %s", w.Code, w.Body.String())
	}
	var hist []updateHistoryView
	decodeInto(t, w.Body.Bytes(), &hist)
	if len(hist) != 2 {
		t.Fatalf("history = %+v", hist)
	}
	got := hist[1]
	if got.Kind != string(audit.KindUpdateReported) || got.From != "0.2.0" || got.To != "0.3.0" || got.Result != "ok" ||
		got.Operator != "ops@host-1" || got.Detail != ev["detail"] || got.Actor != "test key" {
		t.Errorf("history entry = %+v", got)
	}
	if hist[0].Result != "failed" || hist[0].From != "" {
		t.Errorf("newest entry = %+v", hist[0])
	}

	w = callAs(t, rig.h, http.MethodGet, "/updates/history?limit=1", rig.opToken, "", nil)
	decodeInto(t, w.Body.Bytes(), &hist)
	if len(hist) != 1 {
		t.Errorf("history limit=1 = %d entries", len(hist))
	}
}

func TestUpdates_UnknownRoutesAnswerJSON(t *testing.T) {
	rig, _ := updatesRig(t)
	for _, path := range []string{"/updates", "/updates/", "/updates/nope", "/updates/status/x"} {
		w := callAs(t, rig.h, http.MethodGet, path, rig.opToken, "", nil)
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("GET %s = %d %q, want a JSON 404", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

func auditKinds(t *testing.T, rig *operatorRig, kind string) int {
	t.Helper()
	w := callAs(t, rig.h, http.MethodGet, "/audit?kind="+kind, rig.opToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /audit = %d: %s", w.Code, w.Body.String())
	}
	var list []json.RawMessage
	decodeInto(t, w.Body.Bytes(), &list)
	return len(list)
}
