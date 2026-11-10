#!/usr/bin/env node
// internal/usage parses `octopus-delegate: id=(dlg_[0-9a-f]+)` from stderr to attribute cost: keep
// idLine stable.
// READ_VIA=cli runs the worker here as `claude -p` because a Claude subscription token only works
// through the Claude Code client.

"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");

const MAX_PAYLOAD = 1024 * 1024; // the daemon endpoint's request limit
const TIMEOUT_MS = 150 * 1000; // above the daemon's 120 s worker timeout
const MAX_FAILURES = 3; // keep in sync with token-optimizer-hook.js
const MAX_FILES = 200; // internal/tokenopt.MaxFiles
const BOOKKEEPING_TIMEOUT_MS = 30 * 1000;

const USAGE = `usage:
  octopus-delegate read --question "<what you need to know>" --paths <file> [<file> ...]

read sends the files and the question to a cheaper model and prints only its answer.
Each call is one-shot: for a follow-up, send the paths again (they never enter your
context, so that costs this run nothing).`;

const HINT = "Hint: re-read only the lines you need with Read offset/limit (grep -n finds them), instead of the whole file.";

class Exit extends Error {
  constructor(code, message) {
    super(message);
    this.code = code;
  }
}

function stateDir(env) {
  return env.OCTOPUS_TOKEN_OPTIMIZER_STATE_DIR || path.join(env.TMPDIR || os.tmpdir(), "octopus-token-optimizer");
}

function failuresFile(env) {
  return path.join(stateDir(env), "failures");
}

function readFailures(env) {
  try {
    const n = parseInt(fs.readFileSync(failuresFile(env), "utf8").trim(), 10);
    return Number.isFinite(n) ? n : 0;
  } catch {
    return 0;
  }
}

function bumpFailures(env) {
  const n = readFailures(env) + 1;
  try {
    fs.mkdirSync(stateDir(env), { recursive: true });
    fs.writeFileSync(failuresFile(env), `${n}\n`);
  } catch {
    // The counter is a convenience; never fail the call over it.
  }
  return n;
}

function resetFailures(env) {
  try {
    fs.rmSync(failuresFile(env), { force: true });
  } catch {
    // Same as above.
  }
}

function fmtBytes(n) {
  if (n < 1024) return `${n} bytes`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function parseArgs(argv) {
  if (argv[0] !== "read") {
    throw new Exit(2, `octopus-delegate: unknown subcommand ${JSON.stringify(String(argv[0]))}; the only one is read.\n\n${USAGE}`);
  }
  const out = { cmd: argv[0], paths: [] };
  const single = { "--question": "question" };
  for (let i = 1; i < argv.length; i++) {
    let a = argv[i];
    let inline = null;
    const eq = a.indexOf("=");
    if (a.startsWith("--") && eq !== -1) {
      inline = a.slice(eq + 1);
      a = a.slice(0, eq);
    }
    if (a === "--paths") {
      if (inline !== null) out.paths.push(inline);
      while (i + 1 < argv.length && !argv[i + 1].startsWith("--")) out.paths.push(argv[++i]);
      continue;
    }
    if (single[a]) {
      const v = inline !== null ? inline : argv[++i];
      if (v === undefined) throw new Exit(2, `octopus-delegate: ${a} needs a value\n\n${USAGE}`);
      out[single[a]] = v;
      continue;
    }
    if (a === "-h" || a === "--help") throw new Exit(0, USAGE);
    throw new Exit(2, `octopus-delegate: unknown argument ${argv[i]}\n\n${USAGE}`);
  }
  return out;
}

function readText(p) {
  let st;
  try {
    st = fs.statSync(p);
  } catch {
    throw new Exit(2, `octopus-delegate: ${p}: no such file`);
  }
  if (!st.isFile()) throw new Exit(2, `octopus-delegate: ${p}: not a regular file`);
  let buf;
  try {
    buf = fs.readFileSync(p);
  } catch (e) {
    throw new Exit(2, `octopus-delegate: ${p}: cannot read it (${e.code || e.message})`);
  }
  if (buf.includes(0)) throw new Exit(2, `octopus-delegate: ${p}: looks binary (it has NUL bytes); only text files can be delegated`);
  return buf.toString("utf8");
}

function buildBody(args) {
  if (!args.question || !args.question.trim()) throw new Exit(2, `octopus-delegate: read needs --question\n\n${USAGE}`);
  if (!args.paths.length) throw new Exit(2, `octopus-delegate: read needs --paths\n\n${USAGE}`);
  const seen = new Set();
  const files = [];
  for (const p of args.paths) {
    if (seen.has(p)) continue;
    seen.add(p);
    files.push({ path: p, content: readText(p) });
  }
  if (files.length > MAX_FILES) throw new Exit(2, `octopus-delegate: at most ${MAX_FILES} files per call; split them into groups.`);
  return { kind: "read", question: args.question, files };
}

function idLine(r) {
  const cost = Number(r.cost_usd);
  return (
    `octopus-delegate: id=${r.id} model=${r.model || "unknown"} ` +
    `in=${Number(r.input_tokens) || 0} out=${Number(r.output_tokens) || 0} ` +
    `cost=${Number.isFinite(cost) ? cost.toFixed(6) : "0.000000"}`
  );
}

function checkSize(body) {
  const size = Buffer.byteLength(JSON.stringify(body));
  if (size > MAX_PAYLOAD) {
    throw new Exit(
      2,
      `octopus-delegate: the request is ${fmtBytes(size)}, over the 1 MB limit. Send fewer files per call (one question per group of files), or grep for the part you need.`,
    );
  }
}

async function call(env, endpoint, body, timeout) {
  const api = (env.OCTOPUS_API || "").replace(/\/+$/, "");
  const key = env.OCTOPUS_API_KEY || "";
  if (!api || !key) throw new Exit(1, "octopus-delegate: OCTOPUS_API or OCTOPUS_API_KEY is not set in this run, so the daemon cannot be reached.");
  let res;
  try {
    res = await fetch(`${api}/internal/token-optimizer/${endpoint}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${key}`, "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(timeout),
    });
  } catch (e) {
    if (e && (e.name === "TimeoutError" || e.name === "AbortError")) {
      throw new Exit(1, `octopus-delegate: no answer after ${Math.round(timeout / 1000)} s.`);
    }
    throw new Exit(1, `octopus-delegate: cannot reach the daemon at ${api}: ${(e && e.cause && e.cause.code) || (e && e.message) || e}`);
  }
  const text = await res.text().catch(() => "");
  let data = null;
  try {
    data = JSON.parse(text);
  } catch {
    data = null;
  }
  if (!res.ok) {
    const code = data && data.code ? ` ${data.code}` : "";
    const msg = (data && data.error) || text.trim().slice(0, 300) || res.statusText;
    throw new Exit(1, `octopus-delegate: the delegation failed (HTTP ${res.status}${code}): ${msg}`);
  }
  return data;
}

function timeoutOf(env) {
  return Number(env.OCTOPUS_DELEGATE_TIMEOUT_MS) > 0 ? Number(env.OCTOPUS_DELEGATE_TIMEOUT_MS) : TIMEOUT_MS;
}

async function viaDaemon(body, env) {
  const data = await call(env, "delegate", body, timeoutOf(env));
  if (!data || typeof data.text !== "string" || !/^dlg_[0-9a-f]+$/.test(String(data.id || ""))) {
    throw new Exit(1, "octopus-delegate: the daemon's answer is not a delegation result (no id or text).");
  }
  if (!data.text.trim()) throw new Exit(1, `octopus-delegate: the worker answered nothing (id=${data.id}).`);
  return data;
}

// Must match internal/tokenopt.UserMessage byte for byte.
function userMessage(body) {
  const attr = (s) => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  let m = "";
  for (const f of body.files) {
    m += `<file path="${attr(f.path)}">\n${f.content}${f.content.endsWith("\n") ? "" : "\n"}</file>\n\n`;
  }
  return m + `Question: ${body.question}`;
}

// --setting-sources "" keeps the run's hook settings out; --no-session-persistence because the
// adapter saves the newest transcript on disk as the run's session.
function claudeArgs(model, systemPrompt) {
  return [
    "-p",
    "--model", model,
    "--tools", "",
    "--system-prompt", systemPrompt,
    "--setting-sources", "",
    "--strict-mcp-config",
    "--mcp-config", '{"mcpServers":{}}',
    "--no-session-persistence",
    "--disable-slash-commands",
    "--output-format", "json",
  ];
}

// With the worker token, drop every ANTHROPIC_* and CLAUDE_CODE_* (except CLAUDE_CODE_DISABLE_*):
// Claude Code prefers an API key over CLAUDE_CODE_OAUTH_TOKEN.
// Blank credential vars are dropped too: the CLI treats set-but-empty as present (chat turns send
// them blank).
function workerEnv(env, maxOutputTokens) {
  const token = env.OCTOPUS_TOKEN_OPTIMIZER_WORKER_OAUTH_TOKEN || "";
  const out = { ...env };
  for (const k of Object.keys(out)) {
    const claudeVar = k.startsWith("ANTHROPIC_") || k.startsWith("CLAUDE_CODE_");
    if (k === "CLAUDECODE" || k.startsWith("OCTOPUS_TOKEN_OPTIMIZER")) delete out[k];
    else if (claudeVar && token && !k.startsWith("CLAUDE_CODE_DISABLE_")) delete out[k];
    else if (claudeVar && out[k] === "") delete out[k];
  }
  if (token) out.CLAUDE_CODE_OAUTH_TOKEN = token;
  if (Number(maxOutputTokens) > 0) out.CLAUDE_CODE_MAX_OUTPUT_TOKENS = String(Math.floor(Number(maxOutputTokens)));
  return out;
}

// Empty cwd so the repo's CLAUDE.md and git status never reach the worker.
function runClaude(args, message, env, timeout) {
  const { spawn } = require("child_process");
  const cwd = fs.mkdtempSync(path.join(env.TMPDIR || os.tmpdir(), "octopus-delegate-"));
  const started = Date.now();
  return new Promise((resolve) => {
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    let done = false;
    let exitCode = null;
    let killer = null;
    let grace = null;
    const finish = (code) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      clearTimeout(killer);
      clearTimeout(grace);
      fs.rmSync(cwd, { recursive: true, force: true });
      resolve({ code, stdout, stderr, timedOut, ms: Date.now() - started });
    };
    const child = spawn("claude", args, { cwd, env, stdio: ["pipe", "pipe", "pipe"] });
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      killer = setTimeout(() => child.kill("SIGKILL"), 5000);
    }, timeout);
    child.stdout.on("data", (c) => (stdout += c));
    child.stderr.on("data", (c) => (stderr += c));
    child.on("error", (e) => {
      stderr += String(e && e.message ? e.message : e);
      finish(127);
    });
    child.on("exit", (code, signal) => {
      exitCode = code === null ? 128 + (signal === "SIGKILL" ? 9 : 15) : code;
      // A grandchild can hold the pipes open past exit; after 2 s the exit code counts.
      grace = setTimeout(() => {
        child.stdout.destroy();
        child.stderr.destroy();
        finish(exitCode);
      }, 2000);
    });
    child.on("close", () => finish(exitCode === null ? 1 : exitCode));
    child.stdin.on("error", () => {});
    child.stdin.end(message);
  });
}

// "none": the run has no worker token; claude must not start from here either, or it would log in
// with whatever the agent's shell has.
const NO_HOST = "none";

function viaHost(socket, request) {
  const net = require("net");
  const started = Date.now();
  if (socket === NO_HOST) {
    return Promise.resolve({
      ms: 0,
      hostError: "the in-pod worker has no Claude login in this run (the daemon sent no worker token), so the worker host is not running",
      hostCode: "tokenopt.errors.worker_host",
    });
  }
  return new Promise((resolve) => {
    let raw = "";
    let done = false;
    const end = (r) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      conn.destroy();
      resolve({ ms: Date.now() - started, ...r });
    };
    const conn = net.createConnection(socket);
    // The host kills claude at timeout_ms (+5 s for SIGKILL); past that the host itself is stuck.
    const timer = setTimeout(
      () => end({ hostError: "the worker host did not answer", hostCode: "tokenopt.errors.timeout", timedOut: true }),
      request.timeout_ms + 15000,
    );
    conn.on("connect", () => conn.end(JSON.stringify(request)));
    conn.on("data", (c) => (raw += c));
    conn.on("error", (e) => end({ hostError: `the worker host is not reachable at ${socket} (${e.code || e.message})`, hostCode: "tokenopt.errors.worker_host" }));
    conn.on("end", () => {
      let v;
      try {
        v = JSON.parse(raw);
      } catch {
        return end({ hostError: "the worker host sent an unreadable answer", hostCode: "tokenopt.errors.worker_host" });
      }
      if (v.error) return end({ hostError: `the worker host refused the job: ${v.error}`, hostCode: v.code || "tokenopt.errors.worker_host" });
      end({ code: Number(v.exit_code), stdout: String(v.stdout || ""), stderr: String(v.stderr || ""), timedOut: !!v.timed_out });
    });
  });
}

function parseClaude(stdout) {
  let v;
  try {
    v = JSON.parse(stdout);
  } catch {
    return null;
  }
  if (Array.isArray(v)) v = v.filter((e) => e && e.type === "result").pop();
  return v && typeof v === "object" ? v : null;
}

async function viaClaude(body, env) {
  const prep = await call(env, "prepare", { kind: body.kind }, BOOKKEEPING_TIMEOUT_MS);
  if (!prep || !/^dlg_[0-9a-f]+$/.test(String(prep.id || "")) || typeof prep.system_prompt !== "string") {
    throw new Exit(1, "octopus-delegate: the daemon's answer to prepare has no delegation id or instructions.");
  }
  const kindModel = env[`OCTOPUS_TOKEN_OPTIMIZER_${body.kind.toUpperCase()}_MODEL`];
  const model = prep.model || kindModel || "haiku";
  const message = userMessage(body);
  const files = body.files.length;

  const timeout = timeoutOf(env);
  const socket = env.OCTOPUS_TOKEN_OPTIMIZER_WORKER_SOCKET;
  const r = socket
    ? await viaHost(socket, { model, system_prompt: prep.system_prompt, message, max_output_tokens: Number(prep.max_output_tokens) || 0, timeout_ms: timeout })
    : await runClaude(claudeArgs(model, prep.system_prompt), message, workerEnv(env, prep.max_output_tokens), timeout);
  const out = r.hostError ? null : parseClaude(r.stdout);
  const usage = (out && out.usage) || {};
  const report = {
    id: prep.id,
    status: "ok",
    error_code: "",
    files,
    request_bytes: Buffer.byteLength(message) + Buffer.byteLength(prep.system_prompt),
    response_bytes: Buffer.byteLength(r.stdout || ""),
    input_tokens:
      (Number(usage.input_tokens) || 0) + (Number(usage.cache_read_input_tokens) || 0) + (Number(usage.cache_creation_input_tokens) || 0),
    output_tokens: Number(usage.output_tokens) || 0,
    cost_usd: (out && Number(out.total_cost_usd)) || 0,
    duration_ms: r.ms,
  };

  let failure = null;
  const tail = (s) => s.trim().split("\n").slice(-3).join(" ").slice(0, 300);
  if (r.hostError) {
    report.status = r.timedOut ? "timeout" : "failed";
    report.error_code = r.hostCode;
    failure = `octopus-delegate: ${r.hostError}.`;
  } else if (r.timedOut) {
    report.status = "timeout";
    report.error_code = "tokenopt.errors.timeout";
    failure = `octopus-delegate: the worker (claude ${model}) did not answer in ${Math.round(timeout / 1000)} s.`;
  } else if (!out) {
    report.status = "failed";
    report.error_code = r.code === 127 ? "tokenopt.errors.cli_failed" : "tokenopt.errors.upstream_response";
    failure = `octopus-delegate: the worker (claude ${model}) exited ${r.code} without a result: ${tail(r.stderr || r.stdout) || "no output"}`;
  } else if (out.is_error || r.code !== 0) {
    report.status = "failed";
    report.error_code = "tokenopt.errors.cli_failed";
    failure = `octopus-delegate: the worker (claude ${model}) failed: ${tail(String(out.result || "")) || tail(r.stderr) || `exit ${r.code}`}`;
  } else if (typeof out.result !== "string" || !out.result.trim()) {
    report.status = "failed";
    report.error_code = "tokenopt.errors.empty_answer";
    failure = `octopus-delegate: the worker answered nothing (id=${prep.id}).`;
  }

  let unrecorded = null;
  try {
    await call(env, "finish", report, BOOKKEEPING_TIMEOUT_MS);
  } catch (e) {
    unrecorded = e.message;
  }
  if (failure) throw new Exit(1, failure);
  return {
    id: prep.id,
    text: out.result,
    model,
    input_tokens: report.input_tokens,
    output_tokens: report.output_tokens,
    cost_usd: report.cost_usd,
    note: unrecorded && `octopus-delegate: the answer is good but the daemon did not record it (${unrecorded}).`,
  };
}

async function run(argv, env, io) {
  const level = env.OCTOPUS_TOKEN_OPTIMIZER;
  if (argv[0] === "-h" || argv[0] === "--help" || !argv.length) {
    io.out(USAGE + "\n");
    return argv.length ? 0 : 2;
  }
  if (level !== "suggest" && level !== "enforce") {
    io.err("octopus-delegate: the token optimizer is off for this run; read the files directly.\n");
    return 3;
  }
  try {
    const args = parseArgs(argv);
    const body = buildBody(args);
    checkSize(body);
    const via = env[`OCTOPUS_TOKEN_OPTIMIZER_${body.kind.toUpperCase()}_VIA`] || "api";
    const r = via === "cli" ? await viaClaude(body, env) : await viaDaemon(body, env);
    resetFailures(env);
    io.out(r.text.endsWith("\n") ? r.text : r.text + "\n");
    io.err(idLine(r) + "\n");
    if (r.note) io.err(r.note + "\n");
    return 0;
  } catch (e) {
    if (!(e instanceof Exit)) {
      e = new Exit(1, `octopus-delegate: ${e && e.message ? e.message : e}`);
    }
    if (e.code === 0) {
      io.out(e.message + "\n");
      return 0;
    }
    io.err(e.message + "\n");
    if (e.code === 1) {
      const n = bumpFailures(env);
      io.err(HINT + "\n");
      if (level === "enforce") {
        io.err(
          n >= MAX_FAILURES
            ? `octopus-delegate: ${n} failed calls in this run; the read limit is lifted, read files directly.\n`
            : `octopus-delegate: failed call ${n} of ${MAX_FAILURES}; after ${MAX_FAILURES} the read limit is lifted for this run.\n`,
        );
      }
    }
    return e.code;
  }
}

if (require.main === module) {
  run(process.argv.slice(2), process.env, {
    out: (s) => process.stdout.write(s),
    err: (s) => process.stderr.write(s),
  }).then((code) => {
    process.exitCode = code;
  });
}

module.exports = { run, parseArgs, idLine, userMessage, claudeArgs, workerEnv, runClaude };
