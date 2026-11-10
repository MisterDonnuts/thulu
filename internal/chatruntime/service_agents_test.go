package chatruntime

import (
	"errors"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

type stubSource struct {
	all []types.AgentType
	err error
}

func (s stubSource) All() ([]types.AgentType, error) { return s.all, s.err }
func (s stubSource) Invalidate()                     {}

func (s stubSource) For(string) types.AgentSource { return s }

func TestAgentTypeComesFromTheSourceNotTheDirectory(t *testing.T) {
	svc := NewService(nil, nil, nil, "/does/not/exist", nil).WithAgentSource(stubSource{
		all: []types.AgentType{
			{Name: "reviewer", Type: "reviewer", ValidStages: []types.Stage{types.StageAIReview}},
			{Name: "developer", Type: "developer", ValidStages: []types.Stage{types.StageChat, types.StageInProgress}},
		},
	})

	at, err := svc.AgentType("ws1", "developer")
	if err != nil {
		t.Fatalf("AgentType(developer): %v", err)
	}
	if at.Type != "developer" {
		t.Fatalf("got agent type %q, want developer", at.Type)
	}

	if _, err := svc.AgentType("ws1", "reviewer"); !errors.Is(err, ErrNotChatCapable) {
		t.Fatalf("reviewer declares no chat stage, got err %v", err)
	}
	if _, err := svc.AgentType("ws1", "nobody"); err == nil {
		t.Fatal("an unknown agent type should not resolve")
	}
}

func TestAgentTypeWithoutASourceStillReadsTheDirectory(t *testing.T) {
	svc := NewService(nil, nil, nil, "/does/not/exist", nil)
	if _, err := svc.AgentType("ws1", "developer"); err == nil {
		t.Fatal("with no source and no directory the lookup must fail")
	}
}
