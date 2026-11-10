package sandbox

import (
	"context"
	"time"
)

type Runtime interface {
	Image() string
	Create(ctx context.Context, opts CreateOpts) (*CreateResult, error)
	Exec(ctx context.Context, containerID string, opts ExecOpts) (*ExecResult, error)
	Alive(ctx context.Context, containerID string) bool
	Remove(ctx context.Context, containerID string) error
}

// The Kubernetes worktree claim is also mounted by the task's agent pods, so it survives Remove;
// only Release drops it, never an idle eviction.
type StorageReleaser interface {
	ReleaseStorage(ctx context.Context, taskID string) error
}

type TerminatedSweeper interface {
	SweepTerminated(ctx context.Context, olderThan time.Duration) (int, error)
}

var (
	_ Runtime       = (*Docker)(nil)
	_ PeerAddresser = (*Docker)(nil)
)

const EnvSandboxImage = "OCTOPUS_SANDBOX_IMAGE"
