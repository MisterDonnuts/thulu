# thulu-github

MCP service that exposes the GitHub API to Thulu agents, with token resolution **per project** (centralized in Thulu) and tools exposed by **scope** (`read`/`write`).

## Why this and not the `gh` CLI

- **Multi-project, multi-token.** Each project has its own credential: an installation token from the GitHub App or the credential stored in the credential manager. The MCP resolves it server-side from the `task_id`. A stored credential (a PAT) never leaves the daemon: on those projects the MCP calls GitHub through the daemon's proxy (see [Projects with a stored token](#projects-with-a-stored-token-pat)).
- **Permissions per stage.** The agent type grants `thulu-github@read` on one stage and `thulu-github@write` on another (`mcp_by_stage`). The MCP filters which tools it exposes by scope.
- **Auditability.** A single point to log every interaction with GitHub.

## Build & run (dev)

```bash
docker build -t thulu-github tools/thulu-github/
docker run -d \
  --name thulu-github \
  -p 127.0.0.1:3012:3000 \
  --network octopus-agents --network-alias thulu-github \
  -e OCTOPUS_API=http://octopus:8585 \
  thulu-github
```

The server has no GitHub token of its own and does not read `GH_TOKEN` or
`GITHUB_TOKEN` as a fallback: a project with no GitHub connection fails with an
error that says so, instead of acting with whatever token an operator left on a
shared server.

Health check:

```bash
curl -s http://localhost:3012/health
# {"ok":true}
```

## Wiring from the agent

The daemon forwards `MCP_THULU_GITHUB_URL` (from its env or from `/mcps`) to
each agent. Which stages use it and with which scope is set in the agent
type's settings (MCP tab in `/agents/`, or `PUT /types/{name}/settings`):

```json
"mcp_by_stage": {
  "planning":    ["thulu-github@read"],
  "in_progress": ["thulu-github@write", "thulu-sandbox"],
  "qa":          ["thulu-github@read", "thulu-sandbox"]
}
```

`mcp-config.sh` splits the `@scope` suffix from the name and adds `?task=<id>&scope=<scope>` to the SSE URL. The `skill.md` manual in this directory is pasted into the prompt of every run that wires it.

## Scopes

| Scope | Tools |
|-------|-------|
| `read` | `pr_view`, `pr_diff`, `pr_list`, `pr_files`, `pr_comments_list`, `pr_review_threads`, `pr_reviews_list`, `pr_reviewers`, `pr_checks`, `labels_list`, `branch_status`, `workflow_list`, `workflow_view`, `run_list`, `run_view`, `run_watch`, `run_jobs`, `run_logs`, `run_artifacts`, `run_pending_deployments` |
| `write` | everything in read + `commit`, `branch_push`, `pr_comment`, `pr_review_submit`, `pr_review_comment_reply`, `pr_review_thread_resolve`, `pr_review_thread_unresolve`, `pr_reviewers_request`, `pr_reviewers_remove`, `pr_labels_add`, `pr_labels_remove`, `pr_labels_set`, `pr_assignees_add`, `pr_assignees_remove`, `pr_update`, `pr_ready_for_review`, `pr_create`, `run_rerun`, `run_cancel` |
| (none) | `run_dispatch`, `deployment_approve`, `pr_merge` |

`write` implies `read`. Calls to tools outside the scope get an explicit error.

### Reading a PR's feedback

Six `read` tools let an agent work through a review without opening GitHub:
`pr_review_threads` (inline comments per thread, over GraphQL because REST does
not say whether a thread is resolved; unresolved ones first),
`pr_comments_list` (the conversation, with `since`), `pr_reviews_list`,
`pr_reviewers` (pending requests and each reviewer's latest verdict),
`pr_checks` and `labels_list`. The usual order is `pr_review_threads` with
`state: "unresolved"` and then `pr_checks`.

`pr_checks` asks for `?permissions=checks:read`. An App installation without
"Checks: read" refuses that mint; the MCP asks for the token again without the
extra and reads CI from the commit's GitHub Actions runs, like the daemon's PR
view (`githubapp.Resolver.GetPRStatusToken`). The same happens if GitHub
answers 403/404 to the check runs. The response says which source it used
(`source: "checks" | "actions"`). If the commit statuses answer 403/404, they
show up as `statuses_unavailable` and the tool does not fail.

Lists page up to `LIST_CAP` items (300) and cut each body to `BODY_CAP_BYTES`
(8 KB) and each `diff_hunk` to `HUNK_CAP_BYTES` (2 KB).

### The three tools no scope grants

`run_dispatch` starts a deploy, `deployment_approve` unblocks one waiting for
human review and `pr_merge` merges a PR (on a branch that deploys on every
push, that is a deploy). None of them is part of `write`: they have to be named
tool by tool on the stage.

```json
"mcp_by_stage": { "release": ["thulu-github@run_view+run_watch+run_logs+run_dispatch"] }
```

The list of tools is separated by `+`, not by a comma: `MCP_TOOLS` is a CSV, so
a comma inside an entry would split it into phantom servers. The server also
understands a scope inside the list: `thulu-github@write+run_dispatch` grants
everything `write` implies plus that tool. The UI picker reads it and saves it
the same way (`web/src/lib/mcp-entry.ts`).

It is the same rule `octopus-api`'s cross-board tools follow: a capability that
must be deniable per agent is its own tool, not a parameter of a tool already
granted, because the `@` gate filters by tool name. A capability that writes
where nobody is looking, and raises no error, is granted by name, not dragged
in with the `write` every coding agent already has.

For them to be tickable in the UI picker, which probes with `scope=write`, the
server implements `?catalog=1` (`internal/mcp/probe.go`): it widens **only**
`tools/list`, never `CallTool`. Listing is not granting.

`deployment_approve` works only if the run's token is a required reviewer of the
environment. Giving it to the same agent that triggers the deploy voids the gate
the environment exists to enforce; the right default is not to grant it and to
let a person approve in GitHub.

## Acting on a review's feedback

With `write` the agent can close a review's loop without leaving the MCP: it
replies inside the thread with `pr_review_comment_reply` and, once handled,
resolves it with `pr_review_thread_resolve` (`pr_review_thread_unresolve`
reopens it). The manual tells it never to resolve a thread it did not handle.
It also manages labels (`pr_labels_add|remove|set`), reviewers
(`pr_reviewers_request|remove`), assignees (`pr_assignees_add|remove`), title,
body and base (`pr_update`, which does not expose `state`: it does not close
PRs) and draft state (`pr_ready_for_review`).

- All of them sit under "Pull requests: write" in GitHub (labels and assignees
  go through the issues API, which accepts it), so the default token is enough
  and they do not ask for `?permissions=`.
- Labels are checked against the repo's before they are sent: GitHub's
  `addLabels` and `setLabels` silently create any name that does not exist, and
  a typo would end up as a new label. An unknown name is refused with the
  closest matches.
- A `thread_id` is a GraphQL node id and does not say which repo it belongs to,
  while an installation token reaches every repo of the installation. Before
  resolving, the server reads the thread's repo and refuses if it is not
  `repo`.
- `pr_review_comment_reply` accepts the id of any comment in the thread: it
  reads the comment and replies under the first one, because GitHub does not
  accept replies to replies. If the comment belongs to another PR, it publishes
  nothing.

## Token resolution

Lookup order on each call:

1. `GET ${OCTOPUS_API}/internal/tasks/{task_id}/github-token` → the token of the project linked to the task.
2. `GET ${OCTOPUS_API}/internal/projects/{project_id}/github-token` → if `?project=` came in the URL.
3. `GET ${OCTOPUS_API}/internal/github-token?repo=owner/name` → without `?task=`
   or `?project=` (a chat), with the call's `repo`.
If none answers, every tool fails with a clear message. The server has no
fallback token.

A `409` with `"code": "api.github.errors.use_proxy"` from any of the three
means the project reaches GitHub through a stored credential; the next section
covers it.

## Projects with a stored token (PAT)

A PAT carries everything its owner gave it, on every repository the owner
reaches, and GitHub cannot narrow it. If a run received it, the agent's
container could use it with `curl` to merge or deploy past its stage grant and
its approvals. So the daemon never hands a PAT to a run key (task runs and
chats alike): the token endpoints answer `409 api.github.errors.use_proxy`, and
`/internal/credentials/resolve` refuses GitHub credentials to run keys. The
daemon's own pushes, PR status reads and webhooks keep using the PAT.

On that answer this server builds its Octokit on
`${OCTOPUS_API}/internal/github-proxy` with the caller's run key in
`X-API-Key` and no token. The daemon forwards a call to GitHub with the PAT
only after checking, on every request:

- the call is in [`github-proxy.json`](github-proxy.json): method and path for
  REST, the exact document (whitespace collapsed) and its variables for
  GraphQL, each a string, number, boolean or null. Anything else is `403`. For
  GraphQL the daemon sends the listed document and the decoded variables, not
  the caller's body;
- one of the tools that make that call is granted in the run's stage (in a
  delegated chat, the agent's `chat` stage);
- the tool's approval is approved on the task (`run_dispatch` waits for
  `deploy_prod`). The proxy knows which call it forwards, so a rerun next to
  `run_dispatch` does not wait, unlike with an App token. In a delegated chat
  `pr_merge` and `deployment_approve` are refused: they keep waiting for the
  person through `/internal/github/…`;
- the repository belongs to a project the run reaches: the task's projects, or
  any project of the workspace for a delegated chat. A project with an http(s)
  remote on another host (`gitlab.com/acme/widgets`) never matches
  `github.com/acme/widgets`. The GraphQL documents that
  act on a node id (`pr_review_thread_resolve`, `pr_ready_for_review`) name
  their repository in `X-Thulu-Repo`, and the daemon asks GitHub which
  repository the node belongs to before forwarding.

The proxy drops every caller header except `Accept`, `Content-Type` and
`X-GitHub-Api-Version`, caps the request body at 1 MiB and the answer at
16 MiB, gives GitHub 40 s, follows no redirect except a log download's signed
URL (fetched without the token) and rewrites `Link` headers so the next pages
come back through it.

`github-proxy.json` is read by this server (the GraphQL documents come from it)
and embedded in the daemon binary. Adding a GitHub call here means adding its
entry, with the tools that make it; `proxy.test.mjs` fails if an octokit method
`server.js` calls is missing or has a different route, and
`TestGithubProxyTableMatchesToolYaml` fails if an entry names a tool that is
not in `tool.yaml`.

## In a chat

A chat with no task opens the session without `?task=`. With "Act for me" the
chat's key is delegated, and the daemon gives it the token of any project in the
workspace by repository (it accepts https, `.git`, `git@github.com:owner/name`,
in any case). The token comes from the GitHub App without `contents:write`: it
is enough to read, comment, review and open PRs, not to merge. A project's
stored token (PAT) is never handed to a chat: its calls go through the daemon's
proxy, like a task run's. If the daemon refuses (the repo
does not belong to a project of the workspace, there is no App installation,
the `chat` stage does not grant the tool), the error carries its answer as is.

`commit`, `branch_push` and `branch_status` work on a task's worktree and in a
chat fail with an error that says so. `pr_create` still opens the PR, but does
not link it to any task.

`pr_merge` and `deployment_approve` do not use the chat's token: they are sent
to the daemon (`POST /internal/github/{owner}/{repo}/pulls/{n}/merge` and
`.../runs/{id}/deployment-review`), which holds them until the person the chat
acts for confirms them, and the tool returns `status:
"awaiting_confirmation"` with an `action_id`. `run_dispatch`, `run_rerun` and
`run_cancel` do not wait.

## Sessions

`GET /sse` without the run's key (`X-API-Key` or `Authorization: Bearer`)
answers 401. Every `POST /messages` has to carry the same key as its session's
handshake; otherwise, 401. The only session that opens without a key is the
picker's probe (`?catalog=1`): it lists tools and refuses every call.

With a GitHub App, the token comes with the minimum permissions: metadata and
contents read, pull requests write, Actions read. It opens and reviews PRs but
cannot merge one, because GitHub files merging under "Contents: write" and the
daemon pushes the task branch with its own token. The tools that need more ask
for them with `?permissions=`: `run_dispatch`, `run_rerun` and `run_cancel` ask
for `actions:write`; `deployment_approve` asks for `deployments:write` (GitHub
files the review of pending deployments under "Deployments: write", not under
Actions); `pr_merge` asks for `contents:write`. The App needs those permissions
granted, or the mint fails and falls back to the project's stored token, which
for a run means the proxy.

The daemon repeats the check on its side: a run's key gets `actions:write` only
if the stage grants `thulu-github` `run_rerun`, `run_cancel` (both come with
`@write`, `@admin` or the bare entry) or `run_dispatch` by name,
`deployments:write` only with `deployment_approve` by name and `contents:write`
only with `pr_merge` by name. Asking without the grant returns 403 and nothing
is minted. The key can call the endpoint without going through this MCP, so
this server's tool filter is not enough on its own.

`run_dispatch` also carries `requires_approval: deploy_prod` in `tool.yaml`.
When the stage grants it, a task run gets `actions:write` only while its task
has a `deploy_prod` approval in `approved`. Run keys cannot decide approvals
and an agent opens them pending, so only a person unlocks it. Until then the
mint answers 403 naming the approval, and the tool passes that reason on. The
token cannot tell a dispatch from a rerun, so a stage that grants `run_rerun`
next to `run_dispatch` waits for the approval for both. A delegated chat (no
task) is not held: the person it acts for is the one asking.

`commit` and `branch_push` do not ask for a token: the daemon does them. If the
commits it is about to push touch `.github/workflows/`, the daemon also asks for
`workflows:write`, but only with `OCTOPUS_GITHUB_ALLOW_WORKFLOW_WRITES=1`. Off by
default: a workflow pushed to a branch runs on the runners with the repo's
secrets. Without the opt-in, GitHub refuses the push and the error names the
variable.

## Known traps

1. **`run_logs` without `job_id` can return a zip.** GitHub serves a whole run's logs as a zip. This MCP has no unzipper, to keep the image small: it detects the `PK` header and fails with a message asking for `job_id`. For plain text, always pass `job_id`, which comes from `run_jobs({repo, run_id, failed_only: true})`.

2. **`pr_create` does not push code.** The branch has to exist on the remote first: `branch_push` uploads it, or the daemon does at the end of a run on a stage with `push_branch`. The agent cannot push from its container. The MCP only opens the PR and links it to the task.

3. **GitHub queues dispatches asynchronously.** The API answers 204 with no run id. `run_dispatch` handles it: it records the highest id **before** triggering, polls for up to 20 s afterwards, and returns the new run. It compares by id and not by `created_at` because the clock is GitHub's and has one-second resolution. If the run did not show up in time, it returns `run_id: null`; say so, do not guess.

4. **A global `express.json()` breaks SSE.** Same poison as in `thulu-browser`/`thulu-sandbox`: `SSEServerTransport` reads the raw stream. Do not add a global body parser.

5. **`network_mode: host` on the agent.** If the agent runs with host networking, use `localhost:3012` in `MCP_THULU_GITHUB_URL`. On a bridge use `host.docker.internal:3012`.

6. **`run_watch` lives against the client's tool call timeout.** It waits 120 s by default (600 at most). A 15-minute pipeline is several calls, not one long one: the server returns `timed_out: true` with the partial state and the agent calls again.

7. **`GITHUB_API_URL`** points the client at another API root (GitHub Enterprise, or a stub in tests). Empty = `api.github.com`.
