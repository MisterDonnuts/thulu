# Working a task with people and other agents

People and agents work a Thulu task in the same place: the comment thread,
the questions, the approvals, the body with its sections, the test cases and
the reports. This skill teaches what to use from `octopus-api` in each case.
Test cases have their own skill (`octopus-test-cases`).

## You need something from someone

| Situation | Use |
|---|---|
| A decision the task leaves open that has a reasonable default | `task_ask` with `blocking: false` and `default` |
| A decision without which the work that follows gets thrown away | `task_ask` (blocking, the default) |
| You cannot go on in any way: a credential or an access is missing, the request contradicts itself | `task_ask` with `kind: "blocker"` |
| A person should review what you changed before the task moves on ("I touched the checkout, design should look at it") | `task_request_approval` |
| Another agent should weigh in or do a part | a comment that names it with `@name` |
| Telling people something that expects no answer | `task_add_comment` |
| Long evidence: checks, logs, screenshots | `task_add_report` |
| A test case that could not be executed because of something outside it | a `blocked` run of the case (skill `octopus-test-cases`) |

Before asking, check whether the description, the comments, the repo or the
"Open questions" section of the brief already answer it. What has already been
asked is not asked again.

## Asking with `task_ask`

Ask before the expensive work, not after. Everything together, in one call:

```
task_ask(kind="question", blocking=true,
  context="The ticket asks for a CSV export. The list has 40k rows and today
           pages 50 at a time; generating it inside the request takes ~30 s.",
  questions=[
    { header: "Export",
      question: "Is the CSV generated on the spot or emailed when it is ready?",
      options: [
        { label: "By email", description: "Recommended: does not block the request. Needs the job queue." },
        { label: "On the spot", description: "Simpler; the user waits ~30 s." }
      ] },
    { header: "Columns",
      question: "Do I export the visible columns or all of them?",
      options: [{ label: "Visible" }, { label: "All" }] }
  ])
```

- `context`: what you were doing and what you found, in two or three
  sentences. It is the first thing the person answering reads.
- `header`: a label of up to 12 characters.
- Options: up to 6, the recommended one first and saying so in its
  `description`. With no options, the answer is free text. `allow_other`
  (true by default) lets the person write something else.
- Up to 4 questions per call, 3 calls per run and 5 open questions per task.
- A `blocker` is always blocking and the person answering has to write
  something: picking an option is not enough.
- Only the stage run asks. In a mention or a `continuation`, `task_ask` is
  rejected: ask in your reply.

### After asking

**Blocking:** end your turn right away, with one line saying what you asked.
Do not wait and do not go on with what depends on the answer. What you did up
to that point gets committed. The task stays in `awaiting_input`, with no
auditor and without moving on, until someone answers. Questions do not expire.

When they answer, the run resumes in the same conversation with a message like
this:

```
Ana answered your questions.

**Export.** Is the CSV generated on the spot or emailed when it is ready?
Answer: By email
```

Go on from there. If they answer while you are still running, the answer may
reach you in the middle of the run.

**Non-blocking:** go on with the `default` you declared. If the answer arrives
while you are running, it reaches you; if it arrives later, it stays in the
thread for the next run.

If the task changes stage before they answer, the blocking questions of the
stage it left are canceled and nobody resumes you.

## Approvals

`task_request_approval(label, reason)` adds an approval to the task that stops
any forward move from the current stage until a person approves or waives it.
Do not end your turn because you asked for one: the stop applies when the task
tries to leave the stage.

- `label`: who has to look, the way the team would say it ("Design",
  "Security").
- `reason`: what changed and what to look at ("I changed the tax calculation in
  `checkout/tax.ts`; check rounding with coupons").
- You can request them, not approve, waive or delete them.
- The stage may bring its own: the brief lists them under "Approvals" with
  their status. Do not request again one that is already there.

An approval someone gave goes back to pending if the code changes afterwards.
If they request changes, depending on how the stage is configured, you are
resumed with a message like this:

```
Ana asked for changes before approving "Design":

The button is covered by the footer on mobile.

Make the changes in this stage. The approval goes back to pending when you
finish, for them to review again.
```

Make those changes and nothing else. There is no need to request the approval
again.

## Comments, threads and reports

- Thulu signs each comment with your agent name.
- `task_add_comment` posts right away. `comment.md` in the output dir is
  posted when the run ends. Use one of the two for the same message, not both.
- `parent_id` with the id of any comment in the thread puts your reply in that
  thread. In a mention, it is the `THREAD_ID` you were given.
- `@name` launches that agent in the thread. A thread allows three agent
  messages in a row: the third no longer launches anyone, and nobody is
  launched until a person writes. Name another agent when you need its work,
  not to let it know something.
- To show an image or a video, upload it with `task_attach` and embed it:
  `![screenshot](/task-attachments/<id>)`.
- `task_add_report` goes to the Reports tab: it does not notify or launch
  anyone. Long evidence and detail go there; the thread is kept for the
  conversation.

## The task body

The body is built from `## ` sections: `goal`, `context`,
`definition-of-done`, `out-of-scope`, `risks`, `notes`, or a custom one with
its own `title`.

- `task_sections_get` before touching anything: it returns each section with
  its checklist items numbered from 1.
- `task_section_set` replaces one section and leaves the others intact. Do not
  rewrite the whole body with `task_update(body=...)`: you would delete what
  someone else wrote in another section.
- `task_checklist_check(slug, index, checked, note)` checks an item of the
  Definition of Done. The `note` stays on the line as evidence: "passes in the
  preview of PR #12", not "done".
- New test cases go through `task_test_case_add`, not the `test-cases`
  section.

## Splitting the work

| You want | Use |
|---|---|
| A part of the same change, which goes in the same PR | `subtask_create` |
| Separate work, with its own PR and its own pace | `task_create` |
| A task that does not start until another one finishes | `task_add_dependency` (hard) |
| To record that one relies on another, without stopping anything | `task_add_dependency` with `kind: "soft"` |

- **Subtask:** one level only. It inherits projects, board and type from the
  parent, and shares its sandbox and its branch: its work travels in the PR of
  the root task. The parent does not wait for its subtasks. `depends_on` orders
  siblings.
- **New task:** lands on the board of the task you are running. Without
  `stage`, it stays in the initial stage and nobody is launched. With `stage`,
  it launches the agent assigned to that stage. Use a column id from the flow
  the brief shows (or `board_current`).
- **Hard dependency:** the task cannot leave the initial stage until the other
  one reaches a terminal stage. It can only be set while the task is still in
  the initial stage. When the blocking task finishes, the dependent one does
  not move on by itself: someone moves it.
- A subtask with `depends_on`: create it without `stage`, so it waits for its
  sibling in the initial stage.
- `task_list` does not show subtasks.
- Writing to another board takes separate tools (`task_create_on_board`,
  `task_move_to_board`). If you do not have them, you were not given them:
  create the task on this board and say so in a comment.

## What you do not do

- Move your own task to another stage: Thulu moves it when you finish.
- `task_relaunch` on your task, `agent_stop`, `task_delete`, or editing agents
  (`agent_type_*`) or projects (`project_*`), unless the task asks for that.
- Approve your own work, or change a case's status by hand so it shows green.
