//go:build !linux

package hostrun

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Only Linux has systemd scopes: everywhere else the run's watcher holds the cap.
type scopeSupport struct {
	ok     bool
	reason error
}

func probeScopes() scopeSupport {
	return scopeSupport{reason: apperr.Precondition("hostrun.errors.scope_not_linux")}
}

type runScope struct{}

func newRunScope(string) *runScope { return &runScope{} }

func (*runScope) command(bash, entry string, _ int64) (string, []string) {
	return bash, []string{entry}
}

func (*runScope) started(int) {}

func (*runScope) oomKills() int64 { return 0 }

func (*runScope) killedForMemory(context.Context) bool { return false }

func (*runScope) currentKB() int64 { return 0 }

func (*runScope) kill(context.Context) {}
