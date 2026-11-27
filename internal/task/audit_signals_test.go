package task

import (
	"slices"
	"testing"
)

// A run that finished well, wrote its report, touched no locked test and stayed small.
func cleanFacts() auditFacts {
	return auditFacts{
		ExpectsVerdict: true, Verdict: verdictContinue, Artifacts: 1, Risk: "medium",
		Diff: &diffFacts{MaxLines: 400, MaxFiles: 20, Files: []diffFile{{Path: "src/app.ts", Added: 10, Deleted: 2}}},
	}
}

func checkState(t *testing.T, checks []AuditCheck, id string) AuditCheck {
	t.Helper()
	for _, c := range checks {
		if c.Check == id {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", id, checks)
	return AuditCheck{}
}

func TestAuditSignals_CleanRunHasNone(t *testing.T) {
	checks := auditSignals(cleanFacts())
	if got := auditSignalNames(checks); len(got) != 0 {
		t.Fatalf("signals = %v, want none", got)
	}
	want := []string{
		AuditCheckExit, AuditCheckOutOfTurns, AuditCheckVerdict, AuditCheckLockedTests, AuditCheckScope,
		AuditCheckDiffSize, AuditCheckSkips, AuditCheckFlags, AuditCheckNoOutput,
		AuditCheckPersonRelaunch, AuditCheckRiskHigh,
	}
	var got []string
	for _, c := range checks {
		got = append(got, c.Check)
		if c.State != AuditClear || c.Detail != "" {
			t.Errorf("check %+v, want clear with no detail", c)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("checks = %v, want every check in order %v", got, want)
	}
}

// Each signal on its own calls the auditor, and names itself.
func TestAuditSignals_EachSignalFires(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*auditFacts)
		check  string
		detail string
	}{
		{"exit code", func(f *auditFacts) { f.Exit = 2 }, AuditCheckExit, "2"},
		{"out of turns", func(f *auditFacts) { f.OutOfTurns = true }, AuditCheckOutOfTurns, ""},
		{"no verdict", func(f *auditFacts) { f.Verdict = "" }, AuditCheckVerdict, ""},
		{"verdict block", func(f *auditFacts) { f.Verdict = verdictBlock }, AuditCheckVerdict, verdictBlock},
		{"verdict kick back", func(f *auditFacts) { f.Verdict = verdictKickBack }, AuditCheckVerdict, verdictKickBack},
		{"locked test changed", func(f *auditFacts) { f.LockedTouched = true }, AuditCheckLockedTests, lockedChanged},
		{"locked tests unchecked", func(f *auditFacts) { f.LockedUnchecked = true }, AuditCheckLockedTests, lockedUnchecked},
		{"gate config", func(f *auditFacts) {
			f.Diff.Files = append(f.Diff.Files, diffFile{Path: ".eslintrc.json", Added: 1})
		}, AuditCheckScope, ".eslintrc.json"},
		{"suppression", func(f *auditFacts) {
			f.Diff.Suppressions = []string{"src/app.ts"}
		}, AuditCheckScope, "src/app.ts"},
		{"too many lines", func(f *auditFacts) {
			f.Diff.Files = []diffFile{{Path: "src/a.ts", Added: 401, Deleted: 30}}
		}, AuditCheckDiffSize, "401/400 1/20"},
		{"too many files", func(f *auditFacts) {
			f.Diff.MaxFiles = 1
			f.Diff.Files = []diffFile{{Path: "src/a.ts", Added: 1}, {Path: "src/b.ts", Added: 1}}
		}, AuditCheckDiffSize, "2/400 2/1"},
		{"skips the run set", func(f *auditFacts) {
			f.AgentSkips = []Stage{"testing", "docs"}
		}, AuditCheckSkips, "testing, docs"},
		{"flags", func(f *auditFacts) { f.Flags = 3 }, AuditCheckFlags, "3"},
		{"no report", func(f *auditFacts) { f.Artifacts = 0 }, AuditCheckNoOutput, ""},
		{"person relaunch", func(f *auditFacts) { f.PersonRelaunch = true }, AuditCheckPersonRelaunch, ""},
		{"high risk", func(f *auditFacts) { f.Risk = "high" }, AuditCheckRiskHigh, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := cleanFacts()
			c.mutate(&f)
			checks := auditSignals(f)
			got := checkState(t, checks, c.check)
			if got.State != AuditSignal || got.Detail != c.detail {
				t.Errorf("%s = %+v, want signal with detail %q", c.check, got, c.detail)
			}
			if names := auditSignalNames(checks); !slices.Contains(names, c.check) {
				t.Errorf("signal names %v miss %q", names, c.check)
			}
		})
	}
}

// The refinement auditor checks every skip against the spec, so a run that set one is audited on a
// task of any risk.
func TestAuditSignals_SkipsAreASignalAtAnyRisk(t *testing.T) {
	for _, risk := range []string{"", "low", "medium"} {
		f := cleanFacts()
		f.Risk = risk
		f.AgentSkips = []Stage{"testing"}
		if got := auditSignalNames(auditSignals(f)); !slices.Equal(got, []string{AuditCheckSkips}) {
			t.Errorf("risk %q: signals = %v, want only %s", risk, got, AuditCheckSkips)
		}
	}
}

// Tests do not count toward the budget: Test Design writes them and the developer may not touch them.
func TestAuditSignals_TestsLeftOutOfTheBudget(t *testing.T) {
	f := cleanFacts()
	f.Diff.Files = []diffFile{
		{Path: "src/app.component.spec.ts", Added: 900},
		{Path: "src/app.component.ts", Added: 50, Deleted: 30},
	}
	if c := checkState(t, auditSignals(f), AuditCheckDiffSize); c.State != AuditClear {
		t.Errorf("diff_size = %+v, want clear: 80 production lines", c)
	}
}

// Deletions are not size: a change that removes a page the spec asks to drop stays under the budget
// on what it adds, and files that only lose lines do not count toward the file budget.
func TestAuditSignals_DeletionsLeftOutOfTheBudget(t *testing.T) {
	f := cleanFacts()
	f.Diff.Files = []diffFile{
		{Path: "src/app/auth-main/auth-main.page.ts", Deleted: 506},
		{Path: "src/app/app-routing.module.ts", Added: 284, Deleted: 40},
	}
	if c := checkState(t, auditSignals(f), AuditCheckDiffSize); c.State != AuditClear {
		t.Errorf("diff_size = %+v, want clear: 284 lines added", c)
	}
	f.Diff.Files = append(f.Diff.Files, diffFile{Path: "src/app/extra.ts", Added: 200})
	c := checkState(t, auditSignals(f), AuditCheckDiffSize)
	if c.State != AuditSignal || c.Detail != "484/400 2/20" {
		t.Errorf("diff_size = %+v, want signal 484/400 2/20: added lines only, files with added lines only", c)
	}
}

// A diff the daemon could not read proves nothing either way: the two diff checks say so and the
// others decide.
func TestAuditSignals_UnreadDiffIsNoSignal(t *testing.T) {
	f := cleanFacts()
	f.Diff = &diffFacts{Unread: "runner", MaxLines: 400, MaxFiles: 20}
	checks := auditSignals(f)
	for _, id := range []string{AuditCheckScope, AuditCheckDiffSize} {
		if c := checkState(t, checks, id); c.State != AuditUnread {
			t.Errorf("%s = %+v, want unread", id, c)
		}
	}
	if got := auditSignalNames(checks); len(got) != 0 {
		t.Errorf("signals = %v, want none", got)
	}
}

// No project, no diff: the diff checks are not listed. No verdict expected, no verdict check.
func TestAuditSignals_ChecksThatDoNotApplyAreLeftOut(t *testing.T) {
	f := cleanFacts()
	f.Diff = nil
	f.ExpectsVerdict, f.Verdict = false, ""
	for _, c := range auditSignals(f) {
		switch c.Check {
		case AuditCheckScope, AuditCheckDiffSize, AuditCheckVerdict:
			t.Errorf("check %q listed for a run it does not apply to", c.Check)
		}
	}
}

func TestAuditSignals_ScopeDetailCountsPastFive(t *testing.T) {
	f := cleanFacts()
	f.Diff.Suppressions = []string{"a.ts", "b.ts", "c.ts", "d.ts", "e.ts", "f.ts", "a.ts"}
	if c := checkState(t, auditSignals(f), AuditCheckScope); c.Detail != "a.ts, b.ts, c.ts, d.ts, e.ts (+1)" {
		t.Errorf("detail = %q", c.Detail)
	}
}

func TestIsGateConfigPath(t *testing.T) {
	yes := []string{
		".eslintrc.json", "web/.eslintrc.cjs", "eslint.config.mjs", ".prettierrc", ".prettierignore",
		"prettier.config.js", ".golangci.yml", "karma.conf.js", "jest.config.ts", "vitest.config.ts",
		"tsconfig.json", "tsconfig.spec.json", ".github/workflows/ci.yml", "apps/web/.github/workflows/x.yml",
		".thulu/gates.json", ".husky/pre-commit", ".semgrepignore", ".gitleaks.toml", "pyproject/.flake8",
		"sonar-project.properties", ".gitlab-ci.yml", "codecov.yml",
	}
	no := []string{
		"src/app.ts", "package.json", "README.md", "src/eslint-helpers.ts", "docs/github/workflows.md",
		"src/config.ts", "angular.json",
	}
	for _, p := range yes {
		if !IsGateConfigPath(p) {
			t.Errorf("IsGateConfigPath(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsGateConfigPath(p) {
			t.Errorf("IsGateConfigPath(%q) = true, want false", p)
		}
	}
}
