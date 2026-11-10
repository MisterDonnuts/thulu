package settings

import (
	"testing"
	"time"
)

func TestEffectiveAgentCap(t *testing.T) {
	cases := []struct {
		configured int
		saas       bool
		cpus       int
		want       int
	}{
		{0, false, 16, 0},
		{0, true, 16, 14},
		{0, true, 2, 2},
		{0, true, 1, 2},
		{5, true, 16, 5},
		{5, false, 16, 5},
	}
	for _, c := range cases {
		if got := EffectiveAgentCap(c.configured, c.saas, c.cpus); got != c.want {
			t.Errorf("EffectiveAgentCap(%d, %v, %d) = %d, want %d", c.configured, c.saas, c.cpus, got, c.want)
		}
	}
}

func TestEffectiveMaxRunMinutes(t *testing.T) {
	if got := EffectiveMaxRunMinutes(0, false); got != SelfHostedDefaultMaxRunMinutes {
		t.Errorf("self-hosted 0 = %d, want %d", got, SelfHostedDefaultMaxRunMinutes)
	}
	if got := EffectiveMaxRunMinutes(0, true); got != SaaSDefaultMaxRunMinutes {
		t.Errorf("SaaS 0 = %d, want %d", got, SaaSDefaultMaxRunMinutes)
	}
	if got := EffectiveMaxRunMinutes(-5, true); got != SaaSDefaultMaxRunMinutes {
		t.Errorf("SaaS -5 = %d, want %d", got, SaaSDefaultMaxRunMinutes)
	}
	if got := EffectiveMaxRunMinutes(15, true); got != 15 {
		t.Errorf("explicit 15 = %d", got)
	}
}

func TestEffectiveMaxRunMinutesCeiling(t *testing.T) {
	cases := []struct {
		configured int
		saas       bool
		want       int
	}{
		{0, true, SaaSDefaultMaxRunMinutesCeiling},
		{0, false, MaxRunMinutesCeiling},
		{-3, true, SaaSDefaultMaxRunMinutesCeiling},
		{90, true, 90},
		{90, false, 90},
		{MaxRunMinutesCeiling + 1, false, MaxRunMinutesCeiling},
	}
	for _, c := range cases {
		if got := EffectiveMaxRunMinutesCeiling(c.configured, c.saas); got != c.want {
			t.Errorf("EffectiveMaxRunMinutesCeiling(%d, %v) = %d, want %d", c.configured, c.saas, got, c.want)
		}
	}
}

func TestResolveRunLimit(t *testing.T) {
	n := func(v int) *int { return &v }
	cases := []struct {
		name              string
		value             *int
		def, ceiling      int
		saas              bool
		wantEff, wantCeil int
		wantDef           int
		wantValue         *int
	}{
		{"no value, SaaS default", nil, 0, 0, true, 60, 240, 60, nil},
		{"no value, self-hosted default", nil, 0, 0, false, 240, 1440, 240, nil},
		{"no value, default above ceiling", nil, 300, 120, true, 300, 120, 300, nil},
		{"own value under the ceiling", n(180), 0, 0, true, 180, 240, 60, n(180)},
		{"own value above a lowered ceiling", n(180), 0, 90, true, 90, 90, 60, n(180)},
		{"own value self-hosted", n(600), 0, 0, false, 600, 1440, 240, n(600)},
		{"zero value", n(0), 0, 0, true, 60, 240, 60, nil},
		{"negative value", n(-5), 30, 0, false, 30, 1440, 30, nil},
	}
	for _, c := range cases {
		got := ResolveRunLimit(c.value, c.def, c.ceiling, c.saas)
		if got.Effective != c.wantEff || got.Ceiling != c.wantCeil || got.Default != c.wantDef {
			t.Errorf("%s: got %+v, want effective=%d ceiling=%d default=%d", c.name, got, c.wantEff, c.wantCeil, c.wantDef)
		}
		if (got.Value == nil) != (c.wantValue == nil) || (got.Value != nil && *got.Value != *c.wantValue) {
			t.Errorf("%s: value = %v, want %v", c.name, got.Value, c.wantValue)
		}
	}
}

func TestLimitDefaultsAreSeededAndKnown(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	for k := range limitDefaults {
		if !KnownKey(k) {
			t.Errorf("%s is not a known key", k)
		}
		if _, err := s.GetRaw(ctx, k); err != nil {
			t.Errorf("%s not seeded: %v", k, err)
		}
	}
	if d := s.AgentMaxRunDuration(ctx, false); d != 4*time.Hour {
		t.Errorf("self-hosted run limit = %s, want 4h", d)
	}
	if d := s.AgentMaxRunDuration(ctx, true); d != 60*time.Minute {
		t.Errorf("SaaS run limit = %s, want 1h", d)
	}
	if ms := s.SandboxMaxExecTimeoutMs(ctx); ms != DefaultSandboxMaxExecTimeoutMs {
		t.Errorf("sandbox exec cap = %d", ms)
	}
	caps := s.ChatResourceCaps(ctx)
	if caps.MemoryBytes != DefaultAgentMemoryBytes || caps.NanoCPUs != DefaultChatNanoCPUs || caps.PidsLimit != 1024 {
		t.Errorf("chat caps = %+v", caps)
	}
}

func TestSandboxMaxExecTimeoutZeroFallsBack(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutJSON(t.Context(), SandboxMaxExecTimeoutMsKey, 0); err != nil {
		t.Fatal(err)
	}
	if ms := s.SandboxMaxExecTimeoutMs(t.Context()); ms != DefaultSandboxMaxExecTimeoutMs {
		t.Errorf("zero cap read as %d, want the default", ms)
	}
}

func TestAgentMaxQueueMinutes(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if got := s.AgentMaxQueueMinutes(ctx); got != DefaultMaxQueueMinutes {
		t.Errorf("seeded = %d, want %d", got, DefaultMaxQueueMinutes)
	}
	for v, want := range map[int]int{30: 30, MaxQueueMinutes: MaxQueueMinutes, 0: DefaultMaxQueueMinutes,
		-1: DefaultMaxQueueMinutes, MaxQueueMinutes + 1: DefaultMaxQueueMinutes} {
		if err := s.PutJSON(ctx, AgentMaxQueueMinutesKey, v); err != nil {
			t.Fatal(err)
		}
		if got := s.AgentMaxQueueMinutes(ctx); got != want {
			t.Errorf("%d read as %d, want %d", v, got, want)
		}
	}
}
