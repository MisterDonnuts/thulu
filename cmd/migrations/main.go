// Command migrations reads every component's migration list (internal/schema)
// without a database, and keeps internal/schema/migrations.lock: the record of
// which migrations shipped in which release, so none of them ever changes.
//
//	go run ./cmd/migrations list [-json]
//	go run ./cmd/migrations lock                    refresh unreleased lines
//	go run ./cmd/migrations check [-base-stdin]     CI gate 1 (immutability)
//	go run ./cmd/migrations lint [-enforce]         CI gate 2 (expand/contract)
//	go run ./cmd/migrations release -version X      mark everything shipped in X
//	go run ./cmd/migrations ready -version X        `make release` precondition
//	go run ./cmd/migrations mode                    "enforcing" or "report"
//
// It lives apart from the octopus binary so CI runs it without building the
// dashboard the binary embeds. The Makefile targets are the interface; see
// docs/migrations.md. It always works on the checked-in lock
// (schema.LockPath, relative to the repository root it runs from); the base
// branch's lock, which check compares against, arrives on stdin.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/schema"
)

func main() {
	configureLocale()
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, i18n.Text("migrations.cli.error", "err", apperr.Text(err)))
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, i18n.Text("migrations.cli.usage"))
}

func configureLocale() {
	for _, env := range []string{"OCTOPUS_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); v != "" {
			i18n.SetDaemonLocale(i18n.Negotiate(v))
			return
		}
	}
}

func run(cmd string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	lockPath := schema.LockPath
	switch cmd {
	case "list":
		asJSON := fs.Bool("json", false, i18n.Text("migrations.cli.flag_json"))
		if err := fs.Parse(args); err != nil {
			return err
		}
		return list(out, *asJSON)
	case "lock":
		if err := fs.Parse(args); err != nil {
			return err
		}
		return relock(out, lockPath)
	case "check":
		baseStdin := fs.Bool("base-stdin", false, i18n.Text("migrations.cli.flag_base_stdin"))
		if err := fs.Parse(args); err != nil {
			return err
		}
		var base io.Reader
		if *baseStdin {
			base = os.Stdin
		}
		return check(out, lockPath, base)
	case "lint":
		enforce := fs.Bool("enforce", false, i18n.Text("migrations.cli.flag_enforce", "version", schema.EnforceFrom))
		if err := fs.Parse(args); err != nil {
			return err
		}
		return lint(out, lockPath, *enforce)
	case "release":
		version := fs.String("version", "", i18n.Text("migrations.cli.flag_release_version"))
		if err := fs.Parse(args); err != nil {
			return err
		}
		return release(out, lockPath, *version)
	case "ready":
		version := fs.String("version", "", i18n.Text("migrations.cli.flag_ready_version"))
		if err := fs.Parse(args); err != nil {
			return err
		}
		return ready(out, lockPath, *version)
	case "mode":
		if err := fs.Parse(args); err != nil {
			return err
		}
		return mode(out, lockPath)
	case "-h", "-help", "--help", "help":
		usage(out)
		return nil
	}
	usage(os.Stderr)
	return apperr.Invalid("migrations.errors.unknown_command", "command", cmd)
}

func list(out io.Writer, asJSON bool) error {
	all, err := schema.All()
	if err != nil {
		return err
	}
	if asJSON {
		type row struct {
			Component string `json:"component"`
			ID        int    `json:"id"`
			SHA256    string `json:"sha256"`
			Contract  any    `json:"contract,omitempty"`
		}
		rows := make([]row, 0, len(all))
		for _, e := range all {
			r := row{Component: e.Component, ID: e.ID, SHA256: e.SHA256}
			if e.Contract != nil {
				r.Contract = map[string]string{"stopped_using_in": e.Contract.StoppedUsingIn, "reason": e.Contract.Reason}
			}
			rows = append(rows, r)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	for _, e := range all {
		c := ""
		if e.Contract != nil {
			c = "  contract(" + e.Contract.StoppedUsingIn + ")"
		}
		fmt.Fprintf(out, "%-20s %3d %s%s\n", e.Component, e.ID, e.SHA256[:12], c)
	}
	return nil
}

func readLock(path string) (*schema.Lock, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- always schema.LockPath
	if err != nil {
		return nil, err
	}
	l, err := schema.ParseLock(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

func readLockOrEmpty(path string) (*schema.Lock, error) {
	l, err := readLock(path)
	if os.IsNotExist(err) {
		return &schema.Lock{}, nil
	}
	return l, err
}

func writeLock(l *schema.Lock) error {
	repo, err := os.OpenRoot(".")
	if err != nil {
		return err
	}
	defer repo.Close()
	return repo.WriteFile(schema.LockPath, l.Format(), 0o644) // #nosec G306 -- a checked-in source file
}

func mode(out io.Writer, path string) error {
	l, err := readLock(path)
	if err != nil {
		return err
	}
	// Protocol token compared by the Makefile's compat gate.
	if schema.Enforcing(l) {
		fmt.Fprintln(out, "enforcing") //i18n:ignore protocol token read by the Makefile
	} else {
		fmt.Fprintln(out, "report") //i18n:ignore protocol token read by the Makefile
	}
	return nil
}

func relock(out io.Writer, path string) error {
	l, err := readLockOrEmpty(path)
	if err != nil {
		return err
	}
	all, err := schema.All()
	if err != nil {
		return err
	}
	next, err := schema.Relock(l, all)
	if err != nil {
		return err
	}
	if err := writeLock(next); err != nil {
		return err
	}
	fmt.Fprintln(out, i18n.Text("migrations.cli.relocked", "path", path, "count", len(next.Entries), "unreleased", unreleased(next)))
	return nil
}

func unreleased(l *schema.Lock) int {
	n := 0
	for _, e := range l.Entries {
		if e.ReleasedIn == "" {
			n++
		}
	}
	return n
}

func check(out io.Writer, path string, baseLock io.Reader) error {
	l, err := readLock(path)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- always schema.LockPath
	if err != nil {
		return err
	}
	all, err := schema.All()
	if err != nil {
		return err
	}
	problems := schema.Check(l, all)
	if !bytes.Equal(raw, l.Format()) {
		problems = append(problems, i18n.Text("migrations.cli.lock_not_canonical", "path", path))
	}
	if baseLock != nil {
		base, perr := schema.ParseLock(io.LimitReader(baseLock, 4<<20))
		if perr != nil {
			return apperr.Wrap(perr, "migrations.errors.base_lock")
		}
		problems = append(problems, schema.CheckAgainstBase(l, base)...)
	}
	if perr := problems.Err(); perr != nil {
		return perr
	}
	fmt.Fprintln(out, i18n.Text("migrations.cli.check_ok", "count", len(l.Entries), "unreleased", unreleased(l),
		"latest", orNone(l.Latest())))
	return nil
}

func lint(out io.Writer, path string, enforce bool) error {
	l, err := readLock(path)
	if err != nil {
		return err
	}
	all, err := schema.All()
	if err != nil {
		return err
	}
	findings := schema.Lint(l, all)
	enforcing := enforce || schema.Enforcing(l)
	blocked := 0
	for _, f := range findings {
		if !f.Allowed {
			blocked++
		}
		fmt.Fprintln(out, f.String())
	}
	mode := i18n.Text("migrations.cli.mode_report", "version", schema.EnforceFrom)
	if enforcing {
		mode = i18n.Text("migrations.cli.mode_enforcing")
	}
	fmt.Fprintln(out, i18n.Text("migrations.cli.lint_summary", "count", len(findings), "blocked", blocked, "mode", mode))
	if !enforcing && blocked > 0 && os.Getenv("GITHUB_ACTIONS") == "true" {
		//i18n:ignore GitHub Actions annotation syntax, not a message
		fmt.Fprintf(out, "::warning title=migrations lint::%s\n",
			i18n.Text("migrations.cli.lint_annotation", "count", blocked, "version", schema.EnforceFrom))
	}
	if enforcing && blocked > 0 {
		return apperr.Precondition("migrations.errors.lint_blocked", "count", blocked)
	}
	return nil
}

func release(out io.Writer, path, version string) error {
	if version == "" {
		return apperr.Invalid("migrations.errors.version_required")
	}
	version = strings.TrimPrefix(version, "v")
	l, err := readLock(path)
	if err != nil {
		return err
	}
	all, err := schema.All()
	if err != nil {
		return err
	}
	if err = schema.Check(l, all).Err(); err != nil {
		return apperr.Wrap(err, "migrations.errors.lock_stale")
	}
	n := unreleased(l)
	next, err := schema.MarkReleased(l, version)
	if err != nil {
		return err
	}
	if err := writeLock(next); err != nil {
		return err
	}
	fmt.Fprintln(out, i18n.Text("migrations.cli.released", "path", path, "version", version, "count", n))
	return nil
}

func ready(out io.Writer, path, version string) error {
	if version == "" {
		return apperr.Invalid("migrations.errors.version_required")
	}
	l, err := readLock(path)
	if err != nil {
		return err
	}
	all, err := schema.All()
	if err != nil {
		return err
	}
	problems := append(schema.Check(l, all), schema.CheckReady(l, strings.TrimPrefix(version, "v"))...)
	if err := problems.Err(); err != nil {
		return err
	}
	fmt.Fprintln(out, i18n.Text("migrations.cli.ready", "version", version))
	return nil
}

func orNone(s string) string {
	if s == "" {
		return i18n.Text("migrations.cli.none")
	}
	return s
}
