# Browser evidence

Evidence is for whoever reviews the task, not for you. It has to stand on
its own: without opening the repo, without reproducing anything, without asking you what
you were looking at. This skill teaches how to produce it with the `evidence_*` tools of
`thulu-browser`.

## What to deliver

| What you need to show | Deliverable |
|---|---|
| A state: how something looks, where the error is | An annotated screenshot (`evidence_shot` outside a take) |
| A flow: steps that lead to a result | A filmed take, with verifications |
| A fix or a change | The same take against the before and the after (`compare_base_url`) |
| The task's test cases | A take with one `evidence_verify` per case, with the case's key (`TC-2`); on publish, each case gets its run |

If a screenshot is enough, don't film. A 40-second video to show a
wrong color wastes the reviewer's time.

## Two ways to film

**Live** (`evidence_start` … `evidence_finish`). You film while you work
a session. It is the way to go when you are still exploring, when the flow
changes data (a replay would change it again) or when there is no written
take for that flow.

**Replay** (`evidence_render`). A JSON take is filmed in a clean browser.
It is the way to go when the take already exists in the repo, when you need the before and
the after, or when you want to film again after fixing something. The replay
decides nothing: it comes out the same every time.

Both end the same way: a draft you review and then publish.

## Live take

```
task_test_cases($TASK_ID)                       # the keys (TC-1, TC-2…) go in `case`
browser_new_session(task_id=$TASK_ID, viewport_width=1440, viewport_height=900)
browser_navigate(url=...)                       # getting to the starting point is not filmed
evidence_start(title="SHOP-142 · SAVE10 takes 10% off the order total")
browser_get_state
browser_type(index=4, text="SAVE10", label="Type the SAVE10 coupon")
browser_click(index=5, label="Apply it")
evidence_verify(case="TC-2", expected="$90.00", check="text", target="[data-testid=order-total]",
                label="The total drops to $90")
evidence_shot(name="total-with-coupon", title="Total with SAVE10",
              spotlight=[{index: 4, note: "SAVE10 applied"},
                         {target: "[data-testid=order-total]", note: "$100 → $90", tone: "ok"}])
evidence_finish                                  # returns the contact sheet
evidence_publish(render_id=...)
```

Between `evidence_start` and `evidence_finish`, every `browser_navigate`, `click`,
`type`, `select`, `hover`, `press_key`, `scroll` and `wait` is filmed: the cursor
travels to the element, a ring marks it, the action shows and the
`label` appears at the bottom. The time you spend thinking between two calls is not filmed, so there is no
need to hurry or to pad with `browser_wait`.

Each action is recorded in the take with a stable selector: if you pointed at it
by index from `browser_get_state`, the tool looks for one that survives a
redesign (test id, role and accessible name, label, placeholder) and checks that it
points only at that element. If you passed a selector, it keeps it as is.

An action that fails doesn't go into the take. Fix it and carry on; the video doesn't
show it either.

## Labels

The `label` is the video's caption and the report's chapter. Write it in the
ticket's words, telling what happens, not what you touch:

- "Apply the SAVE10 coupon", not "Click on button#apply".
- "The total drops to $90", not "Verify text".
- One short sentence per step. If it doesn't fit on one line, it is two steps.

A step without a `label` gets an automatic one ("Click Apply"). It works as a fallback,
but a video full of those tells nobody anything.

## Verifying

`evidence_verify` records a case: what was expected and what is there. The video
shows a green or red card; the report, a table row.

Measure whenever you can: `check` + `target` makes the tool look at the
page, and it looks again on every replay. It waits up to `timeout_ms` (5 s by
default) for a match, so a total that updates late doesn't give a false
red.

| check | observes | example |
|---|---|---|
| `text` | the element's visible text | `expected: "$90.00"` |
| `value` | an input's value | `expected: "SAVE10"` |
| `visible` / `hidden` | `"visible"` or `"hidden"` | `check: "hidden", expected: "hidden"` for "the error doesn't appear" |
| `count` | how many elements match | `expected: "3"` |
| `url` | the current URL (contains, by default) | `expected: "/orders/"` |
| `title` | the page title | |

`match` changes the comparison: `equals` (default), `contains`, `regex`.

`observed` without `check` is for what only you can judge ("the icon is
aligned with the text"). In the report it is marked as observed by the
agent, not measured. Use it sparingly.

Before filming, read the task's cases with `task_test_cases` and use their key
(`TC-3`) as `case`, written exactly the same. That way the report and the cases talk about the
same thing, and on publish each case gets its run. If the task has no cases,
use the ticket's identifier (`DoD-2`) and no run is recorded.

## Annotated screenshots

`evidence_shot` marks areas with a numbered box and a note, dims the
rest, covers what must not be seen and adds a header with host and time.

- Three or four areas at most. If you need more, it is two screenshots.
- Each note goes next to its area without covering another area or another note; if it doesn't fit
  anywhere, it moves to the list below with its number. If the response says
  "Too crowded", the screenshot covers something: redo it split in two, or with
  shorter notes, before publishing.
- `tone`: `focus` (what to look at), `ok`, `warn`, `bad`.
- `notes`: context that isn't on screen ("test user on the Pro plan").
- `redact`: emails, tokens, customer names. It covers them with a block; whatever you
  pass in `redact_css` when starting the take is blurred for the whole video.
- `keep_clean: true` also delivers the unannotated screenshot, for anyone who wants to
  use it elsewhere.
- `wait_ms: 0` when the finding is that something is **not** there: the tool doesn't
  wait for it to appear and lists it as absent.

Inside a take, the screenshot also goes into the video for a couple of
seconds. Outside a take it is uploaded to the task right away.

## Secrets

Never write a password or a token in `text` as is. Pass
`browser_type(text=..., secret="password")`: the value is typed as dots, it doesn't
end up in the log or in the response, and the take stores only the name
(`{"kind":"fill","secret":"password"}`). In a replay the value comes in through
`evidence_render(secrets={"password": ...})`, read from wherever the environment keeps
credentials. Never write it to a file in the repo.

## Review before publishing

`evidence_finish` and `evidence_render` publish nothing. They return the
`render_id`, a summary of verifications and failures, and the contact sheet: an
image with one frame per chapter. Look at all of it before publishing:

1. Does each frame make sense without the previous one? If not, the step is missing a `label`.
2. Do the caption or the card cover what is being shown? Move the action or
   split the step.
3. Are the ring and the boxes on the right element?
4. Any "STEP FAILED" or screenshot with absent areas? That will stay in the
   report as a finding. If it isn't one, fix the selector and film again.
5. Is personal data visible? `redact` or `redact_css`.

If something is wrong, fix the take (`evidence_get_take` gives it to you as JSON) and
film it again with `evidence_render`. Publish only the good version: each
`evidence_publish` is one more report in the task's Reports tab.

## Publishing and closing cases

`evidence_publish(render_id)` uploads the video, the screenshots, the chapters
(WebVTT) and the take to the task, and publishes the report in the Reports tab.
Comments are left for the conversation. The report's title carries the take's name
and the status in words; the body, environment, video, case table,
findings, screenshots and timed chapters. The response includes its id:
`Report posted to the Reports tab (<id>).`

It also records a run on each of the task's cases whose key appears in an
`evidence_verify`, with the video, the take, the screenshots and the report, and sets
the case to the run's status. Several verifications of the same case make one
run, which passes only if all of them pass; in a before/after the after is what counts.
The response says so:

```
Recorded runs: TC-1 pass, TC-2 fail
Not test cases on this task: TC-2a
```

- Don't call `task_test_case_run` for the same evidence: the run is already there.
- A red case stays in `fail` with its run. Don't change it by hand: it gets
  reported.
- A key the task doesn't have is not recorded. If it was a misspelled case,
  fix the `case` in the take before next time.
- If the response says `Could not record the test case runs`, the video and the
  report are already on the task: record those runs with
  `task_test_case_run` and the `report_id` the response gives, without publishing
  again.

## Before and after

```
evidence_render(take=<the take>, base_url="https://main.preview.example",
                compare_base_url="https://pr-123.preview.example",
                labels=["Before", "After"])
```

The same take runs against both origins. The video ends up side by side, and on
each step the faster side waits for the other, so both reach each
chapter together. The report's table puts both results for each case on the same
row.

- The after has to be the same view with the same steps. An after
  taken from another screen proves nothing.
- The after belongs to the commit that is going to be merged. If you add a commit
  after filming, film again.
- The take's navigations are paths (`/checkout`), not full URLs: that
  is what allows switching origins. A live take stores them that way on its own.

## Flows that change data

Creating an order, deleting a record or sending an email are effects a replay
would repeat. If the flow has one:

- Live, start with `evidence_start(changes_data=true)`. Each action gets
  marked with `side_effect`.
- In a hand-written take, mark the steps that change something with `"side_effect": true`.
- `evidence_render` refuses to run a marked take. `allow_side_effects:
  true` allows it; use it only against a disposable environment (a per-PR preview,
  a database seeded for tests), never against a shared one.

A replay only navigates to the hosts of its `base_url` (and `compare_base_url`), plus
those listed in `allowed_hosts`. A link that leads to another host is cut off before it
leaves and the step is recorded as a failure.

## Takes in the repo

When a flow is going to be filmed more than once, the take lives in the repo, next to
the code it tests:

```
.octopus/evidence/
  targets.json            name → selector, to write @order_total
  flows/login.json        reusable steps, with {{parameters}}
  flows/add-to-cart.json
  takes/shop-142-coupon.json
```

There is a complete example in this skill's `examples/evidence/` (in a repo it goes
in `.octopus/evidence/`). To
film a take from the repo, read the files and build the call like this:

```
evidence_render(
  take    = <takes/shop-142-coupon.json>,
  library = { targets: <targets.json>,
              flows: { "login": <flows/login.json>, "add-to-cart": <flows/add-to-cart.json> } },
  secrets = { "password": ... })
```

- `@name` in `target` (and in `redact`) is replaced by the selector from
  `targets.json`. If the selector changes, it changes in one place only.
- `{"kind": "use", "flow": "login", "params": {"email": "qa@shop.example"}}`
  expands the flow's steps at that point.
- A new take that worked live is saved with `evidence_get_take`, the
  repeated selectors are swapped for `@names` and it is committed with the PR.

## Step reference

All of them accept `label` and `side_effect`.

| kind | fields |
|---|---|
| `goto` | `url` (path or URL); `wait_for`: a target that has to appear before filming, for apps that render after loading |
| `click`, `hover` | `target` |
| `fill` | `target`, `value` or `secret` |
| `type` | `target`, `value`, `delay_ms` (types without clearing) |
| `select` | `target`, `value` |
| `check` | `target`, `checked` (true by default) |
| `press` | `key`, optional `target` |
| `scroll` | `direction` (`down`, `up`, `top`, `bottom`) and `amount`, or `target` |
| `wait_for` | `target`, `state` (`visible`, `hidden`), `timeout_ms` |
| `wait_for_response` | `url_part`: waits for a response whose URL contains it |
| `beat` | `ms`: a pause that stays in the video, with a progress bar; without `label` it is added to the previous chapter |
| `hold` | `ms`: a pause without a bar |
| `viewport` | `width`, `height` |
| `shot` | `shot`: same as `evidence_shot` |
| `verify` | same as `evidence_verify` |
| `note` | `text`, `level` (`step`, `fact`, `issue`) |
| `use` | `flow`, `params` |

The take: `version: 1`, `title`, `base_url`, `viewport`, and optionally
`timezone`, `locale`, `allowed_hosts`, `redact_css`. Setting `timezone` and
`locale` makes dates and currencies come out the same on every replay.

## Common errors

- `could not describe <div> with a selector that matches only it`: the element
  has nothing that identifies it. Point at a more specific one (the button, not its
  container) or pass a selector yourself.
- A step that fails in the replay and worked live: almost always a selector
  that depended on the text of a piece of data. Move it to `targets.json` with a test id or a
  role.
- Red verification because of a value that arrives late: the problem is not the timing,
  it is the verification. Wait for the response with `wait_for_response` before
  verifying.
- `the take changes data`: see [Flows that change data](#flows-that-change-data).
- `no take is recording on this session`: `evidence_start` is missing, or the session
  expired (10 minutes unused) and took the take with it.
