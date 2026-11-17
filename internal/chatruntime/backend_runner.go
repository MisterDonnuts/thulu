package chatruntime

import (
	"context"
	"maps"
	"strings"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/runner"
)

// MachineTurn is one chat turn for the machine the chat was placed on.
type MachineTurn struct {
	WorkspaceID string
	RunnerID    string
	ChatID      string
	// A task chat reads its task's code; the launcher lays it out read-only on the machine.
	TaskID    string
	AgentName string
	AgentType string
	Env       map[string]string
	Stdin     string
}

// MachineTurns runs chat turns on registered machines. The daemon's runner launcher implements it,
// so chats share its queue, claim, lease and revoke with every other job of the machine.
type MachineTurns interface {
	// CheckRunner refuses a machine outside the workspace or revoked.
	CheckRunner(ctx context.Context, workspaceID, runnerID string) error
	// RunTurn queues the turn and waits, within ctx, for what the machine's engine printed.
	RunTurn(ctx context.Context, turn MachineTurn) (ExecOutput, error)
}

// runnerBackend keeps no process: a machine runs one job per turn. The "container" is the chat's
// env, held in memory, which a daemon restart loses; Running then answers false and the Manager
// creates it again, as with Kubernetes' exec tokens.
type runnerBackend struct {
	turns MachineTurns

	mu    sync.Mutex
	chats map[string]machineChat
}

type machineChat struct {
	workspaceID string
	runnerID    string
	chatID      string
	taskID      string
	agentName   string
	agentType   string
	env         map[string]string
}

func newRunnerBackend(turns MachineTurns) *runnerBackend {
	return &runnerBackend{turns: turns, chats: map[string]machineChat{}}
}

// The id names the machine, so the Manager routes a chat by its id alone and sees, without a
// migration, when the chat's placement no longer matches it.
const machineChatPrefix = runner.ContainerPrefix + "chat-"

func machineChatID(runnerID, chatID string) string {
	return machineChatPrefix + runnerID + "-" + chatID
}

// machineOf returns the machine a runtime id points at; "" for a container or a pod. Runner ids are
// hex, so the first dash after the prefix ends it.
func machineOf(id string) string {
	rest, ok := strings.CutPrefix(id, machineChatPrefix)
	if !ok {
		return ""
	}
	runnerID, _, _ := strings.Cut(rest, "-")
	return runnerID
}

func (b *runnerBackend) Create(ctx context.Context, spec CreateSpec) (string, error) {
	runnerID := spec.Env[runner.RunnerIDEnv]
	if err := b.turns.CheckRunner(ctx, spec.Chat.WorkspaceID, runnerID); err != nil {
		return "", err
	}
	id := machineChatID(runnerID, spec.Chat.ID)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chats[id] = machineChat{
		workspaceID: spec.Chat.WorkspaceID, runnerID: runnerID, chatID: spec.Chat.ID, taskID: spec.Chat.TaskID,
		agentName: spec.AgentType.Name, agentType: spec.AgentType.Type, env: maps.Clone(spec.Env),
	}
	return id, nil
}

func (b *runnerBackend) held(id string) (machineChat, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.chats[id]
	return c, ok
}

func (b *runnerBackend) Exec(ctx context.Context, spec ExecSpec) (ExecOutput, error) {
	c, ok := b.held(spec.ContainerID)
	if !ok {
		return ExecOutput{}, errChatPodNeedsRecreate
	}
	env := maps.Clone(c.env)
	for _, kv := range spec.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return b.turns.RunTurn(ctx, MachineTurn{
		WorkspaceID: c.workspaceID, RunnerID: c.runnerID, ChatID: c.chatID, TaskID: c.taskID,
		AgentName: c.agentName, AgentType: c.agentType, Env: env, Stdin: spec.Stdin,
	})
}

func (b *runnerBackend) Running(ctx context.Context, id string) bool {
	c, ok := b.held(id)
	return ok && b.turns.CheckRunner(ctx, c.workspaceID, c.runnerID) == nil
}

func (b *runnerBackend) Remove(_ context.Context, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.chats, id)
}

// The daemon sees nothing a machine keeps; the machine sweeps its own idle chat folders.
func (b *runnerBackend) ListManaged(context.Context) ([]string, error) { return nil, nil }

func (b *runnerBackend) Available() bool { return true }
