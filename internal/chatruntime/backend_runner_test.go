package chatruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// fakeMachine plays the runner launcher: it records each turn and answers with stdout.
type fakeMachine struct {
	mu       sync.Mutex
	turns    []MachineTurn
	stdout   string
	checkErr error
}

func (f *fakeMachine) CheckRunner(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkErr
}

func (f *fakeMachine) RunTurn(_ context.Context, turn MachineTurn) (ExecOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turns = append(f.turns, turn)
	return ExecOutput{Stdout: []byte(f.stdout)}, nil
}

func (f *fakeMachine) turnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.turns)
}

func (f *fakeMachine) setCheckErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkErr = err
}

// placedOn is a chat auth that puts every chat on *machine ("" keeps it in a container), the way the
// dispatcher's placement writes OCTOPUS_RUNNER_ID.
func placedOn(machine *string, refusal *error) LLMAuthFunc {
	return func(env map[string]string, _, _ string, _ *types.AgentType) error {
		if *refusal != nil {
			return *refusal
		}
		if *machine != "" {
			env[runner.RunnerIDEnv] = *machine
		}
		return nil
	}
}

type machineRig struct {
	*rig
	machine *fakeMachine
	placeOn string
	refusal error
}

func newMachineRig(t *testing.T, maxContainers int) *machineRig {
	t.Helper()
	r := &machineRig{rig: newRig(t, maxContainers), machine: &fakeMachine{stdout: okStream}, placeOn: "r1"}
	r.mgr.SetMachineTurns(r.machine)
	r.mgr.SetLLMAuth(placedOn(&r.placeOn, &r.refusal))
	return r
}

func (r *machineRig) turn(t *testing.T, chatID, text string) (*TurnResult, error) {
	t.Helper()
	return r.mgr.ExecTurn(t.Context(), testChat(chatID), testAgent(), AcquireOpts{}, text)
}

// Case 39: a chat placed on a machine runs its turn there with the message as stdin and gets the
// parsed answer back, without starting a container.
func TestMachineChatRunsItsTurnOnTheMachine(t *testing.T) {
	r := newMachineRig(t, 5)
	c := testChat("chat-m1")
	c.SessionID = "sess-0"
	res, err := r.mgr.ExecTurn(t.Context(), c, testAgent(), AcquireOpts{}, "hola")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "hola!" || res.SessionID != "sess-1" {
		t.Errorf("result = %+v, want the parsed answer", res)
	}
	if r.launchCount() != 0 {
		t.Errorf("launched %d containers for a machine chat", r.launchCount())
	}
	turn := r.machine.turns[0]
	if turn.RunnerID != "r1" || turn.ChatID != c.ID || turn.Stdin != "hola" || turn.WorkspaceID != c.WorkspaceID {
		t.Errorf("turn = %+v", turn)
	}
	for k, want := range map[string]string{
		"SESSION_ID": "sess-0", "CHAT_ID": c.ID, "STAGE": "chat", runner.HostRunEnv: "1", runner.RunnerIDEnv: "r1",
	} {
		if turn.Env[k] != want {
			t.Errorf("turn env %s = %q, want %q", k, turn.Env[k], want)
		}
	}
	rt, err := r.store.Get(c.ID)
	if err != nil || machineOf(rt.ContainerID) != "r1" {
		t.Fatalf("runtime = %+v, %v; want one on r1", rt, err)
	}
	if _, err := r.mgr.ExecTurn(t.Context(), c, testAgent(), AcquireOpts{}, "otra"); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.store.Get(c.ID); again.ContainerID != rt.ContainerID {
		t.Errorf("the second turn made a new runtime %q, want %q kept", again.ContainerID, rt.ContainerID)
	}
}

// Case 42: a revoked machine fails the turn with its own error, once, and nothing is queued.
func TestMachineChatOnARevokedMachineFailsClearly(t *testing.T) {
	revoked := apperr.Precondition("task.errors.runner_revoked", "runner", "mac")
	t.Run("at placement", func(t *testing.T) {
		r := newMachineRig(t, 5)
		r.refusal = revoked
		if _, err := r.turn(t, "chat-rv1", "hola"); !errors.Is(err, revoked) {
			t.Fatalf("err = %v, want the revocation", err)
		}
		if r.machine.turnCount() != 0 {
			t.Errorf("queued %d turns on a revoked machine", r.machine.turnCount())
		}
	})
	t.Run("after the chat was open", func(t *testing.T) {
		r := newMachineRig(t, 5)
		if _, err := r.turn(t, "chat-rv2", "hola"); err != nil {
			t.Fatal(err)
		}
		gone := apperr.Precondition("api.runners.errors.runner_revoked", "runner", "mac")
		r.machine.setCheckErr(gone)
		if _, err := r.turn(t, "chat-rv2", "otra"); !errors.Is(err, gone) {
			t.Fatalf("err = %v, want the revocation", err)
		}
		if r.machine.turnCount() != 1 {
			t.Errorf("turns = %d, want only the first", r.machine.turnCount())
		}
	})
}

// After a daemon restart the env is gone: the next turn creates the chat again and goes on.
func TestMachineChatRecreatesAfterLosingItsEnv(t *testing.T) {
	r := newMachineRig(t, 5)
	if _, err := r.turn(t, "chat-rs", "hola"); err != nil {
		t.Fatal(err)
	}
	r.mgr.SetMachineTurns(r.machine)
	if _, err := r.turn(t, "chat-rs", "otra"); err != nil {
		t.Fatal(err)
	}
	if r.machine.turnCount() != 2 {
		t.Errorf("turns = %d, want 2", r.machine.turnCount())
	}
}

// Case 43: Reconcile asks Docker nothing about a machine chat and leaves it live.
func TestReconcileLeavesMachineChatsAlone(t *testing.T) {
	r := newMachineRig(t, 5)
	if _, err := r.turn(t, "chat-rc", "hola"); err != nil {
		t.Fatal(err)
	}
	r.mgr.Reconcile(t.Context())
	rt, err := r.store.Get("chat-rc")
	if err != nil || rt.Status != StatusReady {
		t.Fatalf("runtime = %+v, %v; want it still ready", rt, err)
	}
	if len(r.docker.removed) != 0 {
		t.Errorf("Docker removed %v", r.docker.removed)
	}
}

// Case 44: machine chats count against chat.max and are evicted like the rest.
func TestMachineChatsCountAndGetEvicted(t *testing.T) {
	r := newMachineRig(t, 1)
	if _, err := r.turn(t, "chat-c1", "hola"); err != nil {
		t.Fatal(err)
	}
	r.placeOn = ""
	if _, err := r.turn(t, "chat-c2", "hola"); err != nil {
		t.Fatal(err)
	}
	if rt, _ := r.store.Get("chat-c1"); rt.Status != StatusEvicted {
		t.Errorf("machine chat status = %s, want evicted to make room", rt.Status)
	}
	if n, _ := r.store.CountLive(); n != 1 {
		t.Errorf("live = %d, want 1", n)
	}
	if _, held := r.mgr.machine.(*runnerBackend).held(machineChatID("r1", "chat-c1")); held {
		t.Error("the evicted machine chat still holds its env")
	}
}

// Case 58: moving the profile to or from a machine recreates the chat where it runs now.
func TestChatFollowsItsPlacement(t *testing.T) {
	r := newMachineRig(t, 5)
	r.placeOn = ""
	if _, err := r.turn(t, "chat-p", "hola"); err != nil {
		t.Fatal(err)
	}
	r.placeOn = "r1"
	if _, err := r.turn(t, "chat-p", "otra"); err != nil {
		t.Fatal(err)
	}
	if r.machine.turnCount() != 1 || len(r.docker.removed) != 1 {
		t.Fatalf("machine turns = %d, removed = %v; want the container replaced by the machine", r.machine.turnCount(), r.docker.removed)
	}
	r.placeOn = "r2"
	if _, err := r.turn(t, "chat-p", "y otra"); err != nil {
		t.Fatal(err)
	}
	if got := r.machine.turns[1].RunnerID; got != "r2" {
		t.Errorf("third turn ran on %q, want r2", got)
	}
	r.placeOn = ""
	if _, err := r.turn(t, "chat-p", "de vuelta"); err != nil {
		t.Fatal(err)
	}
	if r.launchCount() != 2 || r.machine.turnCount() != 2 {
		t.Errorf("launches = %d, machine turns = %d; want back in a new container", r.launchCount(), r.machine.turnCount())
	}
}

func TestMachineChatID(t *testing.T) {
	cases := []struct{ id, want string }{
		{machineChatID("ab12", "chat-9f"), "ab12"},
		{"ctr-1", ""},
		{runner.RunIDForJob("ab12"), ""},
	}
	for _, c := range cases {
		if got := machineOf(c.id); got != c.want {
			t.Errorf("machineOf(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}
