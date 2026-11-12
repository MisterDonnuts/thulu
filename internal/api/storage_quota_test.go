package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/entitlements"
)

func setStorageQuota(t *testing.T, rig *operatorRig, quota int64) {
	t.Helper()
	ent := entitlements.DefaultFor(entitlements.PlanTrial)
	ent.WorkspaceID = rig.tenantWS
	ent.StorageQuotaBytes = quota
	if err := rig.srv.entitlements.Set(ent); err != nil {
		t.Fatal(err)
	}
}

func uploadMultipart(t *testing.T, h http.Handler, token, taskID string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/attachments", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type entitlementsBody struct {
	Plan              string                 `json:"plan"`
	StorageQuotaBytes int64                  `json:"storage_quota_bytes"`
	ExtraHoursCapUSD  float64                `json:"extra_hours_cap_usd"`
	Hours             entitlements.HourUsage `json:"hours"`
	Storage           struct {
		UsedBytes      int64 `json:"used_bytes"`
		QuotaBytes     int64 `json:"quota_bytes"`
		RemainingBytes int64 `json:"remaining_bytes"`
	} `json:"storage"`
}

func decodeEntitlements(t *testing.T, w *httptest.ResponseRecorder) entitlementsBody {
	t.Helper()
	var b entitlementsBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return b
}

func TestStorageQuota_UploadsStopAtTheQuota(t *testing.T) {
	rig := newOperatorRig(t)
	setStorageQuota(t, rig, 10)
	taskID := createTaskViaAPI(t, rig.h, rig.tenantToken, "quota")

	w := uploadBase64(t, rig.h, rig.tenantToken, taskID, map[string]any{
		"filename": "a.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("12345678")),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("first upload = %d: %s", w.Code, w.Body.String())
	}
	w = uploadBase64(t, rig.h, rig.tenantToken, taskID, map[string]any{
		"filename": "b.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("12345678")),
	})
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "storage_quota_exceeded") {
		t.Fatalf("over-quota JSON upload = %d, want 413 storage_quota_exceeded: %s", w.Code, w.Body.String())
	}
	w = uploadMultipart(t, rig.h, rig.tenantToken, taskID, []byte("12345678"))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-quota multipart upload = %d, want 413: %s", w.Code, w.Body.String())
	}

	g := callAs(t, rig.h, http.MethodGet, "/entitlements", rig.tenantToken, "", nil)
	if g.Code != http.StatusOK {
		t.Fatalf("GET /entitlements = %d: %s", g.Code, g.Body.String())
	}
	b := decodeEntitlements(t, g)
	if b.Storage.UsedBytes != 8 || b.Storage.QuotaBytes != 10 || b.Storage.RemainingBytes != 2 {
		t.Errorf("storage usage = %+v, want 8 used of 10", b.Storage)
	}
	if b.Plan != "trial" || b.Hours.IncludedHours != 50 || b.Hours.UsedHours != 0 || b.Hours.ResetsAt.IsZero() {
		t.Errorf("plan/hours = %s %+v, want the trial with 50 h unused", b.Plan, b.Hours)
	}
}

func TestStorageQuota_TenantCannotLiftItsOwnQuota(t *testing.T) {
	rig := newOperatorRig(t)
	setStorageQuota(t, rig, 10)
	for _, cred := range [][2]string{{rig.tenantToken, ""}, {"", rig.tenantAdminKey}} {
		w := callAs(t, rig.h, http.MethodPut, "/entitlements", cred[0], cred[1],
			map[string]any{"storage_quota_bytes": 0})
		if w.Code != http.StatusForbidden {
			t.Errorf("tenant override = %d, want 403: %s", w.Code, w.Body.String())
		}
	}
	if ent, _ := rig.srv.entitlements.Get(rig.tenantWS); ent.StorageQuotaBytes != 10 {
		t.Errorf("tenant quota = %d after its own attempt, want 10", ent.StorageQuotaBytes)
	}
}

func TestStorageQuota_OperatorOverride(t *testing.T) {
	rig := newOperatorRig(t)
	path := "/entitlements?workspace=" + rig.tenantWS
	w := callAs(t, rig.h, http.MethodPut, path, "", rig.opKey, map[string]any{"storage_quota_bytes": 1 << 20})
	if w.Code != http.StatusOK {
		t.Fatalf("operator override = %d: %s", w.Code, w.Body.String())
	}
	b := decodeEntitlements(t, w)
	if b.Plan != "trial" || b.StorageQuotaBytes != 1<<20 || b.Storage.QuotaBytes != 1<<20 {
		t.Errorf("after override = %+v", b)
	}
	ent, _ := rig.srv.entitlements.Get(rig.tenantWS)
	if ent.SeatLimit != 4 || ent.HourQuotaMonthly != 50 {
		t.Errorf("other limits changed: %+v", ent)
	}

	w = callAs(t, rig.h, http.MethodPut, path, "", rig.opKey,
		map[string]any{"plan": "team", "agents": 3, "hour_quota_monthly": 250})
	if w.Code != http.StatusOK {
		t.Fatalf("plan + override = %d: %s", w.Code, w.Body.String())
	}
	if ent, _ := rig.srv.entitlements.Get(rig.tenantWS); ent.Plan != entitlements.PlanTeam ||
		ent.ConcurrencyLimit != 3 || ent.HourQuotaMonthly != 250 || ent.StorageQuotaBytes != 30*entitlements.GiB {
		t.Errorf("team with an hour override = %+v", ent)
	}

	w = callAs(t, rig.h, http.MethodPut, path, "", rig.opKey, map[string]any{"storage_quota_bytes": -1})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "negative_limit") {
		t.Errorf("negative override = %d, want 400 negative_limit: %s", w.Code, w.Body.String())
	}
}

func TestExtraHoursCap_WorkspaceAdminSetsIt(t *testing.T) {
	rig := newOperatorRig(t)
	if err := rig.srv.entitlements.ApplyPlan(rig.tenantWS, entitlements.PlanStarter, 0, 0); err != nil {
		t.Fatal(err)
	}
	w := callAs(t, rig.h, http.MethodPut, "/entitlements/extra-hours-cap", "", rig.tenantAdminKey,
		map[string]any{"cap_usd": 120})
	if w.Code != http.StatusOK {
		t.Fatalf("set cap = %d: %s", w.Code, w.Body.String())
	}
	if b := decodeEntitlements(t, w); b.Hours.CapUSD != 120 || b.ExtraHoursCapUSD != 120 {
		t.Errorf("cap after set = %+v / %v", b.Hours, b.ExtraHoursCapUSD)
	}
	for _, bad := range []any{-1, 1e9, nil} {
		w = callAs(t, rig.h, http.MethodPut, "/entitlements/extra-hours-cap", "", rig.tenantAdminKey,
			map[string]any{"cap_usd": bad})
		if w.Code != http.StatusBadRequest {
			t.Errorf("cap %v = %d, want 400", bad, w.Code)
		}
	}
	if ent, _ := rig.srv.entitlements.Get(rig.tenantWS); ent.ExtraCap() != 120 || ent.Plan != entitlements.PlanStarter {
		t.Errorf("stored = %+v", ent)
	}
}
