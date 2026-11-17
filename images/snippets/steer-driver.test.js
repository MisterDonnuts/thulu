"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawn } = require("child_process");

const DRIVER = path.join(__dirname, "steer-driver.js");

// A stand-in for `claude --input-format stream-json`: it answers user messages the way the CLI
// does, folding every message that arrives within foldMs of the first into one turn, and exits on
// EOF. queuedField=false drops queued_turn_count, as older CLIs do.
const FAKE_CLI = `
const foldMs = Number(process.env.FOLD_MS);
const queuedField = process.env.QUEUED_FIELD !== "0";
let buf = "", pending = 0, timer = null;
const answer = () => {
  timer = null;
  pending = 0;
  const ev = { type: "result", subtype: "success", is_error: false, result: "ok" };
  if (queuedField) ev.queued_turn_count = 0;
  process.stdout.write(JSON.stringify(ev) + "\\n");
};
process.stdin.on("data", (c) => {
  buf += c;
  let nl;
  while ((nl = buf.indexOf("\\n")) >= 0) {
    const line = buf.slice(0, nl);
    buf = buf.slice(nl + 1);
    if (!line.includes('"type":"user"')) continue;
    pending++;
    if (!timer) timer = setTimeout(answer, foldMs);
  }
});
process.stdin.on("end", () => {
  if (timer) { clearTimeout(timer); answer(); }
  process.exit(0);
});
`;

function setup() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "steer-driver-"));
  const cli = path.join(dir, "fake-cli.js");
  fs.writeFileSync(cli, FAKE_CLI);
  const steer = path.join(dir, ".steer");
  fs.mkdirSync(steer);
  const queue = path.join(steer, "queue.jsonl");
  fs.writeFileSync(queue, "");
  return { dir, cli, queue };
}

// Runs the driver on prompt, appends steer after appendMs, and resolves with the exit code, or
// with "hung" if the driver is still running after limitMs.
function drive({ cli, queue }, env, { appendMs, limitMs }) {
  return new Promise((resolve) => {
    const child = spawn(
      process.execPath,
      [DRIVER, "--queue", queue, "--grace-ms", "100", "--", process.execPath, cli],
      { env: { ...process.env, ...env }, stdio: ["pipe", "pipe", "pipe"] },
    );
    let out = "";
    child.stdout.on("data", (c) => (out += c));
    child.stdin.end("do the task");
    setTimeout(() => {
      const line = JSON.stringify({ id: "m1", kind: "comment", text: "one more thing" }) + "\n";
      fs.appendFileSync(queue, line);
    }, appendMs);
    const limit = setTimeout(() => {
      child.kill("SIGKILL");
      resolve({ code: "hung", out });
    }, limitMs);
    child.on("close", (code) => {
      clearTimeout(limit);
      resolve({ code, out });
    });
  });
}

test("a message folded into the running turn still lets the run end", async () => {
  // The queue is polled every 400 ms; the CLI folds anything within 1.5 s into the first turn.
  const r = await drive(setup(), { FOLD_MS: "1500" }, { appendMs: 200, limitMs: 8000 });
  assert.equal(r.code, 0, "driver hung waiting for a second result");
  assert.match(r.out, /one more thing/);
  assert.equal((r.out.match(/"type":"result"/g) || []).length, 1);
});

test("a message after the first answer gets its own turn before the run ends", async () => {
  const r = await drive(setup(), { FOLD_MS: "50" }, { appendMs: 0, limitMs: 8000 });
  assert.equal(r.code, 0);
  assert.equal((r.out.match(/"type":"result"/g) || []).length, 2);
});

test("without queued_turn_count the driver counts one result per message", async () => {
  const r = await drive(setup(), { FOLD_MS: "50", QUEUED_FIELD: "0" }, { appendMs: 0, limitMs: 8000 });
  assert.equal(r.code, 0);
  assert.equal((r.out.match(/"type":"result"/g) || []).length, 2);
});
