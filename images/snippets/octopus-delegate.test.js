"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const http = require("http");
const os = require("os");
const path = require("path");
const { execFile, spawnSync } = require("child_process");

const CLI = path.join(__dirname, "octopus-delegate.js");
const HOOK = path.join(__dirname, "token-optimizer-hook.js");
const ID_LINE = /^octopus-delegate: id=(dlg_[0-9a-f]+) model=\S+ in=\d+ out=\d+ cost=\d+\.\d{6}$/m;

const root = fs.mkdtempSync(path.join(os.tmpdir(), "tokopt-cli-"));
const F = (name) => path.join(root, name);
fs.writeFileSync(F("a.go"), "package a\n\nfunc A() int { return 1 }\n");
fs.writeFileSync(F("b.go"), "package a\n\nfunc B() int { return 2 }\n");
fs.writeFileSync(F("ref_test.go"), "package a\n\nfunc TestA(t *testing.T) {}\n");
fs.writeFileSync(F("bin.dat"), Buffer.from([0x50, 0x4b, 0x00, 0x01]));
fs.writeFileSync(F("big.txt"), "x".repeat(1100 * 1024));
fs.mkdirSync(F("adir"));

let reply = null;
let received = [];
const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    received.push({ method: req.method, url: req.url, auth: req.headers.authorization, body: body ? JSON.parse(body) : null });
    if (reply === "hang") return;
    res.writeHead(reply.status, { "Content-Type": reply.type || "application/json" });
    res.end(typeof reply.body === "string" ? reply.body : JSON.stringify(reply.body));
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

const ok = (text, extra = {}) => ({
  status: 200,
  body: { id: "dlg_0123456789abcdef", text, model: "gemini-2.5-flash", provider: "openai", input_tokens: 1234, output_tokens: 56, cost_usd: 0.00042, duration_ms: 900, ...extra },
});

function cli(args, env = {}) {
  return new Promise((resolve) => {
    execFile(
      "node",
      [CLI, ...args],
      {
        cwd: root,
        env: {
          PATH: process.env.PATH,
          OCTOPUS_API: api,
          OCTOPUS_API_KEY: "run-key",
          OCTOPUS_TOKEN_OPTIMIZER: "enforce",
          OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR: F("state"),
          ...env,
        },
      },
      (err, stdout, stderr) => resolve({ code: err ? err.code : 0, stdout, stderr }),
    );
  });
}

function failures() {
  try {
    return Number(fs.readFileSync(F("state/failures"), "utf8").trim());
  } catch {
    return 0;
  }
}

function reset() {
  received = [];
  reply = null;
  fs.rmSync(F("state"), { recursive: true, force: true });
}

test("read: sends the files and the question, prints the answer and the id line", async () => {
  reset();
  reply = ok("A returns 1 and B returns 2.");
  const r = await cli(["read", "--question", "What do A and B return?", "--paths", "a.go", "b.go"]);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(r.stdout, "A returns 1 and B returns 2.\n");
  assert.equal(received.length, 1);
  const got = received[0];
  assert.equal(got.method, "POST");
  assert.equal(got.url, "/internal/token-optimizer/delegate");
  assert.equal(got.auth, "Bearer run-key");
  assert.deepEqual(got.body, {
    kind: "read",
    question: "What do A and B return?",
    files: [
      { path: "a.go", content: fs.readFileSync(F("a.go"), "utf8") },
      { path: "b.go", content: fs.readFileSync(F("b.go"), "utf8") },
    ],
  });
  const lines = r.stderr.trim().split("\n");
  assert.equal(lines.length, 1, r.stderr);
  assert.equal(lines[0], "octopus-delegate: id=dlg_0123456789abcdef model=gemini-2.5-flash in=1234 out=56 cost=0.000420");
  assert.match(lines[0], ID_LINE);
});

test("read: a repeated path is sent once; --flag=value works", async () => {
  reset();
  reply = ok("fine");
  const r = await cli(["read", "--question=q", "--paths", "a.go", "a.go"]);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(received[0].body.files.length, 1);
});

test("write is gone: an unknown subcommand, exit 2, nothing sent", async () => {
  reset();
  for (const args of [["write", "--spec", "s", "--reference", "ref_test.go"], ["write"]]) {
    const r = await cli(args, { OCTOPUS_TOKEN_OPTIMIZER_WRITE: "1" });
    assert.equal(r.code, 2, args.join(" "));
    assert.match(r.stderr, /^octopus-delegate: unknown subcommand "write"; the only one is read\./);
    assert.match(r.stderr, /octopus-delegate read --question/);
    assert.doesNotMatch(r.stderr, /--spec|--reference|--target/, "the usage mentions only read");
  }
  assert.equal(received.length, 0);
  assert.equal(failures(), 0, "a usage error is not a worker failure");
});

test("a missing, binary or non-regular file fails loudly with exit 2 and sends nothing", async () => {
  reset();
  for (const [p, want] of [
    ["missing.go", /missing\.go: no such file/],
    ["bin.dat", /bin\.dat: looks binary/],
    ["adir", /adir: not a regular file/],
  ]) {
    const r = await cli(["read", "--question", "q", "--paths", "a.go", p]);
    assert.equal(r.code, 2, p);
    assert.match(r.stderr, want);
  }
  assert.equal(received.length, 0);
  assert.equal(failures(), 0, "usage errors are not worker failures");
});

test("usage errors exit 2", async () => {
  reset();
  for (const args of [["read", "--paths", "a.go"], ["read", "--question", "q"], ["read", "--bogus"], ["frobnicate"]]) {
    const r = await cli(args);
    assert.equal(r.code, 2, args.join(" "));
  }
  assert.equal(received.length, 0);
});

test("the payload is capped at 1 MiB", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "big.txt"]);
  assert.equal(r.code, 2);
  assert.match(r.stderr, /over the 1 MB limit/);
  assert.match(r.stderr, /fewer files/);
  assert.equal(received.length, 0);
});

test("a 403 exits 1 with the server's error and the hint, and counts a failure", async () => {
  reset();
  reply = { status: 403, body: { error: "token optimizer disabled for this run", code: "tokenopt.errors.disabled" } };
  let r = await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(r.code, 1);
  assert.match(r.stderr, /HTTP 403 tokenopt\.errors\.disabled\): token optimizer disabled for this run/);
  assert.match(r.stderr, /re-read only the lines you need with Read offset\/limit/);
  assert.doesNotMatch(r.stderr, ID_LINE);
  assert.equal(failures(), 1);
  r = await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(failures(), 2);
  assert.equal(received.length, 2, "no retries on 4xx");
});

test("a non-JSON 502 exits 1", async () => {
  reset();
  reply = { status: 502, type: "text/plain", body: "bad gateway" };
  const r = await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(r.code, 1);
  assert.match(r.stderr, /HTTP 502\): bad gateway/);
});

test("a success resets the failure counter", async () => {
  reset();
  fs.mkdirSync(F("state"), { recursive: true });
  fs.writeFileSync(F("state/failures"), "2\n");
  reply = ok("ok");
  const r = await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(failures(), 0);
});

test("after 3 failures the hook stops denying", async () => {
  reset();
  reply = { status: 504, body: { error: "worker timed out", code: "tokenopt.errors.timeout" } };
  for (let i = 0; i < 3; i++) await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(failures(), 3);
  fs.writeFileSync(F("large.txt"), "y".repeat(50000));
  const hook = spawnSync("node", [HOOK, "read"], {
    input: JSON.stringify({ tool_input: { file_path: F("large.txt") } }),
    encoding: "utf8",
    env: { PATH: process.env.PATH, OCTOPUS_TOKEN_OPTIMIZER: "enforce", OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR: F("state") },
  });
  assert.equal(hook.status, 0);
  assert.equal(hook.stdout, "", "degraded: allow");
});

test("a client-side timeout exits 1", async () => {
  reset();
  reply = "hang";
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { OCTOPUS_DELEGATE_TIMEOUT_MS: "300" });
  assert.equal(r.code, 1);
  assert.match(r.stderr, /no answer after/);
  assert.equal(failures(), 1);
});

test("an answer without a delegation id is a failure", async () => {
  reset();
  reply = { status: 200, body: { text: "hi" } };
  const r = await cli(["read", "--question", "q", "--paths", "a.go"]);
  assert.equal(r.code, 1);
});

test("off for this run: exit 3 and no request", async () => {
  reset();
  const r = await cli(["read", "--question", "q", "--paths", "a.go"], { OCTOPUS_TOKEN_OPTIMIZER: "" });
  assert.equal(r.code, 3);
  assert.match(r.stderr, /off for this run/);
  assert.equal(received.length, 0);
});
