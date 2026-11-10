# Engines

> Claude Code is the main engine. Codex and OpenCode run agents and auditors too; live messages to a running agent and the runner on your machine need Claude Code.

An engine is the CLI that drives an agent's conversation: Claude Code
(`claude-code`, the default), codex (`codex`) and OpenCode (`opencode`, see
[opencode.md](opencode.md)). The daemon never runs the CLI: the agent's
container does. So each engine has two halves, and everything specific to an
engine lives in them. Outside those two folders, Thulu compares no engine's
strings.

| Half | Where | What it does |
|---|---|---|
| Shell adapter | `images/snippets/engines/<engine>.sh` | runs the CLI, resumes or opens the session, exports it at the end, says where it looks for skills |
| Go package | `internal/engine/<engine>/` | parses what the adapter emits, declares capabilities, credentials and prices |

The agent picks its engine with the `thulu.engine` label (variable `ENGINE`);
an LLM profile can change it at launch (`applyLLMCredential`,
`internal/task/dispatcher_env.go`).

## Shell adapter

`engine-run.sh` loads it with `source` and calls `engine_run`. The agent run
(`entrypoint-base.sh`), the auditor (`auditor.sh`) and every chat turn
(`chat-turn.sh`) go through it.

### Functions

| Function | | Contract |
|---|---|---|
| `engine_run` | required | reads the prompt from stdin, runs the CLI and emits JSONL on stdout. Exits with the CLI's code (stopping at `max_turns` exits with 1, which is normal). |
| `engine_session_save <dir>` | required | writes `<dir>/id` (the session id, one line) and `<dir>/state/` (the files needed to resume). If there was no session, it does not write `id`. Never credentials, settings or skills. |
| `engine_skills_dir` | required | prints the folder where the engine discovers skills (`<name>/SKILL.md`); empty if it does not discover them. |
| `engine_auth_check` | optional | validates the credentials; message to stderr and a non-zero code if they are missing. The entrypoint calls it before doing anything. |
| `ENGINE_BIN` | optional | the CLI binary. `engine-run.sh` checks that it is in the image before calling `engine_run`. |

### Input

| Variable | What it is |
|---|---|
| stdin | the prompt: the full brief on a new run, the new message on a resume |
| `MODEL` | model for a new session. Empty: the adapter's default, which must be `Capabilities.DefaultModel` |
| `MAX_TURNS` | turn cap, if the engine has `TurnBudget` |
| `EFFORT` | reasoning effort, if it has `EffortFlag` (the dispatcher removes it otherwise) |
| `MCP_CONFIG_ARGS` | MCP flags, if it has `MCP` |
| `SESSION_ID` | the id `engine_session_save` reported last time; empty on the first run |
| `SESSION_STATE_DIR` | that run's snapshot, unpacked: `<dir>/id` and `<dir>/state/`. May not exist |
| `CONVERSATION_FILE` | markdown transcript of the previous runs, built by the daemon. May not exist |
| `RESUME_BASE_PROMPT` | the prompt a new run would have started with (identity, brief). It is prepended when a resume has to open a new session, because the message alone carries none of that |
| `STEER_ENABLED`, `STEER_QUEUE`, `STEER_GRACE_MS` | messages that arrive while the run is live, if the engine has `StreamingInput` |
| `RESUME_MESSAGE` | the person's message when there is one; with steering it shows in the feed. It is set on every resume, with or without a session: with it and no `SESSION_ID` the run is a resume whose session was lost |

### Output

JSONL on stdout. The first line comes from the adapter, not from the CLI:

```json
{"type":"octopus.session","mode":"native"}
```

| `mode` | When |
|---|---|
| `native` | the CLI resumed its own session (`claude --resume`, `codex exec resume`) |
| `rehydrated` | it could not resume and opened a new session with the transcript |
| `fresh` | new session with nothing from before: first run, or a resume with no state and no transcript |

The Go parser reads that line and puts the mode in `EventInit.SessionMode`; it
emits no event of its own. The CLI's lines follow as they are.

### Resuming a session

`snippets/engine-session.sh` does the part every engine shares. The adapter
passes it four functions:

```bash
engine_run() {
  engine_session_run <failed> <restore> <resume> <new>
}
```

| Callback | What it does |
|---|---|
| `<restore>` | copies `SESSION_STATE_DIR/state` to where the CLI looks for sessions and succeeds only if session `SESSION_ID` ended up on disk. If it was already there (a chat container that keeps its home), the one on disk wins |
| `<resume>` | runs the CLI resuming `SESSION_ID`, prompt on stdin |
| `<new>` | runs the CLI with a new session, prompt on stdin |
| `<failed> <line>` | gets the first stdout line of the resume and succeeds if it says the session could not be opened |

A CLI that does not name its model in the stream (OpenCode) announces it in the
session line: with `ENGINE_SESSION_MODEL` set, `engine_session_emit` adds
`"model"` to `{"type":"octopus.session",…}` and the parser puts it in
`EventInit.Model`.

The flow:

1. No `SESSION_ID` and no `RESUME_MESSAGE` (first run, first chat turn): emit
   `fresh` and run `<new>` with stdin. No `SESSION_ID` but `RESUME_MESSAGE`
   and `RESUME_BASE_PROMPT` set (`ResumeStage` with no session to hand over):
   go straight to step 3.
2. With `SESSION_ID` and `<restore>` succeeding: run `<resume>`. Look at the
   first line (skipping the steering echoes). If `<failed>` recognizes it, or
   the CLI wrote nothing, discard the output and go on to step 3. Otherwise,
   emit `native` before that line and pass the rest through with `cat`.
3. Build `RESUME_BASE_PROMPT` + `CONVERSATION_FILE` + the message, emit
   `rehydrated` (or `fresh` if there is no transcript) and run `<new>`. With
   steering, the echo shows only the message (`steer-driver.js
   --echo-initial-file`), not the transcript.

Detection uses shell patterns on the compact JSON: the agent image has no
`jq`.

| Engine | A failed resume |
|---|---|
| Claude Code | stderr `No conversation found with session ID: …`; first stdout line a `result` with `"is_error":true` and `"num_turns":0` |
| codex | stderr `no rollout found for thread id …`; nothing on stdout. A `{"type":"error"` or `turn.failed` before `thread.started` counts too |
| OpenCode | stderr `Session not found`; nothing on stdout, exits with 1. A `{"type":"error"` as the first line counts too |

### What each engine saves

| Engine | Session id | `state/` | Verified with |
|---|---|---|---|
| Claude Code | the name of the `.jsonl` (the `session_id` of `init`) | `<sid>.jsonl` and the `<sid>/` folder if it exists (subagents, large tool results) | Claude Code 2.1.276 |
| codex | the `thread_id` of `thread.started`, which is the uuid in the rollout's name | `sessions/YYYY/MM/DD/rollout-<date>-<id>.jsonl`, with the date folders | codex-cli 0.142.0 |
| OpenCode | the `id` of the newest session in `opencode session list` (`ses_…`, the `sessionID` of every event) | `session.json`, what `opencode export <id>` prints. Restored with `opencode import`; the SQLite database does not travel | opencode-ai 1.18.35 |

Claude Code keeps sessions in `~/.claude/projects/<cwd>/`, with every
non-alphanumeric character of the cwd replaced by `-`. The agent runs in
`/workspace`, so it is `~/.claude/projects/-workspace/`. With only the `.jsonl`
restored, `claude --resume` finds the conversation and continues it under the
same id; it also finds it under another cwd's directory. Codex does the same
with only the rollout restored, without its sqlite databases.

The run's session is the newest one on disk: a run container starts with no
sessions, and a resume writes over the file it restored.

These are never exported: `~/.claude/.credentials.json`, `settings.json`,
`~/.claude/skills`, the project memory (`projects/<cwd>/memory/`),
`~/.codex/auth.json`, `config.toml` and codex's `.sqlite` files, or
OpenCode's `auth.json`, config and `opencode.db`.

### Skills

`skills-native.sh` writes every granted skill with `enforcement: on_demand`
(or no `enforcement`) as `$(engine_skills_dir)/<name>/SKILL.md`, with `name`
and `description` in the frontmatter: the model sees the description and opens
the file when it needs it. Skills with `enforcement: always`, like
`octopus-run`, are still pasted into the prompt. If the adapter prints an
empty folder, every skill is pasted into the prompt. Whether a run takes this
path is decided by `SKILLS_MODE`, which the dispatcher resolves from
`Capabilities.Skills`.

| Engine | `engine_skills_dir` |
|---|---|
| Claude Code | `~/.claude/skills` |
| codex | `$CODEX_HOME/skills` (default `~/.codex/skills`): codex 0.142 ships its own skills there in `.system/` with the same format. `Capabilities.Skills` is `false` |
| OpenCode | `~/.octopus/opencode-skills`, which the run's config puts in `skills.paths`. OpenCode loads the `SKILL.md` with its `skill` tool when the model asks for it |

## Session snapshot

A run's container is deleted when the run ends, and the engine's session would
go with it. `snippets/session-snapshot.sh` takes it to the daemon and brings it
back. It uses `OCTOPUS_API` (the daemon URL every snippet uses) and
`X-API-Key: $OCTOPUS_API_KEY`, the run's key.

When the run ends, `_octopus_session_upload`:

1. `engine_session_save $OUTPUT_DIR/.engine-session`.
2. Writes `$OUTPUT_DIR/.session_id` with the id (empty if there was no
   session).
3. Packs `.engine-session` and sends
   `PUT $OCTOPUS_API/tasks/$TASK_ID/session-snapshot?sid=<id>` with
   `Content-Type: application/gzip`. The daemon keeps one per task-agent pair
   and answers 413 past `OCTOPUS_SESSION_SNAPSHOT_MAX_BYTES`.
4. Deletes `state/` from the run's directory, whether the upload worked or
   not. A failed upload stays in the log and the next resume starts from the
   transcript.

When a run starts with `SESSION_ID` or `RESUME_MESSAGE`,
`_octopus_session_fetch` runs before `engine_run`:

- Only with `SESSION_ID`: `GET …/session-snapshot?sid=$SESSION_ID` → unpacked
  into `SESSION_STATE_DIR=/workspace/.engine-session`.
- `GET …/conversation-transcript?agent=$AGENT_NAME` →
  `CONVERSATION_FILE=/workspace/.conversation.md`.

A 404 on either is not an error. The entrypoint saves the run's full prompt in
`RESUME_BASE_PROMPT` before swapping it for the message.

The auditor runs in its own container through `engine-run.sh`, with
`ENGINE=${AUDITOR_ENGINE:-$ENGINE}` and `MODEL=${AUDITOR_MODEL:-$MODEL}`, with
no session, MCP or steering. Of its session it keeps only the id, in
`.auditor_session_id`. The dispatcher sets `AUDITOR_ENGINE` to the engine the
container's credentials were routed to.

Each stage can name another engine (`auditor.engine_by_stage`, in the API
`auditor_engine_by_stage`). The auditor then runs as a copy of the agent on that
engine: its image, its models and that engine's default profile. The agent's
model, effort, image and bound profile do not carry over. The run key says the
same to the LLM gateway, which only sees the key: `scope_llm_agent` names the
audited agent, and `scope_llm_engine` names the chosen engine, which overrides
that agent's bound profile. The run bundle also comes from `scope_llm_agent`. A
runner refuses any engine but Claude Code. An auditor that cannot start, or that
ends without a verdict, blocks the task.

A chat turn passes `RESUME_BASE_PROMPT` with `prompts/chat.md` (or `main.md`).
In Docker the chat mounts its `~/.claude`, so it resumes natively; in
Kubernetes the pod keeps no state and, if the session is missing, the adapter
opens a new one with the chat's identity instead of failing.

## Go package

`internal/engine/<engine>` implements `engine.Engine` and registers itself in
an `init()` with `engine.Register`:

```go
type Engine interface {
	Name() string                            // "claude-code"; stable, agents pin it
	ParseStream(r io.Reader) iter.Seq[Event] // adapter stdout → normalized events
	AuthEnv(ctx context.Context) []string    // extra credential variables per turn
	Capabilities() Capabilities
}
```

### Events

| Event | Carries |
|---|---|
| `EventInit` | `Model`, `SessionID`, `SessionMode` (from the `octopus.session` line) |
| `EventAssistant` | `Usage` per message |
| `EventRateLimit` | `RateLimit` |
| `EventToolUse` | the tool called |
| `EventResult` | `Result`, `IsError`, `Outcome`, `Usage`, `CostUSD`, `DurationMs`, `NumTurns`, `StopReason` |

`Outcome` is `ok`, `max_turns` or `error`. The dispatcher decides "ran out of
turns" from `Outcome`, never from a CLI subtype. Claude Code maps
`error_max_turns` to `max_turns`; codex never reports `max_turns`.

### Capabilities

| Field | What it declares |
|---|---|
| `ResumableSessions` | the CLI resumes a session by id |
| `StreamJSON` | stdout is JSONL, parseable one line at a time |
| `StreamingInput` | the CLI keeps reading stdin while it works: enables steering |
| `EffortFlag` | accepts reasoning effort; otherwise the dispatcher removes `EFFORT` |
| `MCP` | accepts MCP servers; otherwise the dispatcher removes `MCP_TOOLS` |
| `Skills` | discovers skills on disk; decides `SKILLS_MODE=auto` |
| `Models` | suggestions for the UI, not a closed list |
| `DefaultModel` | the model the adapter runs with an empty `MODEL`. It must match the `.sh` fallback |
| `TurnBudget` | honors `MAX_TURNS`; if `false`, the UI offers no turn setting |
| `Pricing` | price per model (USD per million tokens) to estimate cost when the stream does not carry it. `Family: true` covers ids that contain the short name |
| `Label`, `Provider`, `KeyKinds` | BYOK: name in the UI, provider and credential formats with their variables |
| `APIProviders`, `ProviderEnv` | other provider APIs the CLI speaks (OpenCode: `anthropic`, `openai`) and the variable where the dispatcher tells it which one the profile names |
| `BaseURLEnv`, `OrgIDEnv`, `AuthorizationEnv` | variables for a custom endpoint, organization and bearer |
| `CloudProviders`, `CloudEnv`, `CloudGatewayEnv` | cloud variants (Bedrock, Vertex) and their env, direct or through the gateway |
| `KeyKind.OwnerOnly` | a credential only the installation's owner stores (the Claude OAuth token, for testing) |
| `KeyKind.Federated`, `FederationEnv`, `FederationAudience` | identity federation: the daemon signs a token per run and there is no key; see [workload-identity-federation.md](workload-identity-federation.md) |
| `NonessentialTrafficEnv` | env that turns off telemetry and update checks |
| `PersonalLogin` | the CLI's own login that a person connects in Docker mode: configuration folder, login, status and logout commands, and which variables to clear. See [personal-login.md](personal-login.md) |

## Adding an engine

1. **CLI in the image.** `images/docker/Dockerfile`, with a pinned version. The
   parser is tied to that version through its fixtures.
2. **Adapter.** `images/snippets/engines/<name>.sh` with `ENGINE_BIN`,
   `engine_auth_check`, `engine_run` (on top of `engine_session_run`),
   `engine_session_save` and `engine_skills_dir`. Before writing `<failed>`,
   try by hand what the CLI prints when resuming an id that does not exist and
   what it needs on disk to resume one that does.
3. **Messages.** What the adapter prints goes through `msg_en` (logs) or `msg`
   (prompt) with keys in `internal/i18n/shell/locales/{en,es}/agent.json`;
   then `go test ./internal/i18n/shell -update`.
4. **Go package.** `internal/engine/<name>/` with `Engine`, `ParseStream`
   (reading `octopus.session` and mapping `Outcome`) and complete
   `Capabilities`, including `DefaultModel` and its row in `Pricing`.
5. **Registration.** A blank import of the package where the daemon loads
   engines (currently `internal/usage`), `engine.Replace` in
   `internal/daemon/llm_gateway.go` and a blank import in
   `internal/engine/conformance_test.go`, with its row in `isolationByEngine`.
6. **Fixtures.** `internal/engine/testdata/<name>/ok.jsonl` and `error.jsonl`,
   and `max_turns.jsonl` if it has `TurnBudget`: the real stdout of a run, as
   `session.log` stores it, starting with the `octopus.session` line.
7. **Adapter tests.** In `internal/agentscripts/enginesession_test.go`, a fake
   CLI that answers like the real one: new run, resume from snapshot, failed
   resume that falls back to the transcript, and `engine_session_save` without
   credentials.
8. `make check-ci`.

## Conformance test

`internal/engine/conformance_test.go`, for every registered engine:

- `images/snippets/engines/<name>.sh` exists and defines `engine_run`,
  `engine_session_save` and `engine_skills_dir`;
- every `.sh` in that folder has its engine registered in Go;
- `ParseStream` over each fixture yields an `EventInit` with `SessionID` and
  `SessionMode`, exactly one `EventResult`, and the `Outcome` named by the
  file, consistent with `IsError`;
- `DefaultModel` is not empty and has a price in `Pricing`;
- isolated config: the adapter has the switches that leave the run with the
  config Thulu writes, with no MCP servers, plugins, keys or instructions
  from the user's global config, an org's defaults or the project's repo.
  Each engine declares its own in `isolationByEngine`; an engine with no row
  fails. Known gaps are reported as a skip with their description:
  claude-code without `--strict-mcp-config`, and codex, which runs with
  whatever `$CODEX_HOME/config.toml` holds and the `AGENTS.md` files it finds.

The adapter's behavior is covered by `internal/agentscripts`
(`enginesession_test.go`, `sessionsnapshot_test.go`), which run the real `.sh`
files with fake CLIs and a fake daemon. The codex cases need bash 4
(`mapfile`) and are skipped on macOS.
