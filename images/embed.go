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

// ThuluGates is the quality gates CLI the agent images install at /opt/thulu-gates (tests and
// README left out). A runner writes it into each job and puts it first on the job's PATH, so a
// machine runs the gates of the runner binary it has, never a copy installed by hand that no update
// reaches.
//
//go:embed runtime/thulu-gates/package.json runtime/thulu-gates/bin runtime/thulu-gates/lib runtime/thulu-gates/locales
var ThuluGates embed.FS

// ThuluGatesRoot is the folder ThuluGates keeps the CLI under.
const ThuluGatesRoot = "runtime/thulu-gates"
