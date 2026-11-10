package api

import (
	"os"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/edition/editiontest"
)

func TestMain(m *testing.M) {
	// RequireRole fails closed without a JWT secret, so the suite opts into the dev-mode bypass;
	// role gates are tested in the auth package.
	os.Setenv("OCTOPUS_DEV_MODE", "1")
	os.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	// The SaaS, billing and multi-workspace paths run here; the Community gates are tested in auth
	// and audit.
	editiontest.EnableAll()
	os.Exit(m.Run())
}
