package chatruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type promptCall struct{ ws, agent, stage string }

func TestChatWithoutAPromptIsRefusedBeforeThePod(t *testing.T) {
	r := newRig(t, 5)
	var calls []promptCall
	svc := NewService(r.chats, r.mgr, chat.NewBus(), "", nil).
		WithAgentSource(stubSource{all: []types.AgentType{*testAgent()}}).
		WithPromptCheck(func(_ context.Context, ws, agent, stage string) bool {
			calls = append(calls, promptCall{ws, agent, stage})
			return false
		})
	c := testChat("chat-noprompt")
	if err := r.chats.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	_, err := svc.PostMessage(t.Context(), c.ID, "ana", "hola")
	if apperr.KindOf(err) != apperr.KindPrecondition {
		t.Fatalf("PostMessage err = %v, want a Precondition error", err)
	}
	for _, want := range []string{"manager", "prompts/chat.md", "prompts/main.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err, want)
		}
	}
	if len(calls) != 1 || calls[0] != (promptCall{"ws-1", "manager", string(types.StageChat)}) {
		t.Errorf("prompt check calls = %+v, want one for ws-1/manager/chat", calls)
	}
	if n := r.launchCount(); n != 0 {
		t.Errorf("launched %d containers for an agent with no prompt", n)
	}
	msgs, err := r.chats.ListMessages(t.Context(), c.ID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("stored %d messages for a refused turn", len(msgs))
	}
	if svc.Runtime().TurnInFlight(c.ID) {
		t.Error("the refused turn kept the chat's turn slot")
	}

	if _, err := svc.RunTurnSync(t.Context(), c, "hola", nil); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("RunTurnSync err = %v, want a Precondition error", err)
	}
	if err := svc.PromptReady(t.Context(), c); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("PromptReady err = %v, want a Precondition error", err)
	}
}

func TestChatPromptReadyPassesWhenThePromptExists(t *testing.T) {
	src := stubSource{all: []types.AgentType{*testAgent()}}
	c := testChat("chat-prompt")

	withPrompt := NewService(nil, nil, nil, "", nil).WithAgentSource(src).
		WithPromptCheck(func(context.Context, string, string, string) bool { return true })
	if err := withPrompt.PromptReady(t.Context(), c); err != nil {
		t.Errorf("agent with a prompt: %v", err)
	}
	if err := NewService(nil, nil, nil, "", nil).WithAgentSource(src).PromptReady(t.Context(), c); err != nil {
		t.Errorf("no check wired: %v", err)
	}
	unknown := &chat.Chat{ID: "chat-x", WorkspaceID: "ws-1", AgentType: "ghost"}
	if err := withPrompt.PromptReady(t.Context(), unknown); err != nil {
		t.Errorf("unresolvable agent: %v", err)
	}
}
