#!/usr/bin/env node
// Holds OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN so the agent never does; engine-run.sh starts
// it, then drops the token before the engine.
// Never write to stdout: in a run it is the engine's stream.
// Socket safety relies on engine-run.sh's 0700 dir; this hides the token from the agent's tools,
// not from the run's user (/proc/<pid>/environ).

"use strict";

const fs = require("fs");
const net = require("net");
const { claudeArgs, workerEnv, runClaude } = require("./octopus-delegate.js");

const MAX_ACTIVE = 4;
const MAX_REQUEST = 2 * 1024 * 1024;
const STDERR_TAIL = 4096;
const DEFAULT_TIMEOUT_MS = 150 * 1000;
const MAX_TIMEOUT_MS = 600 * 1000;

function log(msg) {
  process.stderr.write(`token-optimizer-worker-host: ${msg}\n`);
}

function validate(req) {
  if (!req || typeof req !== "object") return "the request is not a JSON object";
  if (typeof req.model !== "string" || !req.model.trim()) return "model is required";
  if (typeof req.system_prompt !== "string") return "system_prompt is required";
  if (typeof req.message !== "string" || !req.message) return "message is required";
  return null;
}

function tail(s, n) {
  const b = Buffer.from(s);
  return b.length <= n ? s : b.subarray(b.length - n).toString("utf8");
}

function serve(socket, env = process.env) {
  let active = 0;
  // The client half-closes after its request; without allowHalfOpen the server closes its side
  // before the answer is ready.
  const server = net.createServer({ allowHalfOpen: true }, (conn) => {
    let size = 0;
    const chunks = [];
    let answered = false;
    const reply = (obj) => {
      if (answered) return;
      answered = true;
      conn.end(JSON.stringify(obj));
    };
    conn.on("error", () => {});
    conn.on("data", (c) => {
      size += c.length;
      if (size > MAX_REQUEST) {
        reply({ error: `the request is over ${MAX_REQUEST} bytes`, code: "tokenopt.errors.worker_host" });
        conn.destroySoon();
        return;
      }
      chunks.push(c);
    });
    conn.on("end", async () => {
      if (answered) return;
      let req;
      try {
        req = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      } catch {
        return reply({ error: "the request is not JSON", code: "tokenopt.errors.worker_host" });
      }
      const bad = validate(req);
      if (bad) return reply({ error: bad, code: "tokenopt.errors.worker_host" });
      if (active >= MAX_ACTIVE) {
        return reply({ error: `${MAX_ACTIVE} workers are already running; try again when one finishes`, code: "tokenopt.errors.worker_busy" });
      }
      active++;
      try {
        const t = Number(req.timeout_ms) > 0 ? Math.min(Number(req.timeout_ms), MAX_TIMEOUT_MS) : DEFAULT_TIMEOUT_MS;
        const r = await runClaude(claudeArgs(req.model, req.system_prompt), req.message, workerEnv(env, req.max_output_tokens), t);
        reply({ exit_code: r.code, stdout: r.stdout, stderr: tail(r.stderr, STDERR_TAIL), timed_out: r.timedOut });
      } catch (e) {
        reply({ error: `could not run claude: ${e && e.message ? e.message : e}`, code: "tokenopt.errors.worker_host" });
      } finally {
        active--;
      }
    });
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    // umask so the socket is 0600 from creation, with no window where it is wider.
    const mask = process.umask(0o177);
    server.listen(socket, () => {
      process.umask(mask);
      server.off("error", reject);
      resolve(server);
    });
  });
}

function main() {
  const [socket, parent] = process.argv.slice(2);
  if (!socket) {
    log("usage: token-optimizer-worker-host.js <socket> [<parent pid>]");
    process.exit(2);
  }
  const cleanup = () => {
    try {
      fs.rmSync(socket, { force: true });
    } catch {
      // Gone already.
    }
  };
  for (const sig of ["SIGTERM", "SIGINT", "SIGHUP"]) {
    process.on(sig, () => {
      cleanup();
      process.exit(0);
    });
  }
  serve(socket).then(
    () => {
      const ppid = Number(parent) || 0;
      if (ppid > 1) {
        setInterval(() => {
          try {
            process.kill(ppid, 0);
          } catch {
            cleanup();
            process.exit(0);
          }
        }, 2000).unref();
      }
    },
    (e) => {
      log(`cannot listen on ${socket}: ${e.message}`);
      process.exit(1);
    },
  );
}

if (require.main === module) main();

module.exports = { serve, MAX_ACTIVE, MAX_REQUEST };
