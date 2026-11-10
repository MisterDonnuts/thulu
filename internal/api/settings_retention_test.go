package api

import (
	"encoding/json"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/settings"
)

func TestValidateSetting_RetentionKeys(t *testing.T) {
	cases := []struct {
		key, raw string
		ok       bool
	}{
		{settings.RetentionSessionsDaysKey, `90`, true},
		{settings.RetentionRunLogsDaysKey, `0`, true},
		{settings.RetentionRunKeysDaysKey, `-1`, false},
		{settings.RetentionStuckWorktreeDaysKey, `"14"`, false},
		{settings.RetentionCacheMetricsDaysKey, `90`, true},
		{settings.RetentionCacheMetricsDaysKey, `-1`, false},
		{settings.JanitorDiskFreeMinPercentKey, `10`, true},
		{settings.JanitorDiskFreeMinPercentKey, `0`, true},
		{settings.JanitorDiskFreeMinPercentKey, `101`, false},
		{settings.AuditRetentionDaysKey, `180`, true},
		{settings.AuditRetentionDaysKey, `0`, true},
		{settings.AuditRetentionDaysKey, `-1`, false},
		{settings.AuditRetentionDaysKey, `"180"`, false},
	}
	for _, tc := range cases {
		err := validateSetting(tc.key, json.RawMessage(tc.raw))
		if (err == nil) != tc.ok {
			t.Errorf("validateSetting(%s, %s) = %v, want ok=%t", tc.key, tc.raw, err, tc.ok)
		}
	}
}

func TestValidateSetting_RefusesNull(t *testing.T) {
	for _, key := range []string{
		settings.SandboxMaxKey, settings.SandboxIdleEvictSecondsKey, settings.SandboxAcquireBlockMsKey,
		settings.AgentMaxContainersKey, settings.MentionMaxContainersKey, settings.ChatMaxKey,
		settings.AgentMaxQueueMinutesKey,
	} {
		if err := validateSetting(key, json.RawMessage(`null`)); err == nil {
			t.Errorf("validateSetting(%s, null) accepted it", key)
		}
		if err := validateSetting(key, json.RawMessage(` null `)); err == nil {
			t.Errorf("validateSetting(%s, ' null ') accepted it", key)
		}
	}
}

func TestValidateSetting_CapacityKeys(t *testing.T) {
	cases := []struct {
		key, raw string
		ok       bool
	}{
		{settings.AgentMaxContainersKey, `0`, true},
		{settings.AgentMaxContainersKey, `8`, true},
		{settings.AgentMaxContainersKey, `-1`, false},
		{settings.AgentMaxContainersKey, `"4"`, false},
		{settings.MentionMaxContainersKey, `0`, true},
		{settings.MentionMaxContainersKey, `2.5`, false},
		{settings.ChatMaxKey, `1`, true},
		{settings.ChatMaxKey, `0`, false},
	}
	for _, tc := range cases {
		err := validateSetting(tc.key, json.RawMessage(tc.raw))
		if (err == nil) != tc.ok {
			t.Errorf("validateSetting(%s, %s) = %v, want ok=%t", tc.key, tc.raw, err, tc.ok)
		}
	}
}
