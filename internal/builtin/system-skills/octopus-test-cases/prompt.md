# Test cases

A task's test cases are rows of their own: a key (`TC-1`), a title, the
description, the steps, the expected result, the priority, a status and every
run that executed it, with its video, its take and its report. QA reads them in
the task and in Evidence, "From tasks" tab (`/evidence/?tab=tasks`), without
opening the repo or asking you anything. This skill teaches how to write and
maintain them with the `octopus-api` tools.

| Tool | Does |
|---|---|
| `task_test_cases` | lists the task's cases, in order, with their runs |
| `task_test_case_add` | adds one or more cases (`cases: [...]`) |
| `task_test_case_update` | changes a case's fields, order, status or `covered` |
| `task_test_case_delete` | deletes a case and its runs |
| `task_test_case_run` | records a run with its verdict and its evidence |

In all of them, `case` accepts the key (`TC-3`) or the id.

## Writing a case

A case tests one behavior. If the expected result joins two things that can
fail separately, they are two cases: when one fails, QA has to know which.

- **Title**: the behavior, in the user's words. "SAVE10 takes 10% off the
  order total", not "Coupon test". Up to 200 characters; in Evidence it is read in a list of hundreds, next to the task
  title.
- **Description**: what has to be in place before starting. User,
  configuration, seeded data, environment.
- **Steps**: a numbered list another person can follow without guessing.
  Which screen, which field, which value.
- **Expected**: something that can be checked by looking at the screen, with
  real values. "Discount -$10.00 and Order total $90.00", not "the discount
  is right".
- **Priority**: `critical` if it touches money, data or security, or if it
  breaks the main flow; `high` for the main flow; `medium` (the default) for
  the rest; `low` for cosmetic things.

Besides the happy path, cover the edges (the value right at the minimum,
zero, empty), the error the app has to show, and what must not change (the
regression the fix could bring).

```
task_test_case_add(id=$TASK_ID, cases=[
  { title: "SAVE10 takes 10% off the order total",
    description: "Logged in as qa@shop.example. Cart: one blue mug at $100.00.",
    steps: "1. Open the cart.\n2. Enter SAVE10 in Coupon.\n3. Press Apply.",
    expected: "Discount -$10.00 and Order total $90.00.",
    priority: "critical" },
  { title: "An order right at the coupon's minimum accepts it",
    description: "SAVE10 asks for a $50.00 minimum. Cart: one mug at $50.00.",
    steps: "1. Open the cart.\n2. Enter SAVE10 in Coupon.\n3. Press Apply.",
    expected: "Discount -$5.00 and Order total $45.00.",
    priority: "high" },
  { title: "An expired coupon is refused and the total stays",
    description: "EXPIRED5 ended yesterday. Cart: one blue mug at $100.00.",
    steps: "1. Open the cart.\n2. Enter EXPIRED5 in Coupon.\n3. Press Apply.",
    expected: "The message 'This coupon has expired' and Order total $100.00.",
    priority: "high" }
])
```

### Keys

Let the task assign them: without `key`, each new case gets the next
`TC-<n>`. Pass one only when the ticket already names the case (a `TC-4` in
the task description), in the form `TC-4` or `TC-4a`.

A key is never renumbered or reused. Runs, `thulu-browser` reports and QA
links point to it. If a case stops applying, it is deleted or marked
`skipped`, and the gap in the numbering stays.

### Before adding

Read `task_test_cases` first. If the case already exists with different text,
fix it with `task_test_case_update`: it keeps the key and the runs. Deleting
and creating it again loses both.

## Tasks with `## Test Cases` in the body

That section is a checklist in the task's markdown. It shows in the task, but
when a task has rows, the rows win. Do not write new cases in it.

There is no import tool to move it into rows: the drawer has the "Import from
body" button for whoever does it by hand. As an agent:

1. `task_test_cases`: if there are rows already, only add the items whose title
   is not there.
2. `task_sections_get` and take the items of the `test-cases` section.
3. `task_test_case_add` with one case per item, in the same order, with the
   item text as the title. If the item carries the expected result ("SAVE10 →
   total $90"), move it to `expected`.
4. For each checked item (`- [x]`), `task_test_case_update(status="pass",
   covered=true)`. This is the same thing the button does: there is no run
   behind it, and do not make one up.

Do not delete the section from the body unless the task asks for it.

## Recording a run by hand

A run is one execution: the verdict, what was seen and the evidence. When you
record it, the case takes the verdict's status and becomes covered.

If you filmed with `thulu-browser`, `evidence_publish` already recorded the
run of each `evidence_verify` whose `case` is a key of the task, with the
`report_id` of the report it published in the Reports tab (see the
`octopus-browser-evidence` skill). Do not record it again: it would count the
same execution twice.

For everything else (a `curl`, an automated test, a browser test without a
take), record it yourself:

1. Upload the files with `task_attach` and keep their ids.
2. If there is a report, publish it with `task_add_report` and keep its id. It
   goes to the Reports tab and stays folded under the run; the task
   conversation does not fill up with evidence.
3. `task_test_case_run` with the ids:

```
task_test_case_run(id=$TASK_ID, case="TC-3", verdict="fail",
  observed="Order total $100.00: the 10% was not subtracted",
  note="Reproduced in the preview of PR #12, Chrome 131.",
  capture_attachment_ids=["<id of total-without-discount.png>"],
  report_id="<id of the report>")
```

`observed` carries the value that appeared, in one or two lines: "Order total
$100.00", not "it doesn't work". A run is never edited: if you test again
after a fix, that is another run.

## Statuses

| Status | When | How |
|---|---|---|
| `pending` | nobody has executed it yet | the initial status |
| `pass` | it was executed and what was observed matches the expected result | run with `verdict: "pass"` |
| `fail` | it was executed and it does not match | run with `verdict: "fail"` and what was seen in `observed` |
| `blocked` | it could not be executed because of something outside the case: the environment is down, a piece of data is missing, another bug breaks the flow before getting there | run with `verdict: "blocked"` and the cause in `observed` ("login returns 502 on staging") |
| `skipped` | it was decided not to execute it in this task: it is out of scope, or the platform does not apply | `task_test_case_update(status="skipped")` and the reason in a task comment |

A red case is reported with its run in `fail`. Do not move it to `skipped` or
edit it so the expected result matches what appeared. Never `pass` without
having executed it.

## How QA reads it

- In the task, the "Test cases" block comes after the body: one bar per status
  and one row per case with its status, its priority and the "Tested by
  someone" checkbox (`covered`). Expanded, the row shows description, steps,
  expected result and the runs, with the video inside and links to the take
  and the report.
- `/tasks/#task-<id>/TC-2` opens the drawer on that case. Use it when you name
  a case in a comment, so QA gets there in one click.
- Evidence, "From tasks" tab (`/evidence/?tab=tasks`), gathers the cases of
  every task in the workspace, with filters by status, board, text and
  "Uncovered only", the totals per status at the top and how many are covered.
  From there QA moves the ones worth keeping into the Evidence library.
