package builtin

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// On a real task's rerun four of seven stops for a person were ones a rule could decide: locked specs
// test design left unformatted, failures base has too (asked twice), and a diff over the size budget
// that was mostly deletions the spec asked for, after refinement had decided not to split. The
// prompts now carry those rules; this test keeps them from being edited out.

var spaces = regexp.MustCompile(`\s+`)

// flat reads a shipped file with every run of whitespace turned into one space, so a phrase that a
// rewrap splits across lines still matches.
func flat(t *testing.T, fsys fs.FS, p string) string {
	t.Helper()
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return spaces.ReplaceAllString(string(b), " ")
}

func TestPromptsCarryTheRulesThatReplacedStops(t *testing.T) {
	cases := []struct {
		fsys  fs.FS
		file  string
		why   string
		wants []string
	}{
		{Workshop(), "agents/thulu-test-designer/prompts/test_design.md", "locked specs are formatted before they lock",
			[]string{"format every test, fixture and stub file you wrote", "format check and linter"}},
		{Workshop(), "skills/test-design/prompt.md", "the skill says how to format before the lock",
			[]string{"## Format before they lock", "format check"}},
		{Workshop(), "agents/thulu-test-designer/auditor/test_design.md", "the auditor sends unformatted tests back",
			[]string{"**Formatted.**"}},
		{Workshop(), "agents/thulu-developer/prompts/in_progress.md", "the developer formats what it writes and does not stop for base",
			[]string{"format every file you created or edited", "Never stop for what is not this change's",
				"`Split:` line says `no`", "do not run the full suite separately"}},
		{Workshop(), "skills/implementation-discipline/prompt.md", "Development's gates run is the one Verification reuses",
			[]string{"--reuse", "--critical <glob>", "Deleted lines the spec asks for", "`Split: no`",
				"a test nobody locked that pins the behavior the spec changes"}},
		{Workshop(), "agents/thulu-developer/auditor/in_progress.md", "the auditor honors the split decision and leaves base failures out",
			[]string{"`Split: no`", "deletions do not count", "they never fail this check"}},
		{Workshop(), "agents/thulu-verifier/prompts/verification.md", "Verification reuses and does not ask about base",
			[]string{"`--reuse`", "Never run the full suite again", "do not ask a person about them"}},
		{Workshop(), "skills/quality-gates/prompt.md", "an unverifiable check is accepted once, for the project",
			[]string{"`accept_unverified`", "Never ask a person to accept a check for this task alone",
				"kick_back` to `test_design`"}},
		{Workshop(), "skills/production-readiness/prompt.md", "readiness does not reopen accepted checks",
			[]string{"`accepted`", "never send it back or ask a person about it again"}},
		{Workshop(), "agents/thulu-refiner/prompts/refinement.md", "the refiner records the split decision",
			[]string{"`Split:` line"}},
		{Workshop(), "skills/spec-writing/prompt.md", "the surface block shows the split line and counts added lines",
			[]string{"Split: no (", "Lines the spec asks to delete", "Drop what a person already answered"}},
		{Workshop(), "agents/thulu-refiner/auditor/refinement.md", "the refiner's auditor checks the split line",
			[]string{"`Split:` line"}},
		{Workshop(), "agents/thulu-reviewer/prompts/ai_review.md", "review does not block on size",
			[]string{"Block or send back for the diff's size"}},
		{Workshop(), "agents/thulu-agent/prompts/in_progress.md", "the minimal agent formats what it writes",
			[]string{"Never leave a new file unformatted"}},
		{SystemSkills(), "octopus-run/prompt.md", "agents find their harness in the run, not on the host",
			[]string{"/workspace/skills/<name>/prompt.md", "/workspace/instructions/prompts/main.md", "`~/.claude`"}},
		{SystemSkills(), "octopus-task-workflow/prompt.md", "a non-blocking question applies its default and does not wait",
			[]string{"never wait for the answer", "the `clarifications` section"}},
	}
	for _, c := range cases {
		body := flat(t, c.fsys, c.file)
		for _, want := range c.wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q (%s)", c.file, want, c.why)
			}
		}
	}
}

// Shapes that sent a task to a person for nothing: a non-blocking question with no default (the API
// refuses it, task.questions.errors.default_required), and a size budget over changed lines, which
// counts the deletions a spec asks for.
func TestNoPromptAsksWithoutADefaultOrCountsDeletions(t *testing.T) {
	banned := regexp.MustCompile(`(?i)task_ask` + "`?" + ` with a non-blocking question|400 changed (production )?lines`)
	walked := 0
	for name, fsys := range map[string]fs.FS{"workshop": Workshop(), "system-skills": SystemSkills()} {
		err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path.Ext(p) != ".md" {
				return err
			}
			walked++
			if m := banned.FindString(flat(t, fsys, p)); m != "" {
				t.Errorf("%s/%s: %q", name, p, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if walked < 20 {
		t.Fatalf("walked %d prompts; the guard is not looking at anything", walked)
	}
}
