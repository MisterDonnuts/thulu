package task

import (
	"os"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

// The locked-tests and worktree tests run git on the host.
func TestMain(m *testing.M) {
	if err := testgit.IsolateHostConfig(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
