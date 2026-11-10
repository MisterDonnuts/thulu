// Package images carries the agent runtime scripts as data inside the binary:
// the entrypoints and the snippets an agent image bakes under /workspace.
//
// A runner (internal/runner/hostrun) runs agents on a machine without an
// image, so it lays these files out per run the way the image does and starts
// the same entrypoint. Shipping them in the binary keeps the runner and the
// daemon on the same scripts: one `octopus` build is both.
package images

import "embed"

// Runtime is the agent runtime tree: entrypoint-base.sh,
// kube-agent-entrypoint.sh and snippets/ (tests excluded, see RuntimeFiles).
//
//go:embed entrypoint-base.sh kube-agent-entrypoint.sh snippets
var Runtime embed.FS
