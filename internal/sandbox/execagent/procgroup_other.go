//go:build !unix

package execagent

import "os/exec"

// Deliberate no-op: the exec agent only runs in Linux sandboxes; this keeps the client building
// elsewhere.
func ownProcessGroup(*exec.Cmd) {}
