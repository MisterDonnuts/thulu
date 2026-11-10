"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const http = require("http");
const net = require("net");
const os = require("os");
const path = require("path");
const { spawn, execFile } = require("child_process");

const HOST = path.join(__dirname, "token-optimizer-worker-host.js");
const CLI = path.join(__dirname, "octopus-delegate.js");
const ID_LINE = /^octopus-delegate: id=(dlg_[0-9a-f]+) model=\S+ in=\d+ out=\d+ cost=\d+\.\d{6}$/m;

// Short: a unix socket path has to fit in ~104 bytes on macOS.
const root = fs.mkdtempSync(path.join(os.tmpdir(), "tkh-"));
const F = (n) => path.join(root, n);
const bin = F("bin");
const fake = F("fake");
const work = F("work");
for (const d of [bin, fake, work]) fs.mkdirSync(d);
fs.writeFileSync(path.join(work, "a.go"), "package a\n");

fs.writeFileSync(
  path.join(bin, "claude"),
  `#!/bin/sh
model=""
prev=""
for a in "$@"; do [ "$prev" = "--model" ] && model="$a"; prev="$a"; done
env > "$FAKE_DIR/env.$model"
cat > "$FAKE_DIR/stdin.$model"
case "$model" in
ok) echo '{"type":"result","is_error":false,"result":"A is a package.","total_cost_usd":0.001,"usage":{"input_tokens":7,"cache_read_input_tokens":100,"cache_creation_input_tokens":3,"output_tokens":4}}' ;;
error) echo '{"type":"result","is_error":true,"result":"Not logged in · Please run /login","total_cost_usd":0,"usage":{}}'; echo "auth failed" >&2; exit 1 ;;
sleep) exec sleep 30 ;;
esac
`,
  { mode: 0o755 },
);

const SOCK = F("host.sock");
let host;
let hostOut = "";

function startHost(sock, extraEnv = {}) {
  const p = spawn("node", [HOST, sock], {
    env: {
      PATH: `${bin}${path.delimiter}${process.env.PATH}`,
      FAKE_DIR: fake,
      OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN: "worker-token",
      CLAUDE_CODE_OAUTH_TOKEN: "the-runs-oauth-token",
      ANTHROPIC_API_KEY: "the-runs-api-key",
      ANTHROPIC_AUTH_TOKEN: "the-runs-auth-token",
      ANTHROPIC_BASE_URL: "https://gateway.example",
      CLAUDE_CODE_USE_VERTEX: "1",
      OCTOPUS_TOKEN_OPTIMIZER: "enforce",
      OCTOPUS_TOKEN_OPTIMIZER_READ_VIA: "cli",
      CLAUDECODE: "1",
      ...extraEnv,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  p.stdout.on("data", (c) => (hostOut += c));
  return p;
}

async function waitFor(sock) {
  for (let i = 0; i < 100; i++) {
    if (fs.existsSync(sock)) return;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error("host did not listen");
}

function ask(request, sock = SOCK) {
  return new Promise((resolve, reject) => {
    let raw = "";
    const c = net.createConnection(sock, () => c.end(typeof request === "string" ? request : JSON.stringify(request)));
    c.on("data", (d) => (raw += d));
    c.on("end", () => resolve(JSON.parse(raw)));
    c.on("error", reject);
  });
}

const req = (model, extra = {}) => ({ model, system_prompt: "sp", message: "hello", max_output_tokens: 1024, timeout_ms: 5000, ...extra });

test.before(async () => {
  host = startHost(SOCK);
  await waitFor(SOCK);
});
test.after(() => {
  host.kill();
  fs.rmSync(root, { recursive: true, force: true });
});

test("socket is 0600", () => {
  assert.equal(fs.statSync(SOCK).mode & 0o777, 0o600);
});

test("round trip ok: claude runs isolated, logged in with the worker's token", async () => {
  const r = await ask(req("ok"));
  assert.equal(r.exit_code, 0);
  assert.equal(r.timed_out, false);
  assert.equal(JSON.parse(r.stdout).result, "A is a package.");
  assert.equal(fs.readFileSync(path.join(fake, "stdin.ok"), "utf8"), "hello");
  const env = fs.readFileSync(path.join(fake, "env.ok"), "utf8");
  assert.match(env, /^CLAUDE_CODE_OAUTH_TOKEN=worker-token$/m, "the worker's token, not the run's");
  assert.doesNotMatch(env, /^ANTHROPIC_/m, "the run's API key, auth token and base URL would outrank the OAuth token");
  assert.doesNotMatch(env, /^CLAUDE_CODE_USE_VERTEX=/m);
  assert.doesNotMatch(env, /the-runs-/);
  assert.match(env, /^CLAUDE_CODE_MAX_OUTPUT_TOKENS=1024$/m);
  assert.doesNotMatch(env, /^OCTOPUS_TOKEN_OPTIMIZER/m, "the worker token is not passed on under its own name either");
  assert.doesNotMatch(env, /^CLAUDECODE=/m);
  assert.match(env, /^PWD=.*octopus-delegate-/m, "runs in an empty scratch dir");
});

test("round trip is_error: exit code, stdout and the stderr tail come back", async () => {
  const r = await ask(req("error"));
  assert.equal(r.exit_code, 1);
  assert.equal(JSON.parse(r.stdout).is_error, true);
  assert.match(r.stderr, /auth failed/);
});

test("round trip timeout: claude is killed and timed_out is set", async () => {
  const t = Date.now();
  const r = await ask(req("sleep", { timeout_ms: 300 }));
  assert.equal(r.timed_out, true);
  assert.ok(Date.now() - t < 8000);
});

test("a fifth concurrent job is refused", async () => {
  const jobs = [];
  for (let i = 0; i < 5; i++) {
    jobs.push(ask(req("sleep", { timeout_ms: 1500 })));
    await new Promise((r) => setTimeout(r, 30));
  }
  const out = await Promise.all(jobs);
  const busy = out.filter((o) => o.code === "tokenopt.errors.worker_busy");
  assert.equal(busy.length, 1, JSON.stringify(out));
  assert.equal(out.filter((o) => o.timed_out).length, 4);
});

test("bad and oversized requests get an error, not a crash", async () => {
  assert.equal((await ask("not json")).code, "tokenopt.errors.worker_host");
  assert.equal((await ask({ system_prompt: "x", message: "y" })).code, "tokenopt.errors.worker_host");
  const big = await ask(req("ok", { message: "x".repeat(2 * 1024 * 1024 + 10) }));
  assert.match(big.error, /over/);
  assert.equal((await ask(req("ok"))).exit_code, 0, "still serving");
});

test("the host prints nothing on stdout", () => {
  assert.equal(hostOut, "");
});

let received = [];
let prepareModel = "ok";
const server = http.createServer((q, s) => {
  let b = "";
  q.on("data", (c) => (b += c));
  q.on("end", () => {
    const name = q.url.split("/").pop();
    received.push({ name, body: JSON.parse(b) });
    s.writeHead(200, { "Content-Type": "application/json" });
    s.end(JSON.stringify(name === "prepare" ? { id: "dlg_00000000000000dd", model: prepareModel, system_prompt: "sp", max_output_tokens: 512 } : {}));
  });
});

function delegate(env) {
  return new Promise((resolve) => {
    execFile(
      "node",
      [CLI, "read", "--question", "q", "--paths", "a.go"],
      {
        cwd: work,
        env: {
          PATH: path.dirname(process.execPath),
          OCTOPUS_API: `http://127.0.0.1:${server.address().port}`,
          OCTOPUS_API_KEY: "run-key",
          OCTOPUS_TOKEN_OPTIMIZER: "enforce",
          OCTOPUS_TOKEN_OPTIMIZER_READ_VIA: "cli",
          OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR: F("state"),
          OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET: SOCK,
          ...env,
        },
      },
      (err, stdout, stderr) => resolve({ code: err ? err.code : 0, stdout, stderr }),
    );
  });
}

test("octopus-delegate goes through the host, which has the token the caller lacks", async () => {
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  received = [];
  fs.rmSync(path.join(fake, "env.ok"), { force: true });
  const r = await delegate({});
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.stdout, "A is a package.\n");
  assert.match(r.stderr, ID_LINE);
  assert.match(r.stderr, /in=110 out=4 cost=0\.001000/);
  const env = fs.readFileSync(path.join(fake, "env.ok"), "utf8");
  assert.match(env, /^CLAUDE_CODE_OAUTH_TOKEN=worker-token$/m, "regression: the worker's token reaches the worker");
  assert.doesNotMatch(env, /^ANTHROPIC_API_KEY=/m);
  assert.match(env, /^CLAUDE_CODE_MAX_OUTPUT_TOKENS=512$/m);
  assert.match(fs.readFileSync(path.join(fake, "stdin.ok"), "utf8"), /^<file path="a\.go">\npackage a\n<\/file>\n\nQuestion: q$/);
  const fin = received.find((x) => x.name === "finish").body;
  assert.equal(fin.status, "ok");
  assert.equal(fin.input_tokens, 110);
});

test("is_error through the host: finish failed, exit 1", async () => {
  received = [];
  prepareModel = "error";
  const r = await delegate({});
  prepareModel = "ok";
  assert.equal(r.code, 1);
  assert.match(r.stderr, /Not logged in/);
  const fin = received.find((x) => x.name === "finish").body;
  assert.equal(fin.status, "failed");
  assert.equal(fin.error_code, "tokenopt.errors.cli_failed");
});

test("timeout through the host: finish timeout", async () => {
  received = [];
  prepareModel = "sleep";
  const r = await delegate({ OCTOPUS_DELEGATE_TIMEOUT_MS: "300" });
  prepareModel = "ok";
  assert.equal(r.code, 1);
  assert.equal(received.find((x) => x.name === "finish").body.status, "timeout");
});

test("a host that is not running: exit 1, finish failed, no claude started from here", async () => {
  received = [];
  const r = await delegate({ OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET: F("nobody.sock") });
  assert.equal(r.code, 1);
  assert.match(r.stderr, /worker host is not reachable/);
  const fin = received.find((x) => x.name === "finish").body;
  assert.equal(fin.status, "failed");
  assert.equal(fin.error_code, "tokenopt.errors.worker_host");
  server.closeAllConnections();
  server.close();
});
