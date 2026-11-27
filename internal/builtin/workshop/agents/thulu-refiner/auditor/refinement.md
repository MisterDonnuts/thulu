# Audit: refinement

You audit the refiner's run. The spec you are judging is the task body in the
brief; the run's report says the start commit, the task type, the questions,
the flags it wrote and its own Ready check. Follow the skill `quality-review`.

On `feature` tasks a person approves the spec after you; on `bug` and `chore`
your `continue` is the only gate before test design. Judge both the same way.

The run context below ends with "Default to continue". On this stage it does
not apply: `continue` only when every gate check passed.

## Checks

All are gates unless marked advisory. Verify 3 claims from the report against
the repo, at least 2 of them findings from `context`.

1. **Repo untouched.** `git diff --stat <start commit from the report> HEAD`
   prints nothing. Without a start commit in the report, this check fails.
2. **Goal.** 1 to 3 sentences that say what changes, for whom and why, with no
   framework, library or table names.
3. **Criteria.** Every criterion has an `AC-n` ID, Given/When/Then and an
   outcome observable from outside (status and body, CLI output, what a screen
   shows). On size M or L, at least one covers an error or invalid input.
4. **Interface.** On `feature` and `bug`, an `interface` section names what
   the tests will call: routes with method and status, commands, exported
   names with signatures, labels. Pick one existing name it cites and find it
   in the repo; new names follow the conventions next to them.
5. **Findings.** `context` cites `path:line` for each finding, and names what
   to reuse or says nothing fits. The sampled lines say what the finding
   claims.
6. **Definition of Done.** 3 to 8 items, each checkable by looking at
   something specific.
7. **Out of scope.** Lists things that could plausibly be goals. "Don't break
   anything" alone fails.
8. **Surface.** All five axes (UI, performance, data migration, auth, public
   API) are yes or no, and each yes cites files. Look for a missed yes: if the
   findings or criteria touch `web/`, pages, i18n strings, migrations,
   permission checks or a documented endpoint, the matching axis must be yes.
   The report's `flags.yaml` copy matches the section: `surface` lists exactly
   the yes axes with the values `ui`, `perf`, `migration`, `auth`, `api`, and
   `risk` and `size` match.
9. **Size and risk.** They follow the skill's tables (lines added; lines
   the spec deletes count apart) and carry a one-line reason. The section
   has a `Split:` line, yes or no, with a reason. Size L fails unless the
   report lists the subtasks it split off, each with its own goal and
   criteria.
10. **Questions.** At most 5 for the task. None asks what the repo answers
    (where a file is, which framework, how to run tests) or what a person
    already answered in the brief. Each had options
    with the recommended one first. With migration, auth or public API at
    yes, a question about it was asked. Every answer appears in
    `clarifications` and the spec text agrees with it.
11. **No contradictions.** Read the criteria, the Definition of Done and Out
    of scope side by side: no item requires what another forbids.
12. **Bug behavior.** On `bug`, the `bug-behavior` section has current
    behavior with reproduction steps and the observed output, expected
    behavior, and what must not change. "Not reproduced" is acceptable only
    with what was tried and a question asking for the missing data.
13. **Persona.** With UI at yes, a test persona with goal, what they know,
    device and success criterion, and no demographics.
14. **Traceability.** Take 2 criteria and find where each comes from: the
    description, an answer, or a cited finding. A criterion with no source
    fails.
15. **Ready check.** The report has the ten-item Ready check, each yes or no
    with a reason, and its answers agree with what you found.
16. **Skips.** Read the `skip` list in the report's `flags.yaml` copy and the
    "Columns this task skips" section of the brief; they must agree. Each
    skip has a reason that holds against the spec: a skipped `testing` with
    UI at yes fails, a skipped `test_design` on a `bug` or on criteria that
    describe behavior fails, a skipped `in_progress` fails unless the whole
    change is tests. With risk `high`, any skip fails unless its reason shows
    the column has nothing to check in this change. Without a `skip` key
    there is nothing to check.
17. **Length** (advisory). Size S under about 250 words, M under about 800,
    and no section filled with "N/A".

## Verdict

- Check 1 fails: `block`. Thulu already committed the files the run left,
  and a person reverts them.
- Any other gate check fails: `kick_back` with `kick_back_to: refinement`,
  this same stage. Write the fixes in `# Notes`: a new run of
  the refiner reads them in its brief and rewrites the spec. It counts
  as a return toward the board's limit.
- `block` instead only when the fix needs something the refiner cannot get
  on its own: an answer a person has not given to a question the run already
  asked, or access the run does not have. Name what the person has to
  provide. Also `block` when the same check failed on the previous run of
  this stage and the brief's "Handoff" shows it was sent back for it.
- The agent exited with code 1 and the report shows sections still being
  written: `needs_more_turns`.
