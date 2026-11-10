package auth

import (
	"os"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/edition/editiontest"
)

// Most of this package is the multi-workspace install (SaaS, invitations, caps). The Community
// limit has its own test, which drops the license.
func TestMain(m *testing.M) {
	editiontest.EnableAll()
	os.Exit(m.Run())
}
