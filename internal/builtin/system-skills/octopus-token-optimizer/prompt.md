# Token optimizer: delegate large reads

This run has the Token optimizer on (`$OCTOPUS_TOKEN_OPTIMIZER`:
`suggest` or `enforce`). `octopus-delegate` sends files and a question to a
cheaper model and returns only the answer to you: the file never enters your
context. The model is set in the agent definition; you don't pick it.

With `enforce`, reading a file larger than
`$OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES` bytes (24 KB) whole (with `Read` without
`offset`/`limit`, `cat`, `sed -n`, `awk`, `git show rev:path`…) is denied and the
reason tells you what to do. With `suggest` it is your call.

## When to delegate

- Reading a large file only to understand it: what it does, what it exports, where
  X is handled.
- A question that spans 3 or more files.
- Summarizing a large diff or log: save it to a file first
  (`git diff main > /tmp/diff.txt`) and delegate that file.

## When not to

- You are going to edit: you need the exact text. Find the area with `grep -n` and read that
  range with `Read` (`offset`/`limit`).
- Debugging, concurrency, security or architecture decisions: the cheap model
  misses details that matter there. Read it yourself.
- Small files: delegating takes 10-30 s and saves nothing.

## How

```bash
octopus-delegate read --question "Which endpoints does it register and with which middleware?" \
  --paths internal/api/router.go internal/api/middleware.go
```

- Paths are relative to where you run the command; each one has to exist and be
  text. The whole request has a 1 MB cap: if it goes over, split it into groups.
- Each call is one-shot and doesn't remember the previous one. To ask a follow-up,
  send the same `--paths` again; it costs your context nothing.
- The `octopus-delegate: id=… model=… in=… out=… cost=…` line on stderr is
  Thulu's accounting. Leave it as is.

## Before using the answer

- A summary doesn't carry line numbers or reliable values. Before editing,
  verify them by reading the range with `Read` or `grep -n`.
- If `octopus-delegate` fails (exits non-zero), don't retry the same thing: read
  only the lines you need. After 3 failures in the run the read limit
  lifts on its own.
