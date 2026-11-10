#!/usr/bin/env node
// Queue lines are written by the daemon (internal/task.steerPayload).
// Closing stdin seals the queue, so the daemon's later appends fail and it resumes the stage
// instead of reporting delivery to a run that stopped listening.

"use strict";

const fs = require("fs");
const path = require("path");
const { spawn } = require("child_process");

function parseArgs(argv) {
  const out = {
    queue: path.join(process.env.OCTOPUS_ROOT || "/workspace", ".steer", "queue.jsonl"),
    graceMs: 1500,
    echoInitial: false,
    echoFile: "",
    cmd: [],
  };
  for (let i = 2; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--queue") out.queue = argv[++i];
    else if (a === "--grace-ms") out.graceMs = parseInt(argv[++i], 10) || 0;
    else if (a === "--echo-initial") out.echoInitial = true;
    else if (a === "--echo-initial-file") out.echoFile = argv[++i];
    else if (a === "--") {
      out.cmd = argv.slice(i + 1);
      break;
    }
  }
  return out;
}

function readStdin() {
  return new Promise((resolve) => {
    let buf = "";
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (c) => (buf += c));
    process.stdin.on("end", () => resolve(buf));
    process.stdin.on("error", () => resolve(buf));
  });
}

const userMessage = (text) =>
  JSON.stringify({ type: "user", message: { role: "user", content: text } }) + "\n";

// The CLI does not replay injected user messages on its stream; echo them for the live view.
// engine_session_guard skips lines with octopus_injected.
const echoLine = (text, author) =>
  JSON.stringify({
    type: "user",
    octopus_injected: true,
    author: author || "user",
    message: { role: "user", content: [{ type: "text", text }] },
  }) + "\n";

const interruptLine = (id) =>
  JSON.stringify({
    type: "control_request",
    request_id: "octopus-" + id,
    request: { subtype: "interrupt" },
  }) + "\n";

const sleepSync = (ms) => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);

// Same lock dir as the daemon's append (internal/api/dockerlauncher.go, steerAppendScript). A stale
// lock is ignored after 5 s so the run can end.
function withQueueLock(queue, fn) {
  const lock = path.join(path.dirname(queue), "lock");
  const deadline = Date.now() + 5000;
  let held = false;
  for (;;) {
    try {
      fs.mkdirSync(lock);
      held = true;
      break;
    } catch (err) {
      if (err.code !== "EEXIST" || Date.now() > deadline) break;
      sleepSync(25);
    }
  }
  try {
    fn();
  } finally {
    if (held) {
      try {
        fs.rmdirSync(lock);
      } catch {
        /* already gone */
      }
    }
  }
}

const closedMarker = (queue) => path.join(path.dirname(queue), "closed");

function sealQueue(queue) {
  withQueueLock(queue, () => {
    try {
      fs.writeFileSync(closedMarker(queue), "");
    } catch {
      /* no queue dir: nothing could append either */
    }
  });
}

// A fallback run in the same container starts a second driver; the first one's seal must not lock
// the daemon out.
function unsealQueue(queue) {
  withQueueLock(queue, () => {
    try {
      fs.unlinkSync(closedMarker(queue));
    } catch {
      /* was not sealed */
    }
  });
}

async function main() {
  const opts = parseArgs(process.argv);
  if (opts.cmd.length === 0) {
    process.stderr.write('{"type":"error","error":"steer-driver: no command given"}\n');
    process.exit(3);
  }
  unsealQueue(opts.queue);
  const prompt = await readStdin();

  const child = spawn(opts.cmd[0], opts.cmd.slice(1), {
    stdio: ["pipe", "pipe", "inherit"],
  });

  let inFlight = 0;
  let closing = null;
  let stdinOpen = true;

  const send = (line) => {
    if (!stdinOpen) return false;
    try {
      child.stdin.write(line);
      return true;
    } catch {
      stdinOpen = false;
      return false;
    }
  };

  const closeStdin = () => {
    if (!stdinOpen) return;
    sealQueue(opts.queue);
    // Appends that landed before the seal belong to this session: stay open until they are
    // answered.
    drainQueue();
    if (inFlight > 0) return;
    stdinOpen = false;
    try {
      child.stdin.end();
    } catch {
      /* the child is already gone */
    }
  };

  const cancelClose = () => {
    if (closing) {
      clearTimeout(closing);
      closing = null;
    }
  };

  const maybeClose = () => {
    if (inFlight > 0 || !stdinOpen) return;
    cancelClose();
    closing = setTimeout(closeStdin, opts.graceMs);
  };

  let tail = "";
  child.stdout.on("data", (chunk) => {
    process.stdout.write(chunk);
    tail += chunk.toString("utf8");
    let nl;
    while ((nl = tail.indexOf("\n")) >= 0) {
      const line = tail.slice(0, nl).trim();
      tail = tail.slice(nl + 1);
      if (!line.startsWith("{")) continue;
      let ev;
      try {
        ev = JSON.parse(line);
      } catch {
        continue;
      }
      // An interrupted turn still emits one result (error_during_execution), so the count stays
      // balanced.
      if (ev.type === "result") {
        inFlight = Math.max(0, inFlight - 1);
        maybeClose();
      }
    }
  });

  let offset = 0;
  let pending = "";
  const drainQueue = () => {
    let size;
    try {
      size = fs.statSync(opts.queue).size;
    } catch {
      return;
    }
    if (size < offset) offset = 0;
    if (size === offset) return;
    let fd;
    try {
      fd = fs.openSync(opts.queue, "r");
    } catch {
      return;
    }
    const buf = Buffer.alloc(size - offset);
    const n = fs.readSync(fd, buf, 0, buf.length, offset);
    fs.closeSync(fd);
    offset += n;
    pending += buf.slice(0, n).toString("utf8");
    let nl;
    while ((nl = pending.indexOf("\n")) >= 0) {
      const line = pending.slice(0, nl).trim();
      pending = pending.slice(nl + 1);
      if (!line) continue;
      let msg;
      try {
        msg = JSON.parse(line);
      } catch {
        continue;
      }
      const text = (msg.text || "").trim();
      if (!text) continue;
      cancelClose();
      if (msg.interrupt) send(interruptLine(msg.id || String(Date.now())));
      process.stdout.write(echoLine(text, msg.author));
      if (send(userMessage(text))) inFlight++;
    }
  };
  const poll = setInterval(drainQueue, 400);

  if (prompt.trim()) {
    if (opts.echoFile) {
      let text = "";
      try {
        text = fs.readFileSync(opts.echoFile, "utf8").trim();
      } catch {
        /* nothing to echo */
      }
      if (text) process.stdout.write(echoLine(text));
    } else if (opts.echoInitial) process.stdout.write(echoLine(prompt.trim()));
    inFlight++;
    send(userMessage(prompt));
  } else {
    maybeClose();
  }

  child.on("close", (code, signal) => {
    clearInterval(poll);
    cancelClose();
    sealQueue(opts.queue);
    process.exit(code === null ? (signal ? 130 : 1) : code);
  });
  child.on("error", (err) => {
    clearInterval(poll);
    process.stderr.write(
      JSON.stringify({ type: "error", error: "steer-driver: " + err.message }) + "\n",
    );
    process.exit(3);
  });
}

main();
