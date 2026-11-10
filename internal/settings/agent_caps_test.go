package settings

import "testing"

func TestAgentResourceCapsDefaults(t *testing.T) {
	s := newTestStore(t)
	caps := s.AgentResourceCaps(t.Context())
	if caps.MemoryBytes != DefaultAgentMemoryBytes {
		t.Errorf("MemoryBytes = %d, want %d", caps.MemoryBytes, DefaultAgentMemoryBytes)
	}
	if caps.PidsLimit != 1024 {
		t.Errorf("PidsLimit = %d, want 1024", caps.PidsLimit)
	}
	if caps.NanoCPUs != 0 {
		t.Errorf("NanoCPUs = %d, want 0 (unlimited)", caps.NanoCPUs)
	}
}

func TestAgentMaxContainersDefaultUnlimited(t *testing.T) {
	s := newTestStore(t)
	n, err := s.AgentMaxContainers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("AgentMaxContainers default = %d, want 0 (unlimited)", n)
	}
}

func TestAgentCapsOverridable(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutJSON(t.Context(), AgentMemoryBytesKey, int64(768)<<20); err != nil {
		t.Fatal(err)
	}
	if err := s.PutJSON(t.Context(), AgentMaxContainersKey, 8); err != nil {
		t.Fatal(err)
	}
	if got := s.AgentResourceCaps(t.Context()).MemoryBytes; got != int64(768)<<20 {
		t.Errorf("overridden MemoryBytes = %d, want %d", got, int64(768)<<20)
	}
	n, _ := s.AgentMaxContainers(t.Context())
	if n != 8 {
		t.Errorf("overridden AgentMaxContainers = %d, want 8", n)
	}
}
