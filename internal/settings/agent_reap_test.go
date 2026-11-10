package settings

import "testing"

func TestAgentReapOnExitDefaultsTrue(t *testing.T) {
	s := newTestStore(t)
	on, err := s.AgentReapOnExit(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Error("AgentReapOnExit default = false, want true")
	}
}

func TestAgentReapOnExitOverride(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutJSON(t.Context(), AgentReapOnExitKey, false); err != nil {
		t.Fatal(err)
	}
	on, err := s.AgentReapOnExit(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Error("AgentReapOnExit = true after override to false")
	}
}
