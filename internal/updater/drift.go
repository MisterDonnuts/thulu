package updater

import (
	"context"
	"fmt"

	"github.com/MisterDonnuts/thulu/internal/clicopy"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/initwizard"
)

// Versions is what each copy of Thulu on this machine runs: the CLI answering, the other CLIs on
// the PATH, the runner service's binary and the stack's daemon.
type Versions struct {
	Self    string
	Copies  []clicopy.Copy
	Service *clicopy.Copy
	// Empty when there is no install here or its daemon does not answer.
	Stack    string
	StackURL string
}

// ReadVersions asks every copy for its version. root is the install, or "" for none.
func ReadVersions(ctx context.Context, self, root string, ops *CLIOps) Versions {
	if ops == nil {
		ops = defaultCLIOps()
	}
	v := Versions{Self: self}
	var selfFile string
	if exe, err := ops.Executable(); err == nil {
		selfFile = clicopy.Resolve(exe)
	}
	u := &updateRun{opts: Options{Delegated: true}, deps: &Deps{CLI: ops}, in: &Install{Root: root}}
	for _, c := range u.cliCandidates(ops) {
		if c.Resolved == selfFile {
			continue
		}
		c.Version = ops.Version(ctx, c.Resolved)
		v.Copies = append(v.Copies, c)
	}
	if bin, ok := ops.ServiceBinary(ctx); ok {
		r := clicopy.Resolve(bin)
		c := clicopy.Copy{Path: bin, Resolved: r, Version: self}
		if r != selfFile {
			c.Version = ops.Version(ctx, r)
		}
		v.Service = &c
	}
	if root != "" {
		if answers, err := initwizard.LoadAnswers(root); err == nil {
			v.StackURL = daemonURL(answers)
			v.Stack = runningVersion(ctx, v.StackURL, initwizard.InstallDevToken(root), "")
		}
	}
	return v
}

// sayDrift warns, after `update -check`, about copies of Thulu on another version.
func (u *updateRun) sayDrift(ctx context.Context) {
	if !u.opts.SyncCLI {
		return
	}
	for _, line := range ReadVersions(ctx, u.selfVersion(), u.in.Root, u.deps.CLI).Drift() {
		fmt.Fprintln(u.out, line)
	}
}

// Drift lists, one line each, the copies that run another version than this CLI.
func (v Versions) Drift() []string {
	var out []string
	for _, c := range v.Copies {
		if c.Version != v.Self {
			out = append(out, i18n.Text("updater.drift.cli", "path", c.Path, "version", orUnknown(c.Version), "self", v.Self))
		}
	}
	if v.Service != nil && v.Service.Version != v.Self {
		out = append(out, i18n.Text("updater.drift.runner", "path", v.Service.Path, "version", orUnknown(v.Service.Version),
			"self", v.Self))
	}
	if v.Stack != "" && v.Stack != v.Self {
		out = append(out, i18n.Text("updater.drift.stack", "url", v.StackURL, "version", v.Stack, "self", v.Self))
	}
	if len(out) > 0 {
		out = append(out, i18n.Text("updater.drift.fix"))
	}
	return out
}
