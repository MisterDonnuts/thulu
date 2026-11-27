package hostrun

import (
	"os"
	"syscall"
)

// StopSignals end `thulu runner run` the clean way: the runs are killed, then reported. SIGHUP comes
// when the terminal the runner runs in closes; left to Go's default it would end the runner on the
// spot, with every run still running.
var StopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
