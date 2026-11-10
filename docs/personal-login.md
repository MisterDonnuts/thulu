# Personal engine login

A person connects their own Claude account to a Docker install, and the runs
they start use that account instead of the workspace's key. The sign-in is
Anthropic's: it runs `claude auth login` inside a container of the agent
image, unmodified, and Claude Code stores what it gets in a Docker volume that
belongs to that person. Thulu never reads, copies, logs or exports that
volume.

Docker mode only. On Kubernetes the routes answer 404, the panel does not show
and the pod launcher refuses a run that asks for a login volume.

## Warning

Any agent container that starts a run for that person mounts the volume and
can read the session. The panel says so before connecting. Connecting a
personal account only makes sense on an install of your own that you control.

Using a Claude subscription this way has to comply with the terms of that
Claude account. The supported way to run Thulu is an Anthropic API key,
Amazon Bedrock or Google Vertex AI, set up as a workspace credential.

## How it connects

Settings → AI engines → "Your own engine login".

1. `POST /me/engine-logins/claude-code`: the daemon creates the volume
   `octopus-login-<install>-claude-code-<user>` if it does not exist (the
   first 8 characters of the install id, so two installs on the same Docker do
   not share a volume; if one with that name exists and does not belong to
   this install it answers 409 and leaves it alone) and starts a container
   with a terminal (`claude auth login --claudeai`, `CLAUDE_CONFIG_DIR`
   pointing at the volume). It reads the terminal until it finds the
   authorization URL and returns it. The container keeps no log
   (`LogConfig: none`).
2. The person opens the URL, approves with their account and copies the code.
3. `POST /me/engine-logins/claude-code/code`: the daemon writes the code into
   the CLI's terminal, waits for it to finish and runs
   `claude auth status --json`. The code is neither stored nor logged. The
   response carries `logged_in`, the email and the organization, nothing else.
4. If what was pasted does not look like a code, the CLI prints "Invalid code"
   and asks again. The daemon notices because the CLI wrote something and is
   still running 10 s later, and leaves the login open for another paste.
5. If the CLI rejects the code (exits with an error), or 10 minutes go by
   without a code, the container is deleted. If this attempt created the
   volume, it is deleted too: an empty volume does not count as connected.

The container starts as root only to hand the volume to `node` and drops to
that user with `gosu`, like the agent's entrypoint.

`DELETE /me/engine-logins/claude-code` runs `claude auth logout` and deletes
the volume. If a run has it mounted, Docker refuses to delete it and the
response is 409.

Only a person reaches these routes: an API key, including a run's key, gets
403. The audit log records `engine_login.connected` and
`engine_login.disconnected` with the engine, never with session data.

## Which runs use it

The daemon stores who triggered each run in `workflow_events.triggered_by`
(migration 50 of `task`), a column separate from the payload. `ListEvents`
does not read it, so it does not show in the events API or in `thulu export`
bundles.

| Origin | `triggered_by` |
|---|---|
| Moving a task, relaunching, conversing, approving or resolving a flag from the dashboard | the person (`dev-mode` on the dev session) |
| Auto-advance and continuations of that person's run | the same person, inherited through the context |
| A queued run that starts later | the person, read from the column of the `run_queued` event |
| Automations, agent mentions, API keys, reconciliation on restart | nobody |

A run uses the personal login when all three hold: a person triggered it, its
engine (the profile's if one is assigned, otherwise the agent's) has a
personal login, and that person connected it. Connected means the login
finished: while one is pending (the first one or a new one on the same volume)
it does not count, and neither does a volume left empty (the daemon restarted
mid-login). `claude auth status` decides it; the daemon runs it once and
remembers the answer until the next login or disconnect. Otherwise the run
uses the workspace profile as usual, and with no profile it fails with the
usual error.

With a personal login the dispatcher:

- removes from the env every credential and endpoint that would win over the
  stored login: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, the Bedrock and Vertex
  variables, and the LLM gateway's;
- sets `CLAUDE_CONFIG_DIR=/home/node/.claude-login` and
  `OCTOPUS_LLM_SOURCE=personal_login`;
- has the Docker launcher mount the volume at `/home/node/.claude-login`;
- admits one run per person at a time. The others wait in the queue with the
  reason `personal` and start when the previous one ends. Two CLIs refreshing
  the same token at once can invalidate each other.

The `claude-code.sh` adapter reads its state from `CLAUDE_CONFIG_DIR`. When a
run with a personal login starts, it deletes what the previous one left in the
volume (sessions, skills, todos, settings) and keeps only `.credentials.json`
and `.claude.json`, so one task does not see another's transcript.

## Cleanup

- The `dockergc` janitor never takes these volumes (label
  `octopus.personal-login`).
- `thulu uninstall` deletes them, except those of another install on the
  same Docker.
- Login containers (label `octopus.personal-login.cmd`) are deleted when the
  command ends; if the daemon dies midway, the janitor takes them like any
  other stopped ephemeral container.
- The infrastructure view counts the size of these volumes but does not list
  their names, which carry the user id.

## Adding another engine

The seam is `engine.PersonalLogin` in the engine's capabilities: configuration
variable and folder, login, status and logout commands, the status parser and
the extra variables to clear. Only `claude-code` declares it today. The
engine's shell adapter has to read its state from that folder and accept
`OCTOPUS_LLM_SOURCE=personal_login` in `engine_auth_check`.

## Known limits

- Chats and agent mentions take another path and do not use the personal
  login.
- Login containers do not mount the corporate CA (`OCTOPUS_EXTRA_CA_BUNDLE`):
  behind a proxy that intercepts TLS, the login can fail.
- An agent defined by folder with an `env_file` can put a key back in the env;
  agents stored in the database have no `env_file`.
- The login container assumes the agent image's `node` user and `gosu`.
