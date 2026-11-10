import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import http from "node:http";
import net from "node:net";
import path from "node:path";
import { after, before, describe, test } from "node:test";
import { fileURLToPath } from "node:url";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";
import { chromiumMissing } from "./chromium.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REC_KEY = "oct_recording_chat";
const PASSWORD = "s3cret-pass-1";

async function browserMissing() {
  const reason = await chromiumMissing();
  if (reason) return reason;
  try { execFileSync(process.env.FFMPEG_PATH || "ffmpeg", ["-version"], { stdio: "ignore" }); } catch { return "ffmpeg is not installed"; }
  return false;
}
const noBrowser = await browserMissing();

function serve(handler) {
  return new Promise((resolve) => {
    const s = http.createServer(handler).listen(0, "127.0.0.1", () => resolve(s));
  });
}
const origin = (s) => `http://127.0.0.1:${s.address().port}`;

const daemon = { requests: [], saved: [] };
const recording = {
  recording: { id: "rec1", status: "open" },
  case: { key: "EV-3", title: "Login leads to the dashboard", description: "", steps: "1. Sign in", expected: "The dashboard greets me" },
  environment: { name: "Staging", base_url: "https://staging.example.com", secret_names: ["QA_EMAIL", "QA_PASSWORD"] },
};

function daemonHandler(req, res) {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const body = Buffer.concat(chunks).toString();
    const key = req.headers["x-api-key"];
    daemon.requests.push({ method: req.method, url: req.url, key, type: req.headers["content-type"], body });
    res.setHeader("content-type", "application/json");
    if (key !== REC_KEY) { res.statusCode = 404; return res.end(JSON.stringify({ error: "no open recording for this chat" })); }
    if (req.method === "GET" && req.url === "/evidence/recording") return res.end(JSON.stringify(recording));
    if (req.method === "GET" && req.url === "/evidence/recording/secrets/QA_PASSWORD") {
      return res.end(JSON.stringify({ name: "QA_PASSWORD", value: PASSWORD }));
    }
    if (req.method === "PUT" && req.url === "/evidence/recording/take") {
      daemon.saved.push(JSON.parse(body));
      return res.end(JSON.stringify({ case: { key: "EV-3", status: "ready" }, batch: { id: "batch-9", status: "queued" } }));
    }
    res.statusCode = 404;
    res.end(JSON.stringify({ error: "not found" }));
  });
}

let api, mod;

before(async () => {
  api = await serve(daemonHandler);
  // lib/octopus.js reads OCTOPUS_API at load time, so set it before importing.
  process.env.OCTOPUS_API = origin(api);
  mod = { ...(await import("../evidence/tools.js")), ...(await import("../lib/octopus.js")) };
});

after(async () => {
  mod.clearEvidenceStores();
  const { closeBrowser } = await import("../lib/browser.js");
  await closeBrowser();
  api.close();
});

const textOf = (r) => r.content.filter((c) => c.type === "text").map((c) => c.text).join("\n");
const handlers = () => mod.evidenceHandlers({ requireSession: () => { throw new Error("no session needed"); } });
const as = (key, fn) => mod.callerKey.run(key, fn);
const take = (steps, extra = {}) => ({ version: 1, title: "EV-3 · Login", base_url: "https://staging.example.com",
  viewport: { width: 800, height: 500 }, steps, ...extra });
const measured = [
  { kind: "goto", url: "/login" },
  { kind: "fill", target: "#pw", secret: "QA_PASSWORD" },
  { kind: "verify", case: "EV-3", expected: "/dashboard", check: "url" },
];

describe("evidence_recording", () => {
  test("shows the case, the environment and the login names, with the caller's key", async () => {
    daemon.requests.length = 0;
    const out = textOf(await as(REC_KEY, () => handlers().evidence_recording({})));
    assert.match(out, /Case to record: EV-3 · Login leads to the dashboard/);
    assert.match(out, /Steps:\n1\. Sign in/);
    assert.match(out, /Expected result:\nThe dashboard greets me/);
    assert.doesNotMatch(out, /Context:/, "an empty description is left out");
    assert.match(out, /Environment: Staging · https:\/\/staging\.example\.com/);
    assert.match(out, /Test logins: QA_EMAIL, QA_PASSWORD\./);
    assert.deepEqual(daemon.requests.map((r) => [r.method, r.url, r.key]), [["GET", "/evidence/recording", REC_KEY]]);
  });

  test("outside a recording chat the daemon's 404 reads as such", async () => {
    await assert.rejects(as("oct_task_run", () => handlers().evidence_recording({})), /no open recording in this chat/);
  });
});

describe("evidence_save_case", () => {
  test("PUTs {take} to the recording and answers with the case and the queued batch", async () => {
    daemon.requests.length = 0;
    daemon.saved.length = 0;
    const out = textOf(await as(REC_KEY, () => handlers().evidence_save_case({ take: take(measured), title: "EV-3 · Sign in" })));
    assert.equal(out, "Saved as EV-3's recording; a first run on Staging is queued (batch batch-9).");
    const put = daemon.requests.find((r) => r.method === "PUT");
    assert.equal(put.url, "/evidence/recording/take");
    assert.equal(put.key, REC_KEY);
    assert.match(put.type, /application\/json/);
    assert.deepEqual(Object.keys(daemon.saved[0]), ["take"]);
    assert.equal(daemon.saved[0].take.title, "EV-3 · Sign in", "title replaces the take's");
    assert.deepEqual(daemon.saved[0].take.steps, measured);
    assert.deepEqual(daemon.saved[0].take.viewport, { width: 800, height: 500 });
  });

  test("warns about a take that changes data, has no checks, or only observes", async () => {
    const effects = take([{ kind: "goto", url: "/" }, { kind: "click", target: "#pay", side_effect: true },
      { kind: "verify", case: "EV-3", expected: "ok", check: "text", target: "#msg" }]);
    assert.match(textOf(await as(REC_KEY, () => handlers().evidence_save_case({ take: effects }))),
      /changes data, so it will not replay automatically/);
    assert.match(textOf(await as(REC_KEY, () => handlers().evidence_save_case({ take: take([{ kind: "goto", url: "/" }]) }))),
      /has no checks/);
    const observed = take([{ kind: "goto", url: "/" }, { kind: "verify", case: "EV-3", expected: "ok", observed: "ok" }]);
    assert.match(textOf(await as(REC_KEY, () => handlers().evidence_save_case({ take: observed }))), /observed, not measured/);
  });

  test("refuses before sending anything: no take, two takes, a take that needs a repo library, a bad take", async () => {
    daemon.requests.length = 0;
    const save = (a) => as(REC_KEY, () => handlers().evidence_save_case(a));
    await assert.rejects(save({}), /exactly one of take_id, render_id or take/);
    await assert.rejects(save({ take: take(measured), take_id: "x" }), /exactly one/);
    await assert.rejects(save({ take: take([{ kind: "use", flow: "login" }]) }), /login/);
    await assert.rejects(save({ take: take([{ kind: "click", target: "@pay" }]) }), /pay/);
    await assert.rejects(save({ take: { title: "no base url", steps: [{ kind: "goto", url: "/" }] } }), /base_url/);
    await assert.rejects(save({ take_id: "gone" }), /not found|expired/);
    assert.deepEqual(daemon.requests, []);
  });

  test("outside a recording chat it says so and saves nothing", async () => {
    daemon.saved.length = 0;
    await assert.rejects(as("oct_task_run", () => handlers().evidence_save_case({ take: take(measured) })),
      /no open recording in this chat/);
    assert.deepEqual(daemon.saved, []);
  });

  test("saves the take behind a render_id or a take_id", { skip: noBrowser }, async () => {
    const app = await serve((_q, res) => { res.setHeader("content-type", "text/html"); res.end("<title>t</title><h1>Hi</h1>"); });
    try {
      const h = handlers();
      const r = await h.evidence_render({
        take: take([{ kind: "goto", url: "/" }, { kind: "verify", case: "EV-3", expected: "Hi", check: "text", target: "h1" }]),
        base_url: origin(app), pace: 0.3,
      });
      const ids = Object.fromEntries([...textOf(r).matchAll(/(render_id|take_id): (\S+)/g)].map((m) => [m[1], m[2]]));
      daemon.saved.length = 0;
      await as(REC_KEY, () => h.evidence_save_case({ render_id: ids.render_id }));
      await as(REC_KEY, () => h.evidence_save_case({ take_id: ids.take_id }));
      assert.equal(daemon.saved.length, 2);
      for (const s of daemon.saved) assert.equal(s.take.steps[1].target, "h1");
    } finally {
      app.close();
    }
  });
});

describe("browser_type with secret and no text", () => {
  test("fetches the value with the caller's key", async () => {
    daemon.requests.length = 0;
    const args = await as(REC_KEY, () => mod.typedText({ session_id: "s", selector: "#pw", secret: "QA_PASSWORD" }));
    assert.equal(args.text, PASSWORD);
    assert.deepEqual(daemon.requests.map((r) => [r.method, r.url, r.key]),
      [["GET", "/evidence/recording/secrets/QA_PASSWORD", REC_KEY]]);
  });

  test("with text it types the text and asks nobody; with neither it refuses", async () => {
    daemon.requests.length = 0;
    const args = await as(REC_KEY, () => mod.typedText({ selector: "#pw", text: "given", secret: "password" }));
    assert.equal(args.text, "given");
    assert.deepEqual(daemon.requests, []);
    await assert.rejects(as(REC_KEY, () => mod.typedText({ selector: "#pw" })), /needs text/);
  });

  test("a name the environment lacks, or a chat that is not recording, is a clear refusal", async () => {
    await assert.rejects(as(REC_KEY, () => mod.typedText({ selector: "#pw", secret: "NOPE" })), /no test login named "NOPE"/);
    await assert.rejects(as("oct_task_run", () => mod.typedText({ selector: "#pw", secret: "QA_PASSWORD" })),
      /no test login named "QA_PASSWORD"/);
  });
});

async function freePort() {
  return new Promise((resolve) => {
    const s = net.createServer().listen(0, "127.0.0.1", () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });
}

describe("browser_type(secret) on the MCP server", { skip: noBrowser }, () => {
  let child, base, app;

  before(async () => {
    app = await serve((_q, res) => {
      res.setHeader("content-type", "text/html");
      res.end(`<!doctype html><title>Login</title><input id="pw" type="password"
        oninput="document.querySelector('#ok').textContent = this.value === '${PASSWORD}' ? 'match' : 'no match'">
        <p id="ok"></p>`);
    });
    const port = await freePort();
    child = spawn(process.execPath, [path.join(HERE, "..", "server.js")], {
      env: { ...process.env, PORT: String(port), LISTEN_HOST: "127.0.0.1", OCTOPUS_API: origin(api) },
      stdio: ["ignore", "pipe", "pipe"],
    });
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("thulu-browser did not start in time")), 15000);
      child.stdout.on("data", (d) => { if (String(d).includes("listening on")) { clearTimeout(timer); resolve(); } });
      child.on("exit", (code) => reject(new Error(`thulu-browser exited early (${code})`)));
    });
    base = `http://127.0.0.1:${port}`;
  });

  after(() => {
    child?.kill("SIGKILL");
    app?.close();
  });

  test("types the recording's test login without returning or logging it", async () => {
    const headers = { "X-API-Key": REC_KEY };
    const client = new Client({ name: "test", version: "0" }, { capabilities: {} });
    await client.connect(new SSEClientTransport(new URL(`${base}/sse`), {
      requestInit: { headers },
      eventSourceInit: { fetch: (url, init) => fetch(url, { ...init, headers: { ...init?.headers, ...headers } }) },
    }));
    try {
      const call = async (name, args) => {
        const r = await client.callTool({ name, arguments: args });
        assert.notEqual(r.isError, true, textOf(r));
        return textOf(r);
      };
      const id = /session_id: (\S+)/.exec(await call("browser_new_session", { viewport_width: 800, viewport_height: 500 }))[1];
      await call("browser_navigate", { session_id: id, url: origin(app) });
      const typed = await call("browser_type", { session_id: id, selector: "#pw", secret: "QA_PASSWORD" });
      assert.match(typed, /QA_PASSWORD/);
      assert.ok(!typed.includes(PASSWORD));
      assert.match(await call("browser_get_text", { session_id: id }), /^match$/m);
      const log = await call("browser_get_session_log", { session_id: id });
      assert.ok(!log.includes(PASSWORD));
      assert.match(log, /"secret": "QA_PASSWORD"/);
      await call("browser_close_session", { session_id: id });
    } finally {
      await client.close();
    }
  });
});
