package hostrun

import (
	"os"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func TestMain(m *testing.M) {
	if err := testgit.IsolateHostConfig(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
