# Refining a task

Refining means leaving a task in a state where someone else can work it
without asking again what was requested, and where a person can decide without
argument whether it is done. You define what has to be true at the end and how
it is checked. The implementation plan belongs to whoever works the task, who
will read the code with more time than you.

The bar: someone who did not see the original conversation reads the task and
knows (a) what to do and (b) how it will be evaluated.

## Before writing

1. Read the whole brief: description, discussion, any existing test cases,
   open questions.
2. Look at the code if you have it. In the initial stage of the board there
   are no repos mounted yet: refine with what the task says and leave whatever
   depends on the code as a question or as a risk.
3. Gather what you do not know and ask it in a single call to `task_ask`
   (skill `octopus-task-workflow`). A scope decision you make yourself without
   asking is a task that comes back.

## What a refined task leaves behind

Each part goes in its own section of the body, with `task_section_set`. Do not
rewrite the whole body: what the person wrote stays where it is.

| Section | What it holds |
|---|---|
| `goal` | One or two sentences with the outcome for the user. "Whoever exports a 40k-row list receives the CSV by email in under 5 minutes." |
| `context` | What you need to know that is not in plain sight: where the code lives, what was tried, the originating ticket or thread. |
| `definition-of-done` | A checklist (`- [ ]`) of conditions that are checked by looking at something: a screen, a command, a number. |
| `out-of-scope` | What someone could believe is included and is not. |
| `risks` | What can go wrong and what would give it away. |

### Definition of Done

Each item gets checked with evidence (`task_checklist_check` with a `note`),
so write it so that it can be:

- "The 40k-row export finishes without a timeout in the PR preview", not "the
  export works".
- "The `billing/` tests pass in CI", not "tests ok".
- One per condition. If an item joins two things that fail separately, they
  are two.
- Between three and eight. More than eight is usually a task that needs to be
  split.

### Test cases

The behaviors someone is going to test go in as cases, with
`task_test_case_add`, not in the body (skill `octopus-test-cases`). The
Definition of Done says what has to be true; the cases say how each behavior
is tested, step by step.

## Routing

- **Agents per stage.** `task_update(stage_assignments={...})` assigns an agent
  to each stage of the flow that needs one, with the stage id the brief shows.
  Pick them from `agent_type_list`, among those that declare that stage. A
  stage that needs an agent and has none stops the task with a system comment.
- **Tags.** `tags` are free-form. Use the ones the board already uses
  (`task_list` shows them to you) instead of inventing synonyms.
- **Reviews.** If the change touches money, permissions, customer data or a
  screen that design looks after, put it in the Definition of Done ("Design
  reviewed the new screen") and in `risks`. Do not request the approval
  yourself: an approval stops the stage in which it is requested, and in yours
  there is nothing to review yet. The agent that makes the change requests it,
  in its own stage.

## Splitting

Split when the task has pieces that can be reviewed separately or that
different agents do:

- Parts of the same change, which travel in the same PR: `subtask_create`, with
  `depends_on` between siblings if one needs another. Create without `stage`
  the ones that have to wait.
- Work that deserves its own PR or its own pace: `task_create` and, if this
  task cannot start without that one, a hard `task_add_dependency`. It can only
  be set while the task is still in the initial stage.

Each piece carries its own `goal` and its own Definition of Done. A subtask
that says "see the parent task" is not refined.

## When you finish

In `_index.md`, a short summary: what got defined, what you asked and what is
still unanswered, how you split the work. Do not move the task: if the stage
advances by itself, Thulu moves it when you finish.
