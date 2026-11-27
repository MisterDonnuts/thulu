package settings

import "context"

// AgentTurnsPerThreadKey caps how many replies agents give each other in one comment thread before
// they wait for a person, so two agents that keep mentioning each other stop spending the
// workspace's tokens. A person's reply starts the count again.
const AgentTurnsPerThreadKey = "agent_turns_per_thread"

const (
	DefaultAgentTurnsPerThread = 5
	MaxAgentTurnsPerThread     = 20
)

// ResolveAgentTurns turns a stored value into the setting's view; a value out of range (written
// before the range existed, or by hand) reads as unset.
func ResolveAgentTurns(value *int) RunLimit {
	out := RunLimit{Default: DefaultAgentTurnsPerThread, Ceiling: MaxAgentTurnsPerThread, Effective: DefaultAgentTurnsPerThread}
	if value != nil && *value >= 1 && *value <= MaxAgentTurnsPerThread {
		v := *value
		out.Value, out.Effective = &v, v
	}
	return out
}

func (s *Store) WorkspaceAgentTurns(ctx context.Context, workspaceID string) (RunLimit, error) {
	value, err := s.workspaceInt(ctx, workspaceID, AgentTurnsPerThreadKey)
	if err != nil {
		return ResolveAgentTurns(nil), err
	}
	return ResolveAgentTurns(value), nil
}

// AgentTurnsPerThread is the cap in effect for the workspace; the default when it cannot be read.
func (s *Store) AgentTurnsPerThread(ctx context.Context, workspaceID string) int {
	limit, err := s.WorkspaceAgentTurns(ctx, workspaceID)
	if err != nil {
		return DefaultAgentTurnsPerThread
	}
	return limit.Effective
}
