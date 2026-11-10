package webhook

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS", "127.0.0.0/8,::1")
	os.Exit(m.Run())
}
