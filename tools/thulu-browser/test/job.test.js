import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, readdirSync } from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";

import { chromiumMissing } from "./chromium.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));

async function missing() {
  const reason = await chromiumMissing();
  if (reason) return reason;
  try { execFileSync(process.env.FFMPEG_PATH || "ffmpeg", ["-version"], { stdio: "ignore" }); } catch { return "ffmpeg is not installed"; }
  return false;
}
const skip = await missing();

const KEY = "oct_run_batch_key_123";
const PASSWORD = "s3cret-pass-1";

const page = `<!doctype html><html lang="en"><title>Shop</title>
<body style="font:16px sans-serif;padding:32px">
  <h1>Sign in</h1>
  <label for="email">Email</label> <input id="email">
  <label for="pw">Password</label> <input id="pw" type="password">
  <button id="go" onclick="document.querySelector('#greeting').textContent =
    document.querySelector('#pw').value === '${PASSWORD}' ? 'Hello, QA' : 'Wrong password'">Sign in</button>
  <p id="greeting"></p>
  <ul><li>One</li><li>Two</li></ul>
  <a id="leave" href="https://elsewhere.test/">Leave</a>
</body></html>`;

function serve(handler) {
  return new Promise((resolve) => {
    const s = http.createServer(handler).listen(0, "127.0.0.1", () => resolve(s));
  });
}
const origin = (s) => `http://127.0.0.1:${s.address().port}`;

const daemon = {
  queues: new Map(),
  nextCalls: new Map(),
  failNext: new Map(),
  refuseUploads: new Set(),
  artifacts: [],
  results: new Map(),
  bodies: [],
  keys: new Set(),
};

function multipart(body) {
  const s = body.toString("latin1");
  return {
    kind: /name="kind"\r\n\r\n([^\r]*)\r\n/.exec(s)?.[1],
    filename: /filename="([^"]+)"/.exec(s)?.[1],
    type: /name="file"[^\r]*\r\nContent-Type: ([^\r\n]+)/i.exec(s)?.[1],
  };
}

function daemonHandler(req, res) {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = Buffer.concat(chunks);
    daemon.keys.add(req.headers["x-api-key"]);
    daemon.bodies.push(body.toString("latin1"));
    res.setHeader("content-type", "application/json");
    let m;
    if ((m = /^\/evidence\/jobs\/([^/]+)\/next$/.exec(req.url)) && req.method === "POST") {
      const batch = m[1];
      daemon.nextCalls.set(batch, (daemon.nextCalls.get(batch) ?? 0) + 1);
      const fail = daemon.failNext.get(batch)?.shift();
      if (fail) { res.statusCode = fail; return res.end(JSON.stringify({ error: "nope" })); }
      const job = daemon.queues.get(batch)?.shift();
      if (!job) { res.statusCode = 204; return res.end(); }
      return res.end(JSON.stringify(job));
    }
    if ((m = /^\/evidence\/jobs\/([^/]+)\/runs\/([^/]+)\/artifacts$/.exec(req.url)) && req.method === "POST") {
      const run = m[2];
      if (daemon.refuseUploads.has(run)) { res.statusCode = 413; return res.end(JSON.stringify({ error: "quota" })); }
      const id = `art${daemon.artifacts.length + 1}`;
      daemon.artifacts.push({ run, id, ...multipart(body), bytes: body.length });
      res.statusCode = 201;
      return res.end(JSON.stringify({ id }));
    }
    if ((m = /^\/evidence\/jobs\/([^/]+)\/runs\/([^/]+)\/result$/.exec(req.url)) && req.method === "POST") {
      daemon.results.set(m[2], JSON.parse(body.toString()));
      return res.end(JSON.stringify({ ok: true }));
    }
    res.statusCode = 404;
    res.end("{}");
  });
}

let app, api, job, logs;

const baseTake = (steps, extra = {}) => ({ version: 1, title: "Login", base_url: "https://prod.example.com",
  viewport: { width: 800, height: 500 }, steps, ...extra });

const login = [
  { kind: "goto", url: "/", label: "Open the shop" },
  { kind: "fill", target: "#email", value: "qa@example.com", label: "Type the test email" },
  { kind: "fill", target: "#pw", secret: "QA_PASSWORD", label: "Type the password" },
  { kind: "click", target: "#go", label: "Sign in" },
];

const jobFor = (id, take, extra = {}) => ({
  run: { id, case_key: `EV-${id}`, title: take?.title ?? "broken" },
  take,
  base_url: origin(app),
  allowed_hosts: [],
  secrets: { QA_PASSWORD: PASSWORD },
  ...extra,
});

// Test files run in parallel and share /tmp, so this one renders into its own TMPDIR.
let workRoot, exitCode;

before(async () => {
  if (skip) return;
  app = await serve((_q, res) => { res.setHeader("content-type", "text/html"); res.end(page); });
  api = await serve(daemonHandler);
  job = await import("../evidence/job.js");

  daemon.queues.set("b1", [
    jobFor("pass", baseTake([
      ...login,
      { kind: "verify", case: "EV-1", expected: "Hello, QA", check: "text", target: "#greeting", label: "Greets me" },
      { kind: "verify", case: "EV-1", expected: "2", check: "count", target: "li", label: "Two items" },
      { kind: "shot", label: "The greeting", shot: { name: "greeting", spotlight: [{ target: "#greeting", note: "hi" }] } },
    ])),
    jobFor("fail", baseTake([
      ...login,
      { kind: "verify", case: "EV-2", expected: "Goodbye", check: "text", target: "#greeting", timeout_ms: 300,
        label: "Says goodbye" },
    ])),
    jobFor("blocked", baseTake([
      { kind: "goto", url: "/" },
      { kind: "verify", case: "EV-3", expected: "Sign in", check: "text", target: "h1", label: "Heading" },
      { kind: "click", target: "#leave", label: "Leave the site" },
      { kind: "verify", case: "EV-3", expected: "Hello", check: "text", target: "#greeting", label: "Never reached" },
    ])),
    jobFor("invalid", { version: 1, title: "Broken", steps: [{ kind: "fly" }] }),
    jobFor("effects", baseTake([
      { kind: "goto", url: "/" },
      { kind: "click", target: "#go", side_effect: true, label: "Pay" },
      { kind: "verify", case: "EV-5", expected: "Hello, QA", check: "text", target: "#greeting" },
    ])),
    jobFor("quota", baseTake([
      { kind: "goto", url: "/" },
      { kind: "verify", case: "EV-6", expected: "Sign in", check: "text", target: "h1", label: "Heading" },
    ])),
  ]);
  daemon.refuseUploads.add("quota");
  workRoot = mkdtempSync(path.join(os.tmpdir(), "job-test-"));
  process.env.TMPDIR = workRoot;
  logs = [];
  exitCode = await job.runJob({ api: origin(api), key: KEY, batch: "b1", pace: 0.3, retryDelays: [10, 10, 10],
    log: (l) => logs.push(l) });
});

after(async () => {
  if (skip) return;
  const { closeBrowser } = await import("../lib/browser.js");
  await closeBrowser();
  for (const s of [app, api]) s.close();
});

const artifactsOf = (run) => daemon.artifacts.filter((a) => a.run === run);

test("the batch runs to the end and exits 0 when the daemon answers 204", { skip }, () => {
  assert.equal(exitCode, 0);
  assert.equal(daemon.nextCalls.get("b1"), 7, "six runs, then the 204");
  assert.deepEqual([...daemon.results.keys()].sort(), ["blocked", "effects", "fail", "invalid", "pass", "quota"]);
  assert.deepEqual([...daemon.keys], [KEY], "every request carries the batch key, and only it");
});

test("a passing take uploads its files and posts pass with its checks", { skip }, () => {
  const r = daemon.results.get("pass");
  assert.equal(r.status, "pass");
  assert.equal(r.error, "");
  assert.deepEqual(r.checks, [
    { label: "Greets me", expected: "Hello, QA", observed: "Hello, QA", pass: true, measured: true },
    { label: "Two items", expected: "2", observed: "2", pass: true, measured: true },
  ], "the password reached the fill step: the page greets only the right one");
  assert.deepEqual(r.failures, []);
  assert.ok(r.duration_ms > 0);
  const files = artifactsOf("pass");
  const byKind = (k) => files.filter((a) => a.kind === k);
  assert.deepEqual(files.map((a) => a.kind).sort(), ["chapters", "poster", "shot", "take", "video"]);
  assert.equal(byKind("video")[0].type, "video/mp4");
  assert.equal(byKind("take")[0].type, "application/json");
  assert.equal(byKind("chapters")[0].type, "text/vtt");
  assert.equal(byKind("shot")[0].type, "image/png");
  assert.equal(byKind("poster")[0].type, "image/jpeg", "the poster is a frame of the video");
  assert.equal(r.video_id, byKind("video")[0].id);
  assert.equal(r.take_id, byKind("take")[0].id);
  assert.equal(r.chapters_id, byKind("chapters")[0].id);
  assert.equal(r.poster_id, byKind("poster")[0].id);
  assert.deepEqual(r.shot_ids, [byKind("shot")[0].id]);
  assert.match(r.report_md, new RegExp(`!\\[login\\.mp4\\]\\(/evidence/artifacts/${r.video_id}\\)`));
  assert.match(r.report_md, new RegExp(`\\(/evidence/artifacts/${r.shot_ids[0]}\\)`));
  assert.doesNotMatch(r.report_md, /task-attachments/);
  assert.match(r.report_md, /2\/2 checks pass/);
});

test("a failing check posts fail", { skip }, () => {
  const r = daemon.results.get("fail");
  assert.equal(r.status, "fail");
  assert.equal(r.checks.length, 1);
  assert.equal(r.checks[0].pass, false);
  assert.equal(r.checks[0].observed, "Hello, QA");
  assert.ok(r.video_id);
});

test("a step that fails before a planned check posts blocked", { skip }, () => {
  const r = daemon.results.get("blocked");
  assert.equal(r.status, "blocked");
  assert.deepEqual(r.checks.map((c) => [c.label, c.pass]), [["Heading", true]], "the check after the failure never ran");
  assert.equal(r.failures.length, 1);
  assert.equal(r.failures[0].step, 2);
  assert.equal(r.failures[0].kind, "click");
  assert.equal(r.failures[0].label, "Leave the site");
  assert.match(r.failures[0].error, /elsewhere\.test/);
});

test("an invalid take posts error with the reason, and uploads nothing", { skip }, () => {
  const r = daemon.results.get("invalid");
  assert.equal(r.status, "error");
  assert.match(r.error, /take/);
  assert.match(r.error, /base_url/);
  assert.deepEqual(artifactsOf("invalid"), []);
  assert.deepEqual([r.video_id, r.poster_id, r.shot_ids, r.checks], ["", "", [], []]);
});

test("a take with side-effect steps is refused with error, never replayed", { skip }, () => {
  const r = daemon.results.get("effects");
  assert.equal(r.status, "error");
  assert.match(r.error, /changes data/);
  assert.match(r.error, /Pay/);
  assert.deepEqual(artifactsOf("effects"), []);
});

test("files the daemon refuses are left out and the result still lands", { skip }, () => {
  const r = daemon.results.get("quota");
  assert.equal(r.status, "pass");
  assert.deepEqual([r.video_id, r.take_id, r.chapters_id, r.poster_id, r.shot_ids], ["", "", "", "", []]);
  assert.ok(logs.some((l) => /refused video/.test(l)));
});

test("the test login value and the key never leave the browser", { skip }, () => {
  for (const b of daemon.bodies) {
    assert.ok(!b.includes(PASSWORD), "a request body carries the password");
    assert.ok(!b.includes(KEY), "a request body carries the key");
  }
  for (const l of logs) {
    assert.ok(!l.includes(PASSWORD) && !l.includes(KEY), `a log line leaks: ${l}`);
  }
  const take = daemon.bodies.find((b) => b.includes('"secret": "QA_PASSWORD"'));
  assert.ok(take, "the uploaded take keeps the secret's name");
});

test("each run's work files are deleted once uploaded", { skip }, () => {
  assert.deepEqual(readdirSync(workRoot).filter((n) => n.startsWith("evidence-")), []);
});

test("a 5xx or a refused connection on next is retried", { skip }, async () => {
  daemon.failNext.set("b-retry", [503, 502]);
  const code = await job.runJob({ api: origin(api), key: KEY, batch: "b-retry", retryDelays: [5, 5, 5], log: () => {} });
  assert.equal(code, 0);
  assert.equal(daemon.nextCalls.get("b-retry"), 3);

  let calls = 0;
  const flaky = async (url, init) => {
    calls++;
    if (calls < 3) throw new TypeError("fetch failed", { cause: { code: "ECONNREFUSED" } });
    return fetch(url, init);
  };
  const client = job.runnerClient({ api: origin(api), key: KEY, fetch: flaky, retryDelays: [5, 5, 5] });
  assert.equal(await client.next("b-empty"), null);
  assert.equal(calls, 3);
});

test("a daemon that keeps failing, or refuses the key, stops the runner with 1", { skip }, async () => {
  daemon.failNext.set("b-down", [503, 503, 503, 503]);
  const down = await job.runJob({ api: origin(api), key: KEY, batch: "b-down", retryDelays: [5, 5, 5], log: () => {} });
  assert.equal(down, 1);
  assert.equal(daemon.nextCalls.get("b-down"), 4, "one try and three retries");

  daemon.failNext.set("b-401", [401]);
  const lines = [];
  const denied = await job.runJob({ api: origin(api), key: KEY, batch: "b-401", retryDelays: [5], log: (l) => lines.push(l) });
  assert.equal(denied, 1);
  assert.equal(daemon.nextCalls.get("b-401"), 1, "a refused key is not asked twice");
  assert.ok(lines.some((l) => /HTTP 401/.test(l)));
});

test("node evidence/job.js exits 0 on an empty batch, 2 without its environment", { skip }, async () => {
  const run = (env) => new Promise((resolve) => {
    const child = spawn(process.execPath, [path.join(HERE, "..", "evidence", "job.js")], {
      env: { PATH: process.env.PATH, HOME: process.env.HOME, ...env }, stdio: ["ignore", "pipe", "pipe"],
    });
    let out = "";
    child.stdout.on("data", (d) => { out += d; });
    child.stderr.on("data", (d) => { out += d; });
    child.on("exit", (code) => resolve({ code, out }));
  });
  const empty = await run({ OCTOPUS_API: origin(api), OCTOPUS_API_KEY: KEY, EVIDENCE_BATCH_ID: "b-none" });
  assert.equal(empty.code, 0, empty.out);
  assert.match(empty.out, /b-none has nothing left/);
  assert.ok(!empty.out.includes(KEY));
  const bare = await run({});
  assert.equal(bare.code, 2);
});

test("the secret scrubber cuts every value, longest first", () => {
  return import("../evidence/job.js").then(({ scrubber }) => {
    const scrub = scrubber(["hunter2-long", "hunter2", "abc", undefined]);
    assert.equal(scrub("typed hunter2-long then hunter2, abc"), "typed ••• then •••, abc");
  });
});

test("a next answer without a run id stops the runner instead of reading as done", async () => {
  const { runJob } = await import("../evidence/job.js");
  const lines = [];
  const client = { next: async () => ({ raw: "<html>" }), artifact: async () => null, result: async () => null };
  assert.equal(await runJob({ api: "http://x", key: KEY, batch: "b", client, log: (l) => lines.push(l) }), 1);
  assert.ok(lines.some((l) => /without a run id/.test(l)));
});

const closedPort = () => new Promise((resolve) => {
  const s = http.createServer().listen(0, "127.0.0.1", () => {
    const { port } = s.address();
    s.close(() => resolve(port));
  });
});

test("any HTTP status counts as an answer; a refused connection does not", async () => {
  const { waitReachable } = await import("../evidence/job.js");
  const down = await serve((_q, res) => { res.statusCode = 503; res.end("starting"); });
  try {
    assert.deepEqual(await waitReachable(origin(down), { windowMs: 200, everyMs: 20 }), { ok: true });
  } finally {
    down.close();
  }
  const port = await closedPort();
  const r = await waitReachable(`http://127.0.0.1:${port}/`, { windowMs: 200, everyMs: 20 });
  assert.equal(r.ok, false);
  assert.match(r.error, /ECONNREFUSED/);
});

test("a URL that starts answering inside the window is reachable", async () => {
  const { waitReachable } = await import("../evidence/job.js");
  const port = await closedPort();
  let late;
  setTimeout(() => {
    late = http.createServer((_q, res) => res.end("up")).listen(port, "127.0.0.1");
  }, 150);
  try {
    const r = await waitReachable(`http://127.0.0.1:${port}/`, { windowMs: 3_000, everyMs: 50 });
    assert.equal(r.ok, true, r.error);
  } finally {
    late?.close();
  }
});

test("an environment that never answers is reported once and the runner exits 0 without running a case", async () => {
  const { runJob } = await import("../evidence/job.js");
  const port = await closedPort();
  const calls = { unreachable: [], result: 0, next: 0 };
  const client = {
    next: async () => {
      calls.next++;
      return { run: { id: "r1", case_key: "EV-1" }, take: { version: 1, steps: [] },
        base_url: `http://127.0.0.1:${port}`, secrets: { PW: "hunter2-secret" } };
    },
    artifact: async () => null,
    result: async () => { calls.result++; return null; },
    unreachable: async (batch, error) => { calls.unreachable.push({ batch, error }); return { status: "failed" }; },
  };
  const lines = [];
  const code = await runJob({ api: "http://x", key: KEY, batch: "b-down-env", client, log: (l) => lines.push(l),
    reach: { windowMs: 200, everyMs: 20 } });
  assert.equal(code, 0);
  assert.equal(calls.next, 1, "no second run is asked for");
  assert.equal(calls.result, 0, "no case gets a result");
  assert.equal(calls.unreachable.length, 1);
  assert.equal(calls.unreachable[0].batch, "b-down-env");
  assert.match(calls.unreachable[0].error, /ECONNREFUSED/);
  assert.ok(lines.some((l) => /never answered/.test(l)), lines.join("\n"));
});

test("an unreachable URL says why: the cause's code, or its message when it has none", async () => {
  const { waitReachable } = await import("../evidence/job.js");
  const noCode = async () => { throw new TypeError("fetch failed", { cause: new Error("bad port") }); };
  const r = await waitReachable("http://x:9/", { fetch: noCode, windowMs: 50, everyMs: 10 });
  assert.equal(r.error, "fetch failed (bad port)");
  const both = async () => {
    throw new TypeError("fetch failed", { cause: Object.assign(new AggregateError([Object.assign(new Error("v6"), { code: "ENETUNREACH" })]), {}) });
  };
  assert.equal((await waitReachable("http://x/", { fetch: both, windowMs: 50, everyMs: 10 })).error, "fetch failed (ENETUNREACH)");
});
