package settings

import (
	"context"
	"time"

	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	AgentMaxRunMinutesKey = "agent.max_run_minutes"

	AgentMaxRunMinutesCeilingKey = "agent.max_run_minutes_ceiling"

	AgentMaxQueueMinutesKey = "agent.max_queue_minutes"

	SandboxMaxExecTimeoutMsKey = "sandbox.max_exec_timeout_ms"

	ChatMemoryBytesKey = "chat.memory_bytes"
	ChatNanoCPUsKey    = "chat.nano_cpus"
	ChatPidsLimitKey   = "chat.pids_limit"
)

const (
	SaaSDefaultMaxRunMinutes = 60

	// A hung engine would otherwise hold its slot and burn CPU until someone stops it.
	SelfHostedDefaultMaxRunMinutes = 240

	SaaSDefaultMaxRunMinutesCeiling = 240

	// A run's API key lives 24 hours; a run allowed longer would lose its key midway.
	MaxRunMinutesCeiling = 24 * 60

	// MaxQueueMinutes keeps a wait inside the 24-hour life of the run key minted before it.
	DefaultMaxQueueMinutes = 120
	MaxQueueMinutes        = 720

	DefaultSandboxMaxExecTimeoutMs = 30 * 60 * 1000

	DefaultChatNanoCPUs int64 = 2_000_000_000
)

var limitDefaults = map[string]any{
	AgentMaxRunMinutesKey:        0,
	AgentMaxRunMinutesCeilingKey: 0,
	AgentMaxQueueMinutesKey:      DefaultMaxQueueMinutes,
	SandboxMaxExecTimeoutMsKey:   DefaultSandboxMaxExecTimeoutMs,
	ChatMemoryBytesKey:           DefaultAgentMemoryBytes,
	ChatNanoCPUsKey:              DefaultChatNanoCPUs,
	ChatPidsLimitKey:             1024,
}

func init() {
	for k, v := range limitDefaults {
		defaults[k] = v
	}
}

func EffectiveMaxRunMinutes(configured int, saas bool) int {
	if configured > 0 {
		return configured
	}
	if saas {
		return SaaSDefaultMaxRunMinutes
	}
	return SelfHostedDefaultMaxRunMinutes
}

func EffectiveMaxRunMinutesCeiling(configured int, saas bool) int {
	switch {
	case configured > 0:
		return min(configured, MaxRunMinutesCeiling)
	case saas:
		return SaaSDefaultMaxRunMinutesCeiling
	}
	return MaxRunMinutesCeiling
}

type RunLimit struct {
	Value     *int `json:"value"`
	Default   int  `json:"default"`
	Ceiling   int  `json:"ceiling"`
	Effective int  `json:"effective"`
}

func ResolveRunLimit(value *int, configuredDefault, configuredCeiling int, saas bool) RunLimit {
	out := RunLimit{
		Default: EffectiveMaxRunMinutes(configuredDefault, saas),
		Ceiling: EffectiveMaxRunMinutesCeiling(configuredCeiling, saas),
	}
	out.Effective = out.Default
	if value != nil && *value > 0 {
		v := *value
		out.Value = &v
		out.Effective = min(v, out.Ceiling)
	}
	return out
}

func EffectiveAgentCap(configured int, saas bool, numCPU int) int {
	if configured > 0 {
		return configured
	}
	if saas {
		return max(2, numCPU-2)
	}
	return 0
}

func (s *Store) AgentMaxRunDuration(ctx context.Context, saas bool) time.Duration {
	n, err := s.getInt(ctx, AgentMaxRunMinutesKey, 0)
	if err != nil {
		n = 0
	}
	return time.Duration(EffectiveMaxRunMinutes(n, saas)) * time.Minute
}

func (s *Store) WorkspaceRunLimit(ctx context.Context, workspaceID string, saas bool) (RunLimit, error) {
	def, err := s.getInt(ctx, AgentMaxRunMinutesKey, 0)
	if err != nil {
		return RunLimit{}, err
	}
	ceiling, err := s.getInt(ctx, AgentMaxRunMinutesCeilingKey, 0)
	if err != nil {
		return RunLimit{}, err
	}
	value, err := s.workspaceInt(ctx, workspaceID, AgentMaxRunMinutesKey)
	if err != nil {
		return RunLimit{}, err
	}
	return ResolveRunLimit(value, def, ceiling, saas), nil
}

func (s *Store) WorkspaceMaxRunDuration(ctx context.Context, workspaceID string, saas bool) time.Duration {
	limit, err := s.WorkspaceRunLimit(ctx, workspaceID, saas)
	if err != nil {
		return s.AgentMaxRunDuration(ctx, saas)
	}
	return time.Duration(limit.Effective) * time.Minute
}

func (s *Store) AgentMaxQueueMinutes(ctx context.Context) int {
	n := s.int64OrDefault(ctx, AgentMaxQueueMinutesKey, DefaultMaxQueueMinutes)
	if n < 1 || n > MaxQueueMinutes {
		return DefaultMaxQueueMinutes
	}
	return int(n)
}

func (s *Store) SandboxMaxExecTimeoutMs(ctx context.Context) int64 {
	n := s.int64OrDefault(ctx, SandboxMaxExecTimeoutMsKey, DefaultSandboxMaxExecTimeoutMs)
	if n <= 0 {
		return DefaultSandboxMaxExecTimeoutMs
	}
	return n
}

func (s *Store) ChatResourceCaps(ctx context.Context) types.ResourceCaps {
	return types.ResourceCaps{
		MemoryBytes: max(s.int64OrDefault(ctx, ChatMemoryBytesKey, DefaultAgentMemoryBytes), 0),
		PidsLimit:   max(s.int64OrDefault(ctx, ChatPidsLimitKey, 1024), 0),
		NanoCPUs:    max(s.int64OrDefault(ctx, ChatNanoCPUsKey, DefaultChatNanoCPUs), 0),
	}
}
