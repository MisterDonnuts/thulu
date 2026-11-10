# Recording a case from the Evidence library

A library case says in words what has to work ("Login takes you to the
dashboard"). You record it once, in the chat Thulu opens for that. From then
on Thulu replays your take with no model against any environment (by hand,
on a schedule or after a deploy), and each run leaves a video, screenshots and
a pass/fail per verification. The people who look at those runs are QA or
product people: they read your labels and your verifications, never the take.

This skill applies only in a recording chat. In a task, evidence is delivered
with `evidence_publish` (skill `octopus-browser-evidence`).

## Walkthrough

```
evidence_recording                                   # case, environment, login names
browser_new_session(viewport_width=1440, viewport_height=900)
browser_navigate(url="https://staging.example.com/login")   # the environment's base URL
evidence_start(title="EV-3 · Login takes you to the dashboard")
browser_get_state
browser_type(index=2, secret="QA_EMAIL", label="I type the test account's email")
browser_type(index=3, secret="QA_PASSWORD", label="And its password")
browser_click(index=4, label="I sign in")
evidence_verify(case="EV-3", expected="/dashboard", check="url", label="I reach the dashboard")
evidence_verify(case="EV-3", expected="Hi, QA", check="text",
                target="[data-testid=greeting]", label="It greets me by name")
evidence_finish                                      # look at the contact sheet
evidence_save_case(take_id=...)
```

## Before filming

- Call `evidence_recording` first. It returns the case's title, steps and
  expected result, the environment (its visible name `display_name` or, if
  that is empty, `name`, the project, whether it is production and the base
  URL) and the names of its test logins. If it answers that there is no open
  recording, do not go on with this skill.
- If you do not know the app, walk through the flow once without recording
  (`browser_navigate`, `browser_get_state`). What you explore before
  `evidence_start` does not go into the take.
- Start the take at the environment's base URL or at the page where the case
  starts. Navigations to the same origin are stored as paths (`/login`), and
  that lets the take replay in another environment.

## One case, one flow

- Record what the case's steps and expected result say. If the case mixes two
  flows, record the main one and tell the person that the other deserves its
  own case.
- Between 3 and 8 verifications, each one on something from the expected
  result.
- Write the labels with the case's words, telling what happens: "I sign in
  with the test account", not "Click on #submit". The label is the video's
  subtitle and the name of each verification in the results table, so it has
  to read the same in every run: no times, ids or data of the day.

## Verifying so the replay measures

- Every verification carries `check` + `target` (or `check="url"` /
  `"title"`, which need no target). With `observed` alone, each replay copies
  what you saw today and never fails. `evidence_save_case` warns if any was
  left that way.
- Pick targets that survive a redesign: test id, role and accessible name.
  Avoid ones that depend on data that changes (a total, a date, a customer),
  unless that data is what the case tests.
- For values that change from one environment to another, `match="contains"`
  or `match="regex"`.
- `evidence_verify` waits up to `timeout_ms` (5 s by default) for the value to
  match, so data that arrives late does not show red.
- `case` carries the case key (`EV-3`), in all of them. Each run judges all
  the verifications together: `pass` if they all pass, `fail` if one fails,
  `blocked` if a step failed before reaching a verification.

## Test logins

- `evidence_recording` lists the names (`QA_EMAIL`, `QA_PASSWORD`). Type them
  with `browser_type(secret="QA_PASSWORD")`, without `text`: thulu-browser asks
  Thulu for the value, types it as dots and the take stores only the name.
  You never see it, and on each replay the runner fills in the value of the
  environment it runs in.
- Never ask the person for a password or write it in `text`. If a login the
  case needs is missing, tell them which one: anyone on the team adds it in
  Evidence → Environments, and an admin does if the environment is production.
- If the login fails with the stored values, do not try others. Report that
  the test account for that environment does not get in.

## Steps that change data

- Avoid creating, deleting, paying for or sending anything, unless the case is
  about that.
- If the case is about that, start with `evidence_start(changes_data=true)` and
  tell the person: a take that changes data does not replay on its own, so
  every automatic run will end in `error`. It stays as a reference recording.

## Reviewing and saving

- Look at the contact sheet `evidence_finish` returns. Each frame makes sense
  with its label, no verification turned red without you expecting it and no
  step says STEP FAILED.
- If something went wrong, film again (`evidence_start` again) or fix the take:
  `evidence_get_take` gives it to you as JSON and `evidence_save_case(take=...)`
  saves the fixed version.
- Save with `evidence_save_case(take_id=...)`. There is no task here, so do not
  use `evidence_publish`.
- The take has to replay without a repo's library: no `use` steps and no
  `@name` targets. A take recorded live already meets this.
- You save only once: saving closes the recording and Thulu queues a first
  run of the case in the same environment. Do not wait for it.

## Closing

Tell the person in two lines what you recorded and that the first run is
already queued:

> I recorded EV-3: signing in with the test account and reaching the dashboard, with 3 verifications.
> A first run is queued on Staging; the result will show up in the case.

If the take changes data or is missing something (a login, a verification you
could not measure), add a line saying so.

## Common errors

- "there is no open recording in this chat": `evidence_recording` and
  `evidence_save_case` only work in the chat opened from Evidence → a case →
  Record with agent, and only until the take is saved.
- "no test login named X": the name is misspelled or the environment does not
  have it. Use exactly the ones `evidence_recording` lists.
- The take does not compile on save: it has `use` steps or `@name` targets.
  Record it live or replace them with selectors.
- A verification passes live and fails on the first run: almost always the
  target depended on data from that session. Change it to a test id or a role
  and record again.
