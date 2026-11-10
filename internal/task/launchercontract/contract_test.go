package launchercontract

import "testing"

func TestContract_FakeLauncher(t *testing.T) {
	f, drv := NewFake()
	RunSuite(t, f, drv)
}
