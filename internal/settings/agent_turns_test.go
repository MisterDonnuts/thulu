package settings

import "testing"

func TestResolveAgentTurns(t *testing.T) {
	ptr := func(v int) *int { return &v }
	for _, tc := range []struct {
		name  string
		value *int
		want  int
		set   bool
	}{
		{"unset", nil, DefaultAgentTurnsPerThread, false},
		{"own value", ptr(3), 3, true},
		{"the ceiling", ptr(MaxAgentTurnsPerThread), MaxAgentTurnsPerThread, true},
		{"zero reads as unset", ptr(0), DefaultAgentTurnsPerThread, false},
		{"above the ceiling reads as unset", ptr(MaxAgentTurnsPerThread + 1), DefaultAgentTurnsPerThread, false},
	} {
		got := ResolveAgentTurns(tc.value)
		if got.Effective != tc.want || (got.Value != nil) != tc.set || got.Default != 5 || got.Ceiling != 20 {
			t.Errorf("%s: %+v, want effective %d (set %v), default 5, ceiling 20", tc.name, got, tc.want, tc.set)
		}
	}
}

func TestAgentTurnsPerThreadIsPerWorkspace(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if got := s.AgentTurnsPerThread(ctx, "ws-a"); got != 5 {
		t.Errorf("no value = %d, want 5", got)
	}
	if err := s.PutWorkspaceRaw(ctx, "ws-a", AgentTurnsPerThreadKey, "2"); err != nil {
		t.Fatal(err)
	}
	if got := s.AgentTurnsPerThread(ctx, "ws-a"); got != 2 {
		t.Errorf("own value = %d, want 2", got)
	}
	if got := s.AgentTurnsPerThread(ctx, "ws-b"); got != 5 {
		t.Errorf("another workspace = %d, want 5", got)
	}
	if err := s.PutWorkspaceRaw(ctx, "ws-c", AgentTurnsPerThreadKey, `"many"`); err != nil {
		t.Fatal(err)
	}
	if got := s.AgentTurnsPerThread(ctx, "ws-c"); got != 5 {
		t.Errorf("unreadable value = %d, want 5", got)
	}
}
