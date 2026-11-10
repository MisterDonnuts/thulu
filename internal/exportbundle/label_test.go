package exportbundle

import "testing"

func TestStoresWorkspaceLabel(t *testing.T) {
	var s Stores
	if got := s.workspaceLabel("bbbb000000000008"); got != "bbbb000000000008" {
		t.Errorf("no namer: %q, want the id", got)
	}
	s.WorkspaceLabel = func(id string) string { return "acme" }
	if got := s.workspaceLabel("bbbb000000000008"); got != "acme" {
		t.Errorf("namer: %q, want its answer", got)
	}
}
