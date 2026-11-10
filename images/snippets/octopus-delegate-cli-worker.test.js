"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const http = require("http");
const os = require("os");
const path = require("path");
const { execFile } = require("child_process");

const CLI = path.join(__dirname, "octopus-delegate.js");
const ID_LINE = /^octopus-delegate: id=(dlg_[0-9a-f]+) model=\S+ in=\d+ out=\d+ cost=\d+\.\d{6}$/m;

const root = fs.mkdtempSync(path.join(os.tmpdir(), "tokopt-cliw-"));
const F = (name) => path.join(root, name);
const work = F("work");
const bin = F("bin");
const fake = F("fake");
for (const d of [work, bin, fake]) fs.mkdirSync(d, { recursive: true });
fs.writeFileSync(path.join(work, "a.go"), "package a\n\nfunc A() int { return 1 }\n");
fs.writeFileSync(path.join(work, "b.go"), "package a\n// no newline at the end");

fs.writeFileSync(
  path.join(bin, "claude"),
  `#!/bin/sh
printf '%s\\n' "$@" > "$FAKE_DIR/args"
cat > "$FAKE_DIR/stdin"
pwd > "$FAKE_DIR/cwd"
env > "$FAKE_DIR/env"
case "$FAKE_MODE" in
ok) printf '%s\\n' "$FAKE_RESULT" ;;
error) echo '{"type":"result","subtype":"success","is_error":true,"result":"API Error: 401 invalid token","total_cost_usd":0,"usage":{"input_tokens":3,"output_tokens":0}}'; exit 1 ;;
sleep) exec sleep 30 ;;
garbage) echo "segfault, probably" >&2; exit 2 ;;
esac
`,
  { mode: 0o755 },
);

const result = (text, extra = {}) =>
  JSON.stringify({
    type: "result",
    subtype: "success",
    is_error: false,
    result: text,
    total_cost_usd: 0.0026,
    usage: { input_tokens: 10, cache_read_input_tokens: 200, cache_creation_input_tokens: 30, output_tokens: 5 },
    ...extra,
  });

let routes = {};
let received = [];
const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    const name = req.url.replace("/internal/token-optimizer/", "");
    received.push({ name, auth: req.headers.authorization, body: body ? JSON.parse(body) : null });
    const r = routes[name] || { status: 404, body: { error: "no route", code: "not_found" } };
    res.writeHead(r.status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(r.body));
  });
});

let api = "";
test.before(async () => {
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  api = `http://127.0.0.1:${server.address().port}`;
});
test.after(() => {
  server.closeAllConnections();
  server.close();
  fs.rmSync(root, { recursive: true, force: true });
});

const PREPARE = { status: 200, body: { id: "dlg_00000000000000aa", model: "claude-haiku-4-5", system_prompt: "You are a precise code analyst.", max_output_tokens: 4096 } };
const FINISH = { status: 200, body: { ok: true } };

function reset(r = {}) {
  received = [];
  routes = { prepare: PREPARE, finish: FINISH, ...r };
  for (const f of fs.readdirSync(fake)) fs.rmSync(path.join(fake, f));
  fs.rmSync(F("state"), { recursive: true, force: true });
}

function cli(args, env = {}) {
  return new Promise((resolve) => {
    execFile(
      "node",
      [CLI, ...args],
      {
        cwd: work,
        env: {
          PATH: `${bin}${path.delimiter}${process.env.PATH}`,
          OCTOPUS_API: api,
          OCTOPUS_API_KEY: "run-key",
          OCTOPUS_TOKEN_OPTIMIZER: "enforce",
          OCTOPUS_TOKEN_OPTIMIZER_THRESHOLD_BYTES: "24576",
          OCTOPUS_TOKEN_OPTIMIZER_READ_VIA: "cli",
          OCTOPUS_TOKEN_OPTIMIZER_READ_MODEL: "haiku",
          OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR: F("state"),
          CLAUDECODE: "1",
          CLAUDE_CODE_OAUTH_TOKEN: "oauth-token",
          FAKE_DIR: fake,
          FAKE_MODE: "ok",
          FAKE_RESULT: result("A returns 1."),
          ...env,
        },
      },
      (err, stdout, stderr) => resolve({ code: err ? err.code : 0, stdout, stderr }),
    );
  });
}

const fakeFile = (name) => fs.readFileSync(path.join(fake, name), "utf8");
const finishBody = () => received.find((r) => r.name === "finish").body;
const failures = () => {
  try {
    return Number(fs.readFileSync(F("state/failures"), "utf8").trim());
  } catch {
    return 0;
  }
};

test("read via cli: prepare, claude with the daemon's message, finish with summed tokens", async () => {
  reset();
  const r = await cli(["read", "--question", "What do A and B return?", "--paths", "a.go", "b.go"]);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.stdout, "A returns 1.\n");
  assert.equal(r.stderr.trim(), "octopus-delegate: id=dlg_00000000000000aa model=claude-haiku-4-5 in=240 out=5 cost=0.002600");
  assert.match(r.stderr, ID_LINE);

  assert.deepEqual(received.map((x) => x.name), ["prepare", "finish"], "the daemon never calls the worker itself");
  assert.deepEqual(received[0].body, { kind: "read" });
  assert.equal(received[0].auth, "Bearer run-key");

  const want =
    '<file path="a.go">\npackage a\n\nfunc A() int { return 1 }\n</file>\n\n' +
    '<file path="b.go">\npackage a\n// no newline at the end\n</file>\n\n' +
    "Question: What do A and B return?";
  assert.equal(fakeFile("stdin"), want);

  const args = fakeFile("args").split("\n").slice(0, -1);
  const flag = (f) => args[args.indexOf(f) + 1];
  assert.equal(args[0], "-p");
  assert.equal(flag("--model"), "claude-haiku-4-5");
  assert.equal(flag("--system-prompt"), "You are a precise code analyst.");
  assert.equal(flag("--tools"), "", "no tools");
  assert.equal(flag("--setting-sources"), "", "no settings, so no hooks");
  assert.equal(flag("--mcp-config"), '{"mcpServers":{}}');
  assert.equal(flag("--output-format"), "json");
  for (const f of ["--strict-mcp-config", "--no-session-persistence", "--disable-slash-commands"]) assert.ok(args.includes(f), f);
  assert.ok(!args.includes("--settings"));

  const env = fakeFile("env");
  assert.match(env, /^CLAUDE_CODE_OAUTH_TOKEN=oauth-token$/m, "without a worker token, the caller's credential is inherited");
  assert.match(env, /^CLAUDE_CODE_MAX_OUTPUT_TOKENS=4096$/m);
  assert.doesNotMatch(env, /^OCTOPUS_TOKEN_OPTIMIZER/m, "the worker is not a Token optimizer run");
  assert.doesNotMatch(env, /^CLAUDECODE=/m);
  const cwd = fakeFile("cwd").trim();
  assert.match(path.basename(cwd), /^octopus-delegate-/, "an empty scratch dir, never the repo (CLAUDE.md, git status)");
  assert.ok(!fs.existsSync(cwd), "the scratch dir is removed");

  const fin = finishBody();
  assert.equal(fin.id, "dlg_00000000000000aa");
  assert.equal(fin.status, "ok");
  assert.equal(fin.error_code, "");
  assert.equal(fin.files, 2);
  assert.equal(fin.input_tokens, 240, "input + cache_read + cache_creation");
  assert.equal(fin.output_tokens, 5);
  assert.equal(fin.cost_usd, 0.0026);
  assert.equal(fin.request_bytes, Buffer.byteLength(want) + Buffer.byteLength("You are a precise code analyst."));
  assert.ok(fin.response_bytes > 0);
  assert.ok(Number.isInteger(fin.duration_ms) && fin.duration_ms >= 0);
});

test("is_error from claude: finish failed, exit 1, failure counted", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { FAKE_MODE: "error" });
  assert.equal(r.code, 1);
  assert.match(r.stderr, /API Error: 401 invalid token/);
  assert.match(r.stderr, /re-read only the lines you need with Read offset\/limit/);
  assert.doesNotMatch(r.stderr, ID_LINE);
  const fin = finishBody();
  assert.equal(fin.status, "failed");
  assert.equal(fin.error_code, "tokenopt.errors.cli_failed");
  assert.equal(fin.input_tokens, 3);
  assert.equal(failures(), 1);
});

test("claude that prints no JSON: finish failed", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { FAKE_MODE: "garbage" });
  assert.equal(r.code, 1);
  assert.match(r.stderr, /exited 2 without a result: segfault, probably/);
  assert.equal(finishBody().status, "failed");
  assert.equal(finishBody().error_code, "tokenopt.errors.upstream_response");
});

test("no claude on PATH: finish failed", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { PATH: path.dirname(process.execPath) });
  assert.equal(r.code, 1);
  assert.equal(finishBody().status, "failed");
  assert.equal(finishBody().error_code, "tokenopt.errors.cli_failed");
});

test("a worker past the timeout is killed: finish timeout, exit 1", async () => {
  reset();
  const started = Date.now();
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { FAKE_MODE: "sleep", OCTOPUS_DELEGATE_TIMEOUT_MS: "500" });
  assert.equal(r.code, 1);
  assert.ok(Date.now() - started < 10000, "killed, not waited out");
  assert.match(r.stderr, /did not answer in/);
  assert.equal(finishBody().status, "timeout");
  assert.equal(finishBody().error_code, "tokenopt.errors.timeout");
  assert.equal(failures(), 1);
});

test("a 424 from prepare: exit 1, no claude, no finish", async () => {
  reset({ prepare: { status: 424, body: { error: "the workspace has no token optimizer worker", code: "tokenopt.errors.no_worker" } } });
  const r = await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(r.code, 1);
  assert.match(r.stderr, /HTTP 424 tokenopt\.errors\.no_worker\)/);
  assert.deepEqual(received.map((x) => x.name), ["prepare"]);
  assert.ok(!fs.existsSync(path.join(fake, "args")), "claude never ran");
  assert.equal(failures(), 1);
});

test("a finish that fails does not throw away a good answer", async () => {
  reset({ finish: { status: 500, body: { error: "db down", code: "internal" } } });
  const r = await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.stdout, "A returns 1.\n");
  assert.match(r.stderr, ID_LINE);
  assert.match(r.stderr, /did not record it/);
});

test("a read without READ_VIA goes to /delegate", async () => {
  reset({
    delegate: { status: 200, body: { id: "dlg_00000000000000bb", text: "from the daemon", model: "m", input_tokens: 1, output_tokens: 1, cost_usd: 0 } },
  });
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { OCTOPUS_TOKEN_OPTIMIZER_READ_VIA: "" });
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.stdout, "from the daemon\n");
  assert.deepEqual(received.map((x) => x.name), ["delegate"]);
});

test("validation is the same as the api path: a missing file sends nothing", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "missing.go"]);
  assert.equal(r.code, 2);
  assert.equal(received.length, 0);
});

test("the worker's own token: claude logs in with it, never with the run's credentials", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], {
    OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN: "worker-token",
    CLAUDE_CODE_OAUTH_TOKEN: "the-runs-oauth-token",
    ANTHROPIC_API_KEY: "the-runs-api-key",
    ANTHROPIC_AUTH_TOKEN: "the-runs-auth-token",
    ANTHROPIC_BASE_URL: "https://gateway.example",
    CLAUDE_CODE_USE_BEDROCK: "1",
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
  });
  assert.equal(r.code, 0, r.stderr);
  const env = fakeFile("env");
  assert.match(env, /^CLAUDE_CODE_OAUTH_TOKEN=worker-token$/m);
  assert.doesNotMatch(env, /^ANTHROPIC_/m, "no API key, auth token or base URL of the run");
  assert.doesNotMatch(env, /^CLAUDE_CODE_USE_BEDROCK=/m);
  assert.doesNotMatch(env, /the-runs-/, "nothing of the run's credentials");
  assert.doesNotMatch(env, /^OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN=/m);
  assert.match(env, /^CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1$/m, "the off switches stay");
  assert.match(env, /^CLAUDE_CODE_MAX_OUTPUT_TOKENS=4096$/m);
});

test("socket none (engine-run.sh had no worker token): exit 1, finish failed, no claude", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET: "none" });
  assert.equal(r.code, 1);
  assert.match(r.stderr, /no Claude login in this run/);
  assert.ok(!fs.existsSync(path.join(fake, "args")), "claude never ran");
  const fin = finishBody();
  assert.equal(fin.status, "failed");
  assert.equal(fin.error_code, "tokenopt.errors.worker_host");
  assert.equal(failures(), 1);
});
