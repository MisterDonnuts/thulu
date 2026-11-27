package task

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// An auditor in signals mode runs only when the finished run gives a reason to look. The checks
// read data the dispatcher already has at the run's end, plus one diff of the task branch on the
// daemon host. A run with no signal goes on as if the auditor had said continue, and its
// auditor_skipped event lists every check done so a person can see why.

// The checks, in the order the event lists them.
const (
	AuditCheckExit           = "exit"
	AuditCheckOutOfTurns     = "out_of_turns"
	AuditCheckVerdict        = "verdict"
	AuditCheckLockedTests    = "locked_tests"
	AuditCheckScope          = "scope"
	AuditCheckDiffSize       = "diff_size"
	AuditCheckSkips          = "skips"
	AuditCheckFlags          = "flags"
	AuditCheckNoOutput       = "no_output"
	AuditCheckPersonRelaunch = "person_relaunch"
	AuditCheckRiskHigh       = "risk_high"
)

// The states of one check.
const (
	AuditClear  = "clear"
	AuditSignal = "signal"
	// The daemon could not read what the check needs (a project whose code lives on a runner, a
	// worktree git could not read). Not a signal: the other checks still decide.
	AuditUnread = "unread"
)

// AuditCheck is one check of a finished run. Detail holds data (counts, paths, stage ids) or a fixed
// code, never prose, so the event reads the same in every language.
type AuditCheck struct {
	Check  string `json:"check"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// What the dispatcher knows about a finished run when it decides on the auditor.
type auditFacts struct {
	Exit       int
	OutOfTurns bool
	// The stage takes a verdict.md from its agent (agent_verdict); Verdict is what it said, "" when
	// there was none or it did not parse.
	ExpectsVerdict bool
	Verdict        string
	// A locked test of another stage changed, or could not be compared.
	LockedTouched   bool
	LockedUnchecked bool
	// nil when the task has no project, so there is no diff to read.
	Diff *diffFacts
	Risk string
	// The columns the task skips that the run's own agent set. The refinement auditor checks every
	// skip against the spec, so a run that set one is audited whatever the risk.
	AgentSkips []Stage
	// Flags the run's flags.yaml raised.
	Flags int
	// Reports the run left (its _index.md, verdict and the like).
	Artifacts int
	// The run is the first one after a person relaunched the stage with a reason.
	PersonRelaunch bool
}

type diffFacts struct {
	// Why a project's diff could not be read; "" when every one was.
	Unread string
	Files  []diffFile
	// Paths whose diff adds or removes a suppression (project.SuppressionPattern).
	Suppressions []string
	MaxLines     int
	MaxFiles     int
}

type diffFile struct {
	Path    string
	Added   int
	Deleted int
}

// Detail codes of the locked_tests check.
const (
	lockedChanged   = "changed"
	lockedUnchecked = "unchecked"
)

// How many paths a detail names before it gives up and counts.
const auditDetailPaths = 5

// auditSignals runs every check that applies to the run.
func auditSignals(f auditFacts) []AuditCheck {
	var out []AuditCheck
	add := func(check string, signal bool, detail string) {
		state := AuditClear
		if signal {
			state = AuditSignal
		} else {
			detail = ""
		}
		out = append(out, AuditCheck{Check: check, State: state, Detail: detail})
	}
	add(AuditCheckExit, f.Exit != 0, strconv.Itoa(f.Exit))
	add(AuditCheckOutOfTurns, f.OutOfTurns, "")
	if f.ExpectsVerdict {
		v := f.Verdict
		add(AuditCheckVerdict, v != verdictContinue, v)
	}
	switch {
	case f.LockedTouched:
		add(AuditCheckLockedTests, true, lockedChanged)
	case f.LockedUnchecked:
		add(AuditCheckLockedTests, true, lockedUnchecked)
	default:
		add(AuditCheckLockedTests, false, "")
	}
	if f.Diff != nil {
		out = append(out, diffChecks(*f.Diff)...)
	}
	risky := f.Risk == "high"
	add(AuditCheckSkips, len(f.AgentSkips) > 0, stagesText(f.AgentSkips))
	add(AuditCheckFlags, f.Flags > 0, strconv.Itoa(f.Flags))
	add(AuditCheckNoOutput, f.Artifacts == 0, "")
	add(AuditCheckPersonRelaunch, f.PersonRelaunch, "")
	add(AuditCheckRiskHigh, risky, "")
	return out
}

// The scope and size checks of the branch diff. A diff that could not be read leaves both unread.
// The size counts production lines added and the files that gain lines: deletions the spec asks for
// (a page or a flow it removes) are not size, and on one real task 506 of 790 changed lines were such
// deletions.
func diffChecks(d diffFacts) []AuditCheck {
	if d.Unread != "" {
		return []AuditCheck{{Check: AuditCheckScope, State: AuditUnread}, {Check: AuditCheckDiffSize, State: AuditUnread}}
	}
	var gates []string
	lines, files := 0, 0
	for _, f := range d.Files {
		if IsGateConfigPath(f.Path) {
			gates = append(gates, f.Path)
		}
		if IsTestPath(f.Path) || f.Added == 0 {
			continue
		}
		files++
		lines += f.Added
	}
	touched := slices.Concat(gates, d.Suppressions)
	scope := AuditCheck{Check: AuditCheckScope, State: AuditClear}
	if len(touched) > 0 {
		scope.State, scope.Detail = AuditSignal, pathsText(dedupe(touched))
	}
	size := AuditCheck{Check: AuditCheckDiffSize, State: AuditClear}
	if lines > d.MaxLines || files > d.MaxFiles {
		size.State = AuditSignal
		size.Detail = strconv.Itoa(lines) + "/" + strconv.Itoa(d.MaxLines) + " " +
			strconv.Itoa(files) + "/" + strconv.Itoa(d.MaxFiles)
	}
	return []AuditCheck{scope, size}
}

// auditSignalNames is the checks that fired, in order.
func auditSignalNames(checks []AuditCheck) []string {
	var out []string
	for _, c := range checks {
		if c.State == AuditSignal {
			out = append(out, c.Check)
		}
	}
	return out
}

func auditCheckNames(checks []AuditCheck) string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Check
	}
	return strings.Join(names, ", ")
}

// Base names of the files that configure the gates a run must pass: linters, formatters, type
// checkers, test runners, coverage and scanners. A run that edits one can make a gate pass by
// changing the rule instead of the code.
var gateConfigNames = map[string]bool{
	".prettierignore": true, ".eslintignore": true, ".stylelintignore": true, ".editorconfig": true,
	".semgrepignore": true, ".gitleaks.toml": true, ".gitleaksignore": true, ".trivyignore": true,
	"sonar-project.properties": true, ".coveragerc": true, "codecov.yml": true, ".codecov.yml": true,
	"ruff.toml": true, ".ruff.toml": true, ".flake8": true, "mypy.ini": true, ".pylintrc": true,
	"pytest.ini": true, "tox.ini": true, ".rubocop.yml": true, ".swiftlint.yml": true, "phpstan.neon": true,
	"phpunit.xml": true, "phpunit.xml.dist": true, ".pre-commit-config.yaml": true, "lefthook.yml": true,
	".gitlab-ci.yml": true, "biome.json": true, "biome.jsonc": true, "deno.json": true,
	"detekt.yml": true, "checkstyle.xml": true, ".scalafmt.conf": true, "rustfmt.toml": true,
	".rustfmt.toml": true, "clippy.toml": true, ".clang-format": true, ".clang-tidy": true,
}

// Base-name prefixes of gate configs that come in several extensions (.eslintrc.json,
// karma.conf.js, tsconfig.spec.json…).
var gateConfigPrefixes = []string{
	".eslintrc", "eslint.config.", ".prettierrc", "prettier.config.", ".stylelintrc", "stylelint.config.",
	".golangci.", "karma.conf.", "jest.config.", "vitest.config.", "vitest.workspace.", "playwright.config.",
	"cypress.config.", ".mocharc", ".nycrc", ".c8rc", "tsconfig", "jsconfig", ".lintstagedrc",
	"lint-staged.config.", "commitlint.config.", ".markdownlint", ".yamllint", "babel.config.", ".babelrc",
}

// Directories whose files are all gate or CI configuration.
var gateConfigDirs = []string{".github/workflows/", ".circleci/", ".husky/", ".thulu/", ".buildkite/"}

// IsGateConfigPath reports whether a repository path configures a quality gate or the CI that runs
// it.
func IsGateConfigPath(p string) bool {
	p = strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/")
	for _, dir := range gateConfigDirs {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	base := strings.ToLower(path.Base(p))
	if gateConfigNames[base] {
		return true
	}
	for _, prefix := range gateConfigPrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func pathsText(paths []string) string {
	shown := paths[:min(len(paths), auditDetailPaths)]
	out := strings.Join(shown, ", ")
	if rest := len(paths) - len(shown); rest > 0 {
		out += " (+" + strconv.Itoa(rest) + ")"
	}
	return out
}

func stagesText(stages []Stage) string {
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
