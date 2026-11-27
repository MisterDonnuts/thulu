// Command runner. Every external tool goes through run(), so tests swap it for a fake and the
// report always knows the exact command, exit code and duration.

import { spawn } from "node:child_process";
import { accessSync, constants, existsSync, statSync } from "node:fs";
import { delimiter, dirname, join } from "node:path";

const MAX_OUTPUT = 64 * 1024 * 1024;

// Every tool runs in its own process group (detached), out of reach of whatever stops gates: a
// `timeout` around gates kills only gates' group. These are the groups still alive; gates kills
// them on its way out, and each tool's group once the tool closes, so a test server or a jest worker
// the tool left behind does not outlive it.
const liveGroups = new Set();
let exitHooks = false;

// Kills every live group from an exit, SIGTERM, SIGINT or SIGHUP. The signal handlers exit with the
// signal's code, as the default action would have.
function installExitHooks() {
  if (exitHooks || process.platform === "win32") return;
  exitHooks = true;
  process.on("exit", killLiveGroups);
  for (const [sig, n] of [
    ["SIGTERM", 15],
    ["SIGINT", 2],
    ["SIGHUP", 1],
  ]) {
    process.once(sig, () => {
      killLiveGroups();
      process.exit(128 + n);
    });
  }
}

export function killLiveGroups() {
  for (const pgid of liveGroups) killGroup(pgid);
  liveGroups.clear();
}

function killGroup(pgid) {
  try {
    process.kill(-pgid, "SIGKILL");
  } catch {
    // the group is empty
  }
}

// A tool that exits while a child it left holds its stdout open never closes; past this grace its
// group is killed so the close comes.
const EXIT_SWEEP_GRACE_MS = 2000;

// run resolves, never rejects: a missing binary comes back as {notFound: true}, a timeout as
// {timedOut: true}. code is null when the process died from a signal. With stallMs, a process that
// prints nothing and does not grow watchFile for that long is killed as stalled ({timedOut: true,
// stalled: true}): a test suite stuck on one test stops in minutes instead of at the time budget.
export function run(cmd, args = [], opts = {}) {
  const started = Date.now();
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(cmd, args, {
        cwd: opts.cwd,
        env: opts.env ? { ...process.env, ...opts.env } : process.env,
        stdio: [opts.input === undefined ? "ignore" : "pipe", "pipe", "pipe"],
        detached: process.platform !== "win32",
      });
    } catch (err) {
      resolve(result({ error: err, started }));
      return;
    }
    const grouped = process.platform !== "win32" && child.pid > 0;
    if (grouped) {
      installExitHooks();
      liveGroups.add(child.pid);
    }
    let closed = false;
    let exitSweep = null;
    const sweep = () => {
      if (!grouped) return;
      killGroup(child.pid);
      liveGroups.delete(child.pid);
    };
    const out = [];
    const errOut = [];
    let outLen = 0;
    let errLen = 0;
    let timedOut = false;
    let stalled = false;
    let timer = null;
    let stallTimer = null;
    let lastActivity = Date.now();
    let watchedSize = -1;
    child.stdout.on("data", (c) => {
      if (outLen < MAX_OUTPUT) out.push(c);
      outLen += c.length;
      lastActivity = Date.now();
    });
    child.stderr.on("data", (c) => {
      if (errLen < MAX_OUTPUT) errOut.push(c);
      errLen += c.length;
      lastActivity = Date.now();
    });
    if (opts.stallMs > 0) {
      stallTimer = setInterval(
        () => {
          const size = opts.watchFile ? fileSize(opts.watchFile) : -1;
          if (size !== watchedSize) {
            watchedSize = size;
            lastActivity = Date.now();
          }
          if (Date.now() - lastActivity >= opts.stallMs) {
            timedOut = true;
            stalled = true;
            clearInterval(stallTimer);
            killTree(child);
          }
        },
        Math.max(10, Math.min(5000, Math.floor(opts.stallMs / 4))),
      );
    }
    if (opts.input !== undefined) child.stdin.end(opts.input);
    if (opts.timeoutMs > 0) {
      timer = setTimeout(() => {
        timedOut = true;
        killTree(child);
      }, opts.timeoutMs);
    }
    child.on("error", (err) => {
      if (timer) clearTimeout(timer);
      if (stallTimer) clearInterval(stallTimer);
      if (exitSweep) clearTimeout(exitSweep);
      sweep();
      resolve(result({ error: err, started }));
    });
    child.on("exit", () => {
      if (!grouped || closed) return;
      exitSweep = setTimeout(() => {
        if (!closed) sweep();
      }, EXIT_SWEEP_GRACE_MS);
      exitSweep.unref();
    });
    child.on("close", (code, signal) => {
      closed = true;
      if (timer) clearTimeout(timer);
      if (stallTimer) clearInterval(stallTimer);
      if (exitSweep) clearTimeout(exitSweep);
      // The tool is gone; what it started in its group is not, unless this kills it.
      sweep();
      resolve(
        result({
          code,
          signal,
          timedOut,
          stalled,
          started,
          stdout: Buffer.concat(out).toString("utf8"),
          stderr: Buffer.concat(errOut).toString("utf8"),
        }),
      );
    });
  });
}

function killTree(child) {
  try {
    if (process.platform !== "win32") process.kill(-child.pid, "SIGKILL");
    else child.kill("SIGKILL");
  } catch {
    try {
      child.kill("SIGKILL");
    } catch {
      // already gone
    }
  }
}

function fileSize(p) {
  try {
    return statSync(p).size;
  } catch {
    return -1;
  }
}

function result({ code = null, signal = null, timedOut = false, stalled = false, started, stdout = "", stderr = "", error }) {
  const notFound = error?.code === "ENOENT";
  return {
    code: error ? (notFound ? 127 : 126) : code,
    signal,
    timedOut,
    stalled,
    notFound,
    error: error ? String(error.message || error) : null,
    stdout,
    stderr,
    durationMs: Date.now() - started,
  };
}

// which finds an executable on PATH, or returns null.
export function which(bin, envPath = process.env.PATH || "") {
  if (bin.includes("/")) return isExecutable(bin) ? bin : null;
  for (const dir of envPath.split(delimiter)) {
    if (!dir) continue;
    const p = join(dir, bin);
    if (isExecutable(p)) return p;
  }
  return null;
}

function isExecutable(p) {
  try {
    accessSync(p, constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

// localBin finds a project-local Node binary (node_modules/.bin) walking up from dir to stop.
export function localBin(dir, name, stop) {
  let d = dir;
  for (;;) {
    const p = join(d, "node_modules", ".bin", name);
    if (existsSync(p)) return p;
    if (d === stop) return null;
    const up = dirname(d);
    if (up === d) return null;
    d = up;
  }
}

// shellQuote renders argv as one copy-pasteable command line for the report.
export function shellQuote(cmd, args = []) {
  return [cmd, ...args]
    .map((a) => (/^[A-Za-z0-9_@%+=:,./-]+$/.test(a) ? a : `'${String(a).replace(/'/g, `'\\''`)}'`))
    .join(" ");
}
