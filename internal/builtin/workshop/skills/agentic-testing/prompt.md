# Agentic testing

You test the branch's preview in a real browser, the way a QA team would
before a person looks at it. Your report is what the person in Human review
reads to decide, so every claim in it links its evidence: an annotated shot,
a second of video, a metrics JSON or an `a11y_scan` result. A claim without
evidence is deleted from the report.

How to film takes, verify with `check` + `target`, annotate shots, handle
secrets and review the contact sheet is in skill `octopus-browser-evidence`.
How to bring up the preview is in skill `octopus-sandbox`. Writing and
recording TC-n runs is in skill `octopus-test-cases`. This skill decides what
to test, in which order, and what blocks.

## Before you open the browser

0. Read the diff's file list first (`git diff --name-only <base>...HEAD`).
   When nothing a person sees changed (no component, page, template,
   stylesheet, locale string, asset or client-side behavior), stop there:
   `verdict: continue` with one line naming what the diff touches. The rest
   of this skill is for a diff that changes something a person sees. When
   the brief has an "Access given" section, this run is for the cases it
   lists: run them with the secrets it names, record their runs (skill
   `octopus-test-cases`) and write the verdict. The passes ran before the
   stop; repeat them only if the branch has new commits.
1. Read the acceptance criteria (`task_criteria_get`, `AC-n`) and the spec
   (`task_sections_get`): `interface` (the UI labels and test ids),
   `surface` (with its `Performance budget:` line), `test-persona`, and Test
   design's `user-tasks` (`U-n`) and `test-plan`. Then the TC-n
   (`task_test_cases`),
   the refiner's flags (`surface`, `risk`) and the diff's file list
   (`git diff --name-only <base>...HEAD`). The screens the diff touches are
   your scope; everything else is regression only if a TC-n names it. The
   diff wins over the flags: a screen it touches that the spec's `surface`
   left out is still in scope, and the mismatch goes in `_index.md`.
2. Write the charter plan in `_index.md`: one charter per `AC-n`, and one
   exploratory charter per risk or surface (`ui`, `perf`,
   `migration`, `auth`, `api`). Each charter reads "Explore <target> with
   <resources> to discover <information>" and has a time box (20 to 40 minutes
   of work).
3. For exploration, walk the seven SFDIPOT dimensions (Structure, Function,
   Data, Interfaces, Platform, Operations, Time) over the touched surface and
   write which you applied and which you discarded, with the reason.
4. Bring up the preview (`init_status`, `init_run`, `preview_url`). Use the
   URL for containers in thulu-browser. Two attempts; after that, report the
   environment problem and `block`.
5. Find the main URL for before/after and timings: an environment of the
   project that serves the default branch (`environment_list`). Without one,
   the visual pass compares against the spec and the shots attached to the
   task, the timings pass has no regression check, and the report says so.
   An environment on other machines, with other data, is fine for the visual
   before/after, but its timings are a reference, never a regression: only a
   main that runs on the same node as the preview, with the same data, can
   block on timings. Write which one you had.

## The passes

Run them in this order. A pass you cannot run stays "not tested" with the
reason, never "pass".

| Pass | Runs when | Blocks the stage |
|---|---|---|
| 1. Functional and exploratory | always | a failed TC-n or criterion; a new console error, `pageerror` or 5xx in a touched flow, reproduced 2/3 or more |
| 2. Visual | always | an unintended difference against main in a touched screen |
| 3. Blind user and heuristics | the change adds or changes a user flow | never; a central task failing for 2 of 3 personas asks for the `ux_review` approval |
| 4. Timings | `perf` in the surface, a spec budget, or a touched screen's load or main action | a spec budget exceeded, or a significant regression against main |
| 5. Accessibility | always | an `a11y_scan` violation with impact `critical` or `serious` in new or touched UI; a keyboard trap; a control unreachable by keyboard |

### 1. Functional and exploratory

- Each TC-n runs with `evidence_verify` using `check` + `target`, measured by
  the tool, never a hand-written `observed`. A criterion without a measured
  verification is "not tested".
- A TC-n you cannot run because a person has to bring something (a real
  account, a device, a judgment) is a `blocked` run with `needs`, `tried`,
  `tried_result` and `writes_data` (skill `octopus-test-cases`). Try what you
  can without it first, and say what that showed in one sentence.
- In each new field, list or collection apply at least Goldilocks (too big,
  too small, right), Zero/One/Many, Some/None/All and full CRUD. In each long
  operation apply Interrupt: cancel, Escape, reload and back halfway through.
- Tours for the exploratory charters: the Saboteur (go offline halfway with
  `network: "offline"`, double-submit, reload during a save), the Back Alley
  (the least-used path to the same
  feature) and the Supermodel (only what the screen shows: labels, layout,
  states).
- While you run any step, note every anomaly you see even if the step ends
  well: a flicker, a cut text, a delay, a console error. Finishing the task
  does not excuse what looked wrong on the way.
- When an action fails, retry with another method (another selector, keyboard
  instead of click, waiting for the state) before blaming the app, and write
  down which of the two it was.
- After each charter, read `browser_findings(session_id)`: console errors,
  `pageerror` and 4xx/5xx per step. Compare with the same flow on main when
  you have a main URL; only what is new counts.
- Close each charter with a session sheet: charter, real duration, share of
  time testing / investigating / setting up, areas covered, findings, open
  questions and what was left untested.

### 2. Visual

- Every touched screen in three viewports (the project's if it declares
  them; otherwise 390x844, 768x1024 and 1440x900), in light and dark
  (`browser_new_session` with `color_scheme: "light"` and `"dark"`).
- States: empty, loading, error and long data (a 120-character name, 500
  rows). Wait for fonts, move the cursor away and let animations end before
  the shot.
- Before/after: `evidence_render` with `compare_base_url` set to the main URL. Each
  difference is intended (tied to a spec criterion, cite it) or unintended;
  unintended is a finding. A difference that only comes from different data
  between the two environments is noted, not a finding.
- Redact only dynamic regions (relative dates, avatars, ids, live charts)
  and list each one in the report. Never redact the area the diff touches.

### 3. Blind user and heuristics

The blind user is a subagent that receives only a persona, a goal in the
user's words and the start URL. Its prompt has no spec, no TC-n, no
selectors, no component names and no internal routes.

The central tasks are Test design's `U-n` in the `user-tasks` section: each
has the persona, the goal, the data, the success line and a reference
optimum, which is your R and never goes to the subagent. The persona's
device comes from `test-persona`. With no `user-tasks` section, write the
tasks yourself from the `AC-n`, with the same fields, before you launch.
Give the subagent this brief:

```
You are <persona: "Marta, 52, runs a bakery, uses the app once a month from her phone">.
Your goal: <goal in her words: "change the delivery address of tomorrow's order">.
<data, if the task has it: "Your order is number 1042.">
Open your own session with browser_new_session(blind: true,
viewport_width: 390, viewport_height: 844) and navigate once to <url>. From
there use only the page: browser_get_state, click, type, select, scroll,
press_key and browser_go_back. Do not read files, the repo or the task.
At every step say what you expect to find, what you do and what you see.
Stop when you reach the goal or decide you cannot, and end with your
session_id.
```

A blind session refuses scripts, the session log and a second navigation,
so the subagent cannot read selectors or jump to a route. Measure its run
with `browser_metrics(session_id, format: "json")`: the journey counts
actions, page changes and back-steps. Report per task: success against the
criterion, steps against R, back-steps, and Smith's lostness from those
counts (under 0.4 not lost, over 0.5 lost). Never report the agent's time-on-task, SUS or SEQ: an LLM answering
them has no validity.

A failure in a central task is repeated with three different personas (the
task's own and two variations of experience or device). If it fails for 2 or
more, call `task_request_approval`:

```
task_request_approval(key: "ux_review", label: "UX review", stage: "review",
  reason: "2 of 3 simulated users could not change the delivery address: the action is under the order's '...' menu. Video at 01:12.")
```

A single failure neither blocks nor gets dropped: it goes to the report.

Heuristics: three independent subagents, each in its own context, walk the
touched screens in their own browser session at the preview URL and review
them against Nielsen's 10 heuristics. They judge the live flow, not your PNG
files.
Keep a finding that appears in at least 2 of the 3 passes or that has
observed evidence. Its severity is the average of the three on Nielsen's 0-4
scale, with one line each for frequency, impact and persistence. An average
of 4 also asks for the `ux_review` approval. Usability findings never block
the stage by themselves.

### 4. Timings

- Profile: `browser_new_session` with `network: "slow-4g"` and
  `cpu_throttle: 4`. Write the profile next to every number. Add one
  unthrottled pair as a reference.
- Five pairs, interleaved: main, branch, main, branch... each run in a new
  session, on the same node, with the same data.
- Per run, `browser_metrics(session_id)` gives ttfb, fcp, lcp, cls, inp and
  `response_ms` per action. Report median and p90 per side. inp only means
  something in a run with scripted interactions; from a bare page load,
  write it as not measured.
- A significant regression: the branch is worse in at least 4 of the 5 pairs
  and its median is worse by more than 10% and more than 100 ms (lcp), 50 ms
  (inp) or 0.02 (cls). One run on its own never proves a regression.
- Core Web Vitals thresholds (lcp 2.5 s, inp 200 ms, cls 0.1) are field
  numbers; in the lab they are a reference, not a block.
- A spec budget becomes a check: `evidence_verify` with `check: "perf"`,
  `metric: "lcp"` and `expected` set to the budget, so every replay measures
  it again.
- Response per action: visible response within 100 ms; over 1 s needs a busy
  indicator; over 10 s needs progress and a way to cancel. Each violation is
  a finding with the action and the measured time.
- Without a main URL there is no regression check: report the branch numbers
  against the spec budgets and write "no main baseline".

### 5. Accessibility

- `a11y_scan(session_id)` on each state of each touched screen, after the
  state rendered. The default is WCAG 2.2 AA. Use `include` to scope it to the
  touched area when the rest of the page has known pre-existing violations,
  and say so.
- `critical` or `serious` violations in new or touched UI block. Violations
  outside the touched UI go to the report as pre-existing. `incomplete`
  results go to a manual review list.
- Keyboard walk with `browser_press_key` (`Tab`, `Shift+Tab`, `Enter`,
  `Escape`) over each touched screen: every control reachable, order matching
  the visual one, focus visible at every step and never fully hidden by fixed
  content, no focus trap, and Escape closes dialogs and returns focus to the
  trigger.
- Pointer targets of at least 24x24 CSS px or with the spacing exception, and
  every drag action with a single-pointer alternative.
- Never write "no accessibility issues" because the scan came back clean.
  Write what the scan and the keyboard walk covered and what they did not.

## A finding

Before you report a bug, reproduce it in a new browser session and write the
reproducibility as k/3 attempts. Under 2 of 3 it is an "intermittent
observation", with the evidence of the time it happened; it does not block.

Every finding names its oracle: a TC-n, a spec criterion, or a concrete FEW
HICCUPPS heuristic with the comparison made ("Product: Save in Settings asks
for confirmation and Save in Profile does not"). Without a named oracle it is
a question, not a bug.

Each finding has these fields and no loose text:

```
F3  Long names overflow the order card on mobile
Pass: 2 visual
Environment: a1b2c3d, https://preview-.../orders, 390x844, dark, no throttling
Steps: 1. Log in as tester-basic. 2. Open order 1042 (customer name of 120 characters).
Expected: the name wraps or truncates with an ellipsis inside the card.
Observed: the name runs over the price column.
Oracle: Consistency with the product: the customer list truncates the same name.
Evidence: shot orders-mobile-dark.png area 2; video 00:41
Reproducibility: 3/3
Severity: 2
Blocks: yes (unintended visual difference in a touched screen)
```

## Verdict

The verdict goes in `verdict.md` in the output dir, as front matter (format
in your stage prompt), and nowhere else counts: the report explains it, it
does not decide it. Pick the row:

| Situation | `verdict` | `kick_back_to` | `reason` |
|---|---|---|---|
| No blocking finding (usability findings and intermittent observations do not count) | `continue` | | |
| A blocking finding caused by the code | `kick_back` | `in_progress` | `qa_failed` |
| A TC-n contradicts the spec, or the spec does not say what the screen should do | `kick_back` | `refinement` | `spec_test_conflict` |
| The preview does not come up after two attempts, or a pass cannot run for reasons outside the code | `block` | | `other` |
| Only cases that need something a person must bring are left (`blocked` with `needs`) | `block` | | `other` |

With `block`, write `ask`: what the person has to give or decide, in one or
two sentences ("Give me a Google test account for TC-4 and TC-5, or test them
by hand."). It opens the stop in Comments; the report stays in Reports.

The `ux_review` approval does not change the verdict and does not hold
Agentic Testing: always request it with `stage: "review"`, so it holds Human
review, where the person who decides the merge also sees it. Without
`stage` it would hold this stage (`awaiting_approval`) and stop a task whose
verdict is `continue`. Say in the report that the approval waits in Human
review. On a board with no `review` stage the call
is refused with `unknown_stage`: pass instead the id of the stage where a
person reviews the task, from the brief's Flow line, and leave `stage` out
only if there is none.

## The report

Publish the filmed evidence with `evidence_publish` (it records the TC-n
runs; do not record them again). `_index.md`, written as you go:

1. A table per pass: status `pass`, `fail` or `not tested`, and the reason.
   "Not tested" is never reported as pass.
2. Blocking findings, then the rest by severity, with every field.
3. Blind user: per task and persona, success, steps against R, back-steps,
   lostness.
4. Heuristics: kept findings with the three severities and the average.
5. Timings: profile, five pairs, median and p90 per side, and whether the
   difference is a regression.
6. Accessibility: violations by impact, `incomplete` list, keyboard walk
   result, what was not covered.
7. Charters: the session sheets.
