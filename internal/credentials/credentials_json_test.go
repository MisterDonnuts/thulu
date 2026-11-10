package credentials

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCredentialOmitsZeroTimes(t *testing.T) {
	raw, err := json.Marshal(Credential{ID: "c1", Name: "gh", Provider: "github", State: StateValid})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"validated_at"`, `"expires_at"`} {
		if strings.Contains(string(raw), field) {
			t.Errorf("%s present in %s", field, raw)
		}
	}

	when := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	raw, err = json.Marshal(Credential{ID: "c1", ValidatedAt: when, ExpiresAt: when})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"validated_at":"2026-10-16T12:00:00Z"`) ||
		!strings.Contains(string(raw), `"expires_at":"2026-10-16T12:00:00Z"`) {
		t.Errorf("set times must still be sent: %s", raw)
	}
}
