package chatruntime

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/types"
)

var errBackendUnavailable error = apperr.Unavailable("chatruntime.errors.backend_unavailable")

var errChatPodNeedsRecreate error = apperr.New("chatruntime.errors.pod_needs_recreate")

type unavailableBackend struct{}

func (unavailableBackend) Create(context.Context, CreateSpec) (string, error) {
	return "", errBackendUnavailable
}
func (unavailableBackend) Exec(context.Context, ExecSpec) (ExecOutput, error) {
	return ExecOutput{}, errBackendUnavailable
}
func (unavailableBackend) Running(context.Context, string) bool          { return false }
func (unavailableBackend) Remove(context.Context, string)                {}
func (unavailableBackend) ListManaged(context.Context) ([]string, error) { return nil, nil }
func (unavailableBackend) Available() bool                               { return false }

type backend interface {
	Create(ctx context.Context, spec CreateSpec) (id string, err error)

	Exec(ctx context.Context, spec ExecSpec) (ExecOutput, error)

	Running(ctx context.Context, id string) bool

	// Must be idempotent; a missing target is not an error.
	Remove(ctx context.Context, id string)

	ListManaged(ctx context.Context) ([]string, error)

	Available() bool
}

type CreateSpec struct {
	Chat              *chat.Chat
	AgentType         *types.AgentType
	Stage             string
	Env               map[string]string
	Code              map[string]types.CodeView
	WorkspaceReadOnly bool
	Resources         types.ResourceCaps
}

type ExecSpec struct {
	ContainerID string
	Env         []string
	Stdin       string
}

type ExecOutput struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}
