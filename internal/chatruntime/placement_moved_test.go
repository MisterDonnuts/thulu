package chatruntime

import (
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// placedInTurn answers each auth call with the next placement; "" is a container on the
// workspace's key, so a turn resolved there carries it.
func placedInTurn(placements ...string) LLMAuthFunc {
	var mu sync.Mutex
	return func(env map[string]string, _, _ string, _ *types.AgentType) error {
		mu.Lock()
		defer mu.Unlock()
		next := placements[0]
		if len(placements) > 1 {
			placements = placements[1:]
		}
		if next == "" {
			env["ANTHROPIC_API_KEY"] = "sk-workspace"
			env[engine.LLMSourceEnv] = "binding"
			return nil
		}
		env[runner.RunnerIDEnv] = next
		return nil
	}
}

// A profile moved to a machine between the turn's auth and the create's: the turn is placed again
// before it runs, so the key it was resolved with for a container never reaches the machine.
func TestTurnResolvedForAContainerNeverRunsOnAMachine(t *testing.T) {
	r := newMachineRig(t, 5)
	r.mgr.SetLLMAuth(placedInTurn("", "r1"))
	res, err := r.turn(t, "chat-mv", "hola")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hola!" {
		t.Errorf("result = %+v, want the machine's answer", res)
	}
	if r.machine.turnCount() != 1 || r.launchCount() != 0 {
		t.Fatalf("machine turns = %d, containers = %d; want the turn on the machine", r.machine.turnCount(), r.launchCount())
	}
	if got := r.machine.turns[0].Env["ANTHROPIC_API_KEY"]; got != "" {
		t.Errorf("the machine turn carries ANTHROPIC_API_KEY=%q", got)
	}
}
