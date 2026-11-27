// A blind session and the accessibility scan, end to end over MCP.
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

const pages = {
  "/": `<!doctype html><html lang="en"><title>Shop</title>
<body style="font:16px sans-serif;padding:32px"><main>
  <h1>Shop</h1>
  <a id="go-cart" class="nav-link" href="/cart">Cart</a>
  <a href="https://docs.example.test/help?ref=shop">Help</a>
  <button id="btn-save" class="primary" disabled>Save</button>
  <label for="email">Email</label> <input id="email" name="email" type="email" required>
</main></body></html>`,
  "/cart": `<!doctype html><html lang="en"><title>Cart</title>
<body style="font:16px sans-serif;padding:32px"><main>
  <h1>Cart</h1>
  <a href="/">Back to the shop</a>
  <input id="coupon" type="text">
</main></body></html>`,
};

const listen = (server) => new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server.address().port)));

let app, srv, client, origin;

before(async () => {
  if (skip) return;
  app = http.createServer((req, res) => {
    const body = pages[req.url.split("?")[0]];
    if (!body) { res.statusCode = 404; return res.end(); }
    res.setHeader("content-type", "text/html");
    res.end(body);
  });
  origin = `http://127.0.0.1:${await listen(app)}`;
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
const refused = async (name, args) => {
  const r = await client.callTool({ name, arguments: args });
  assert.equal(r.isError, true, `${name} should be refused: ${text(r)}`);
  return text(r);
};
const sessionOf = (opened) => /session_id: (\S+)/.exec(opened)[1];

test("a blind session sees roles, names and states, and nothing of how the page is built", { skip }, async () => {
  const opened = await call("browser_new_session", { blind: true });
  assert.match(opened, /Blind mode/);
  const session_id = sessionOf(opened);
  await call("browser_navigate", { session_id, url: origin });

  const state = await call("browser_get_state", { session_id });
  assert.match(state, /^Page: Shop \(127\.0\.0\.1:\d+\)/);
  assert.match(state, /\[1\] link "Cart"\n/);
  assert.match(state, /\[2\] link "Help" \(goes to docs\.example\.test\)/);
  assert.match(state, /\[3\] button "Save" \(disabled\)/);
  assert.match(state, /\[4\] textbox "Email" \(required\)/);
  for (const hidden of ["go-cart", "nav-link", "btn-save", "primary", "/cart", "ref=shop", "name=", "id="]) {
    assert.ok(!state.includes(hidden), `get_state leaks "${hidden}":\n${state}`);
  }

  assert.match(await refused("browser_navigate", { session_id, url: `${origin}/cart` }), /only the first navigation/);
  for (const tool of ["browser_run_script", "browser_get_session_log", "browser_export_script", "browser_list_scripts"]) {
    assert.match(await refused(tool, { session_id, code: "1" }), new RegExp(`${tool} is not available`));
  }

  // The tester finds the cart through the page, then comes back by clicking the page's own link.
  await call("browser_click", { session_id, index: 1 });
  await call("browser_go_back", { session_id });
  await call("browser_click", { session_id, index: 1 });
  await call("browser_click", { session_id, index: 1 });
  const { journey } = JSON.parse(await call("browser_metrics", { session_id, format: "json" }));
  assert.equal(journey.actions, 5);
  assert.equal(journey.navigations, 5, "the first visit, two clicks to the cart, one back, one link home");
  assert.equal(journey.backs, 3, "go_back, the cart seen again, and the shop seen again");
  assert.ok(journey.time_on_task_ms > 0);
  assert.match(await call("browser_metrics", { session_id }), /Journey: 5 actions, 5 navigations, 3 back,/);
  await call("browser_close_session", { session_id });
});

test("a blind tester keeps the page: a failed start retries, back from the first page stays put, a take hides its selectors", { skip }, async () => {
  // The start URL did not answer: the only navigation is not used up.
  const failed = sessionOf(await call("browser_new_session", { blind: true }));
  const probe = http.createServer();
  const closed = await listen(probe);
  await new Promise((r) => probe.close(r));
  await refused("browser_navigate", { session_id: failed, url: `http://127.0.0.1:${closed}/` });
  // Chromium then commits its own error page; navigating during that commit is interrupted.
  await call("browser_wait", { session_id: failed, ms: 500 });
  await call("browser_navigate", { session_id: failed, url: origin });
  await call("browser_close_session", { session_id: failed });

  const session_id = sessionOf(await call("browser_new_session", { blind: true }));
  await call("browser_navigate", { session_id, url: origin });

  // Back from the first page would land on the blank tab, where a blind tester cannot navigate again.
  assert.match(await call("browser_go_back", { session_id }), /Nothing to go back to/);
  assert.match(await call("browser_get_state", { session_id }), /^Page: Shop /);

  await call("evidence_start", { session_id, title: "Blind cart" });
  const clicked = await call("browser_click", { session_id, index: 1 });
  assert.match(clicked, /^Recorded: click\nPage: Cart/);
  for (const hidden of ["go-cart", "/cart", "href", "127.0.0.1"]) {
    assert.ok(!clicked.includes(hidden), `a recorded click leaks "${hidden}":\n${clicked}`);
  }
  await call("evidence_finish", { session_id, discard: true });
  const { journey } = JSON.parse(await call("browser_metrics", { session_id, format: "json" }));
  assert.equal(journey.backs, 0, "a go_back that went nowhere is not a way back");
  await call("browser_close_session", { session_id });
});

test("go_back works outside blind mode and says when there is nowhere to go", { skip }, async () => {
  const session_id = sessionOf(await call("browser_new_session", {}));
  assert.match(await call("browser_go_back", { session_id }), /Nothing to go back to/);
  await call("browser_navigate", { session_id, url: origin });
  await call("browser_navigate", { session_id, url: `${origin}/cart` });
  assert.match(await call("browser_go_back", { session_id }), new RegExp(`Went back to: ${origin}/`));
  assert.match(await call("browser_get_state", { session_id }), /URL: +http/, "a normal session keeps the full state");
  await call("browser_close_session", { session_id });
});

test("a11y_scan summarizes violations by impact and names the unlabelled input", { skip }, async () => {
  const session_id = sessionOf(await call("browser_new_session", {}));
  await call("browser_navigate", { session_id, url: `${origin}/cart` });
  const out = await call("a11y_scan", { session_id });
  assert.match(out, /^Accessibility of http:\/\/127\.0\.0\.1:\d+\/cart \(axe 4\.14\.0, wcag2a, wcag2aa, wcag21a, wcag21aa, wcag22aa\)/);
  assert.match(out, /Violations: 1 critical · 0 serious · 0 moderate · 0 minor/);
  assert.match(out, /Fails at serious or worse: 1 rule\./);
  assert.match(out, /- FAIL \[critical\] label: .* 1 element\(s\): #coupon\. https:\/\/dequeuniversity\.com/);
  assert.match(out, /A clean scan does not make the page accessible/);

  const scoped = await call("a11y_scan", { session_id, exclude: ["#coupon"] });
  assert.match(scoped, /Violations: 0 critical · 0 serious · 0 moderate · 0 minor/);
  await call("browser_close_session", { session_id });
});
