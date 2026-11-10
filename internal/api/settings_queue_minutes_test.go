package api

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/settings"
)

func TestValidateMaxQueueMinutes(t *testing.T) {
	for _, c := range []struct {
		raw string
		ok  bool
	}{
		{"1", true}, {"120", true}, {strconv.Itoa(settings.MaxQueueMinutes), true},
		{"0", false}, {"-5", false}, {strconv.Itoa(settings.MaxQueueMinutes + 1), false}, {`"soon"`, false},
	} {
		numeric, err := validateNumericSetting(settings.AgentMaxQueueMinutesKey, json.RawMessage(c.raw))
		if !numeric {
			t.Fatal("agent.max_queue_minutes must be validated as a number")
		}
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.raw, err, c.ok)
		}
	}
}
