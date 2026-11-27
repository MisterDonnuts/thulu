# Production readiness

The last agent check before a person approves the push. Every earlier stage
said yes; this skill is how you find out whether they were right. The order
below goes from cheap and mechanical to reading and judgment. Do every check
even after one fails: the next round should fix everything at once.

## 1. The branch is up to date and merges cleanly

The target is the PR's base branch (`pr_view`, `baseRefName`), or the
brief's **Default branch** when there is no PR.

Fetch it first, so you compare against what the remote has now:

| `$OCTOPUS_GIT_NATIVE` | How to fetch |
|---|---|
| `1` (Kubernetes, a runner) | `git fetch origin <target>` |
| anything else (Docker) | `branch_status` from `thulu-github`: Thulu fetches `origin/<target>` for your worktree and returns `behind` and `needs_rebase` |

Then, from the repo:

```bash
git rev-list --count HEAD..origin/<target>          # commits behind
git merge-tree --write-tree --name-only --no-messages origin/<target> HEAD
```

`merge-tree` exits 0 when the merge is clean and 1 when it conflicts; after
the first line (a tree id) it lists the conflicted files. In Docker the
repo's object store is read-only and `merge-tree` fails with "insufficient
permission"; give it a temporary one:

```bash
GIT_OBJECT_DIRECTORY=$(mktemp -d) \
GIT_ALTERNATE_OBJECT_DIRECTORIES=$(git rev-parse --path-format=absolute --git-common-dir)/objects \
git merge-tree --write-tree --name-only --no-messages origin/<target> HEAD
```

Behind by one commit or more, or any conflict, is `branch_behind` for
`in_progress`. The body gives the count, the target and every conflicted file
("66 commits behind main; conflicts in cart/cart.go, cart/total.ts"). A
branch cut from an old base is the usual cause: Development rebases onto the
current target when it starts and after every send-back.

## 2. Commits follow the project's rules

Read the rules at the target, not in the diff: `git show origin/<target>:CLAUDE.md`,
the same for `AGENTS.md`, `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md`,
`commitlint.config.*`, `.gitmessage` and `.github/` workflows that lint
commits. Then list the branch's commits:

```bash
git log --format='%h%x09%an <%ae>%x09%cn <%ce>%x09%s' <base>..HEAD
git log --format='--- %h%n%B' <base>..HEAD        # bodies and trailers
```

| Rule in the project | Finding when |
|---|---|
| A single commit per change | more than one commit in `<base>..HEAD` |
| An author or an email domain | any commit with another author or email |
| No co-author, "Generated with" or tool trailers | any commit carrying one |
| A message format (Conventional Commits, a ticket prefix) | any subject that does not match |

When the rules name no author, every commit on the branch has the same
author identity, and it is one that appears as an author on the target
branch (`git log origin/<target> --format='%an <%ae>' | sort | uniq -c`). A
branch whose commits mix identities is a finding: on one real task three
commits carried the wrong email.

Each finding names the SHA and what is wrong. It is `commit_rules` for
`in_progress` when `$OCTOPUS_GIT_NATIVE` is `1`. Otherwise Development cannot
rewrite history (its `.git` is read-only): `block` with `reason:
commit_rules`.

## 3. Debug, evidence and secret files

```bash
git diff --name-status <base>...HEAD
git diff <base>...HEAD -U0 | grep '^+'
```

| Kind | Paths or lines |
|---|---|
| Debug and scratch | `*.log`, `*.orig`, `*.rej`, `*.bak`, `*.tmp`, `.DS_Store`, `tmp/`, `scratch/`, `debug*`, notebooks with outputs, a file whose name says it is a copy |
| Run output and evidence | `_index.md`, `comment.md`, `verdict.md`, `flags.yaml`, `locked-tests.txt`, `screenshots/`, `*.webm`, `*.mp4`, a `gates/` or `thulu-gates` folder, `report.json`, `*.sarif`, `coverage/`, `playwright-report/`, `test-results/` |
| Secrets | `.env*` (other than an example file), `*.pem`, `*.key`, `id_rsa*`, `*.p12`, credential JSON; added lines with a private key header, `AKIA[0-9A-Z]{16}`, `ghp_`, `github_pat_`, `xox[abp]-`, `sk-` followed by a long token, or a password or token assigned a literal |

A path the spec asks for, or one `.gitignore` at the target allows on
purpose (a fixture, a documented asset), is not a finding; say why in the
report. A debug or evidence file is `forbidden_files` for `in_progress`. A
secret is `block` with `reason: needs_decision`, also when a later commit
deleted it: it stays in the branch's history until a person rotates it.

## 4. The change matches what was agreed

Sources, in this order of authority: the answers a person gave in the task
(`task_get`, `questions`, and decisions people wrote in comments), the
approved spec (`acceptance-criteria`, `interface`, `out-of-scope`,
`definition-of-done`, `bug-behavior`), the project's rules and its skills.
Project skills live in the repo (`.claude/skills/`, `.agents/`, `skills/`,
`docs/` the rules point to); read the ones the diff touches.

One row per criterion, Definition of Done item, answer and rule touched:
`met` or `not met`, with the `file:line` that shows it. An answer the code
ignores is as much a finding as a missed criterion.

- Not met, and the fix is code: `spec_mismatch` for `in_progress`.
- The code does something nobody asked for, an answer contradicts the spec,
  or the spec cannot be met as written: `block`, `needs_decision`.

## 5. Thulu test cases ran on this head and passed

`task_test_cases` lists each case with its runs, newest first. The repo's
own suite does not count here: it may be broken on the base, and
Verification owns it.

For each case:

1. The newest run is `pass`.
2. It ran on the current head: its `ran_at` is after the head commit
   (`git show -s --format=%cI HEAD`), and the Agentic testing report names
   the same SHA as `git rev-parse HEAD`.
3. It has evidence: a video, a take, a capture or a report id.

A case whose newest run is `waived` is a known gap a person accepted: the
run's `author` is that person and its `note` is the reason. It needs no
evidence and no other authorization. List it under known gaps with the
reason and the author, and never send it back as `evidence_missing`. Thulu
refuses a waiver from an agent run, so a `waived` run always comes from a
person.

A case that is `skipped`, `blocked`, `pending`, without a run or marked "not
tested" or "not verified" in a report passes only with both a stated reason
and a person's written authorization in the task for that case: a comment or
an answer by a person, not an agent, that names it. An agent's "not
applicable" is not an authorization.

Anything else is `evidence_missing` for `testing`, one line per case: key,
what is missing.

## 6. The earlier reports hold up

The brief has the latest report of Development, Verification, AI review and
Agentic testing. For each:

- It names the head it looked at, and that is the current head.
- What it did not check is listed with a reason. Verification's
  `not_verified` entries are never a pass. A check Verification lists as
  `accepted` was accepted for the project by a person, in its quality gate
  profile, with the reason it shows: list it under known gaps, and never
  send it back or ask a person about it again. The same goes for
  pre-existing findings and quarantined tests.
- Three of its claims, picked at random, match the code when you check them
  (`file:line`, a command you run read-only).

Then across reports: test counts, coverage, the criteria each one marks met
and the findings each one closed must agree.

| What you found | Target | `reason` |
|---|---|---|
| A Verification claim the code or the artifacts contradict, or a blocking check left unverified without a reason | `verification` | `reports_inconsistent` |
| The AI review has no spec check per criterion, skipped dimensions the diff touches without saying why, or missed a defect you found in the diff | `ai_review` | `review_shallow` |
| The testing report and the test case runs disagree, or a case it calls passed has no run | `testing` | `reports_inconsistent` |
| Development's report claims a criterion or a test the code does not have | `in_progress` | `spec_mismatch` |

## 7. Nits that should not ship

Read the whole diff once more, as the person who will approve it:

- debug prints, commented-out code, leftover `thulu:stub`, dead code, unused
  imports or variables;
- a TODO or FIXME without a task, a hard-coded value the project keeps in
  config;
- user-facing text with typos, or text outside the project's i18n catalogs
  when it has them;
- a minor finding an earlier report listed and nobody fixed.

Formatting the project's formatter owns is not a nit. Each nit is
`file:line` and the fix, under `nits` for `in_progress`.

## Verdict

Write it to `verdict.md` in the output dir (format in your main prompt); the
report explains it, it does not decide it.

| Situation | `verdict` | `kick_back_to` | `reason` |
|---|---|---|---|
| Every check passes | `continue` | | |
| The branch is behind the target or conflicts | `kick_back` | `in_progress` | `branch_behind` |
| Commits break the rules, with native git | `kick_back` | `in_progress` | `commit_rules` |
| Commits break the rules, without native git | `block` | | `commit_rules` |
| Debug or evidence files in the diff | `kick_back` | `in_progress` | `forbidden_files` |
| A criterion, answer or rule not met, fixable in code | `kick_back` | `in_progress` | `spec_mismatch` |
| Nits | `kick_back` | `in_progress` | `nits` |
| Verification's report does not hold up | `kick_back` | `verification` | `reports_inconsistent` |
| The AI review was shallow | `kick_back` | `ai_review` | `review_shallow` |
| Test cases without a passing run on this head or without evidence (a waived case is a known gap, never this) | `kick_back` | `testing` | `evidence_missing` |
| The testing report and the runs disagree | `kick_back` | `testing` | `reports_inconsistent` |
| Scope, product or spec decision; a secret in the branch | `block` | | `needs_decision` |
| The same finding your last send-back named | `block` | | `repeated_kick_back` |
| A check you could not run after two attempts | `block` | | `other` |

Several findings: send the task to the earliest target (`in_progress`, then
`verification`, `ai_review`, `testing`), use the reason of the first finding
for that target, and list the others in the body under "After this round".
Every column after the target runs again anyway.

### Twice for the same reason

Before you send the task back, compare with your last send-back, which
the brief shows under "Handoff". If a finding is still there after the round that was meant to fix
it, the round did not work and another one will not either: `block` with
`reason: repeated_kick_back`, and the body names the finding, the round that
first sent it back and what is still wrong. Thulu enforces the same rule:
for `branch_behind`, `commit_rules`, `forbidden_files`, `spec_mismatch`,
`evidence_missing`, `review_shallow`, `reports_inconsistent` and `nits`, a
second send-back from this column with the same reason blocks the task
instead of moving it.

## The report

`_index.md`, written as you go:

1. Head: task id, HEAD, base, target, diff size.
2. Verdict and the findings that decided it.
3. Branch: behind count, merge result, conflicted files, commands run.
4. Commits: the table of commits with author and trailers, the rules read
   and where they came from.
5. Files: flagged paths and lines, and paths you cleared with the reason.
6. Guidelines: the row per criterion, answer and rule.
7. Test cases: key, newest run, ran on head (yes or no), evidence, and for
   each case without a run the reason and who authorized it. Then the known
   gaps: each waived case with its reason and the person who waived it.
8. Earlier reports: per report, head it looked at, claims checked and
   result, contradictions.
9. Nits.
10. After this round: findings for later columns.
11. Not checked, with the reason.
