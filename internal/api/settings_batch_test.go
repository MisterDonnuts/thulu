package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/settings"
)

func TestValidateNumericSettingRefusesNull(t *testing.T) {
	for _, key := range []string{
		settings.SandboxIdleEvictSecondsKey, settings.SandboxAcquireBlockMsKey, settings.SandboxMaxKey,
	} {
		for _, raw := range []string{"null", " null "} {
			if _, err := validateNumericSetting(key, json.RawMessage(raw)); err == nil {
				t.Errorf("%s = %q was accepted", key, raw)
			}
		}
		if _, err := validateNumericSetting(key, json.RawMessage("5")); err != nil {
			t.Errorf("%s = 5: %v", key, err)
		}
	}
}

func TestPatchSettingsIsAllOrNothing(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	read := func(key string) string {
		t.Helper()
		w := getJSON(t, h, "/settings/"+key, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /settings/%s = %d: %s", key, w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	w := patchJSONAuth(t, h, "/settings", "", map[string]any{
		settings.SandboxMaxKey:              3,
		settings.SandboxIdleEvictSecondsKey: 600,
		settings.SandboxAcquireBlockMsKey:   1500,
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("PATCH /settings = %d: %s", w.Code, w.Body.String())
	}
	if got := read(settings.SandboxMaxKey); got != "3" {
		t.Errorf("sandbox.max = %s, want 3", got)
	}
	if got := read(settings.SandboxAcquireBlockMsKey); got != "1500" {
		t.Errorf("sandbox.acquire_block_ms = %s, want 1500", got)
	}

	w = patchJSONAuth(t, h, "/settings", "", map[string]any{
		settings.SandboxMaxKey:              7,
		settings.SandboxIdleEvictSecondsKey: nil,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with a null = %d, want 400: %s", w.Code, w.Body.String())
	}
	var refusal struct {
		Setting string `json:"setting"`
		Code    string `json:"code"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Setting != settings.SandboxIdleEvictSecondsKey || refusal.Code == "" || refusal.Error == "" {
		t.Errorf("refusal = %s, want setting %s with a code and a message", w.Body.String(), settings.SandboxIdleEvictSecondsKey)
	}
	if got := read(settings.SandboxMaxKey); got != "3" {
		t.Errorf("a refused PATCH still wrote sandbox.max = %s", got)
	}
	if got := read(settings.SandboxIdleEvictSecondsKey); got != "600" {
		t.Errorf("a refused PATCH changed sandbox.idle_evict_seconds to %s", got)
	}

	if w := patchJSONAuth(t, h, "/settings", "", map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("empty PATCH = %d, want 400", w.Code)
	}
}
