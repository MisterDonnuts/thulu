// The session tools end to end over MCP: a throttled session, page errors per step and the
// timing of each action.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import path from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";

import { chromiumMissing } from "./chromium.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const skip = await chromiumMissing();

// The empty icon keeps Chromium from asking for /favicon.ico in the middle of a measured step.
const page = `<!doctype html><html lang="en"><title>Cart</title><link rel="icon" href="data:,">
<body style="font:16px sans-serif;padding:32px">
  <h1>Cart</h1>
  <p id="scheme"></p>
  <button id="pay" onclick="fetch('/api/pay?card=4242', { method: 'POST' })
    .then((r) => { document.querySelector('#out').textContent = 'HTTP ' + r.status; })">Pay</button>
  <button id="boom" onclick="console.error('cart total is NaN'); setTimeout(() => { throw new TypeError('x is undefined'); })">Break</button>
  <button id="inert">Nothing</button>
  <p id="out"></p>
  <script>
    document.querySelector('#scheme').textContent = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  </script>
</body></html>`;

const listen = (server) => new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

let app, srv, client;

before(async () => {
  if (skip) return;
  app = http.createServer((req, res) => {
    if (req.url.startsWith("/api/pay")) { res.statusCode = 500; return res.end("{}"); }
    if (req.url === "/favicon.ico") { res.statusCode = 404; return res.end(); }
    res.setHeader("content-type", "text/html");
    res.end(page);
  });
  await listen(app);
  const probe = http.createServer();
  const port = await listen(probe);
  await new Promise((r) => probe.close(r));
  const child = spawn(process.execPath, [path.join(HERE, "..", "server.js")], {
    env: { ...process.env, PORT: String(port), LISTEN_HOST: "127.0.0.1", OCTOPUS_API: "http://127.0.0.1:9", OCTOPUS_LOCALE: "en" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("thulu-browser did not start in time")), 15000);
    child.stdout.on("data", (d) => { if (String(d).includes("listening on")) { clearTimeout(timer); resolve(); } });
    child.on("exit", (code) => reject(new Error(`thulu-browser exited early (${code})`)));
  });
  srv = child;
  const base = `http://127.0.0.1:${port}`;
  const headers = { "X-API-Key": "oct_run_key" };
  client = new Client({ name: "test", version: "0" }, { capabilities: {} });
  await client.connect(new SSEClientTransport(new URL(`${base}/sse`), {
    requestInit: { headers },
    eventSourceInit: { fetch: (url, init) => fetch(url, { ...init, headers: { ...init?.headers, ...headers } }) },
  }));
});

after(async () => {
  if (skip) return;
  await client?.close();
  srv?.kill("SIGKILL");
  app?.close();
});

const text = (r) => r.content.filter((c) => c.type === "text").map((c) => c.text).join("\n");
const call = async (name, args) => {
  const r = await client.callTool({ name, arguments: args });
  assert.ok(!r.isError, `${name} failed: ${text(r)}`);
  return text(r);
};

test("a bad profile is refused before any browser opens", { skip }, async () => {
  const r = await client.callTool({ name: "browser_new_session", arguments: { network: "5g" } });
  assert.equal(r.isError, true);
  assert.match(text(r), /unknown network profile "5g"/);
});

test("a throttled dark session times each action and files page errors under the step that caused them", { skip }, async () => {
  const opened = await call("browser_new_session", { network: "fast-4g", cpu_throttle: 2, color_scheme: "dark" });
  assert.match(opened, /Profile: fast-4g · CPU 2x · dark\./);
  const session_id = /session_id: (\S+)/.exec(opened)[1];
  const origin = `http://127.0.0.1:${app.address().port}`;

  const nav = await call("browser_navigate", { session_id, url: `${origin}/cart` });
  assert.match(nav, /Response \d/);
  assert.match(await call("browser_get_text", { session_id }), /Cart\s+dark/, "the page sees prefers-color-scheme: dark");

  const paid = await call("browser_click", { session_id, selector: "#pay" });
  assert.match(paid, /1 page error: 500 POST .*\/api\/pay/);
  assert.doesNotMatch(paid, /4242/, "the query string never leaves the browser");
  await call("browser_click", { session_id, selector: "#boom" });
  const inert = await call("browser_click", { session_id, selector: "#inert" });
  assert.match(inert, /No response: the page did not change and sent no request/);

  const findings = await call("browser_findings", { session_id });
  assert.match(findings, /^3 page errors, by step:/);
  assert.match(findings, /#\d+ browser_click #pay\n  - 500 POST http:\/\/127\.0\.0\.1:\d+\/api\/pay\n/);
  assert.match(findings, /browser_click #boom\n  - Console error: cart total is NaN\n  - Uncaught error: TypeError: x is undefined/);
  assert.doesNotMatch(findings, /favicon/);

  const json = JSON.parse(await call("browser_metrics", { session_id, format: "json" }));
  assert.deepEqual(json.profile, { network: "fast-4g", cpu_throttle: 2, color_scheme: "dark",
    viewport: { width: 1440, height: 900 } });
  const [goto, pay, , dead] = json.steps;
  assert.equal(goto.tool, "browser_navigate");
  assert.ok(goto.ttfb >= 0 && goto.fcp > 0, JSON.stringify(goto));
  assert.ok(pay.response_ms > 0 && pay.requests >= 1, JSON.stringify(pay));
  assert.equal(dead.dead, true);
  assert.deepEqual(json.findings, { http: 1, failed: 0, console: 1, pageerror: 1 });

  const words = await call("browser_metrics", { session_id });
  assert.match(words, /^Lab timing, one run in this browser \(not field data\) · 1440×900 · fast-4g · CPU 2x · dark/);
  assert.match(words, /Page errors: 1 HTTP 4xx\/5xx, 0 failed requests, 1 console errors, 1 uncaught/);
  await call("browser_close_session", { session_id });
});
