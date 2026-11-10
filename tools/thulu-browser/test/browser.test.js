import assert from "node:assert/strict";
import http from "node:http";
import { after, before, test } from "node:test";
import { execFileSync } from "node:child_process";

import { chromiumMissing } from "./chromium.js";

async function missing() {
  const reason = await chromiumMissing();
  if (reason) return reason;
  try { execFileSync(process.env.FFMPEG_PATH || "ffmpeg", ["-version"], { stdio: "ignore" }); } catch { return "ffmpeg is not installed"; }
  return false;
}
const skip = await missing();

const app = (v) => `<!doctype html><html lang="en"><title>Shop v${v}</title>
<body style="font:16px sans-serif;padding:32px">
  <h1>Checkout</h1>
  <label for="coupon">Coupon</label> <input id="coupon" placeholder="Code">
  <label>Password <input type="password" name="password"></label>
  <label>Day <input type="date" name="day" value="2026-09-01" oninput="document.querySelector('#picked').textContent = this.value"></label>
  <span id="picked"></span>
  <button id="btn-8f3a91c2d7" onclick="document.querySelector('#total').textContent = '${v === 2 ? "$90" : "$100"}'">Apply</button>
  <button aria-label="Close banner">×</button>
  <button data-testid="pay">Pay</button>
  <ul><li>One</li><li>One</li></ul>
  <a href="https://elsewhere.test/">Leave</a>
  <p>Total: <strong id="total">$100</strong></p>
</body></html>`;

function serve(handler) {
  return new Promise((resolve) => {
    const s = http.createServer(handler).listen(0, "127.0.0.1", () => resolve(s));
  });
}
const origin = (s) => `http://127.0.0.1:${s.address().port}`;

const daemon = { uploads: [], reports: [], comments: [], runs: [], keys: new Set() };
let v1, v2, api, mod, browser;

before(async () => {
  if (skip) return;
  v1 = await serve((_q, res) => { res.setHeader("content-type", "text/html"); res.end(app(1)); });
  v2 = await serve((_q, res) => { res.setHeader("content-type", "text/html"); res.end(app(2)); });
  api = await serve((req, res) => {
    const chunks = [];
    req.on("data", (c) => chunks.push(c));
    req.on("end", () => {
      daemon.keys.add(req.headers["x-api-key"]);
      const body = Buffer.concat(chunks);
      res.setHeader("content-type", "application/json");
      if (req.url.endsWith("/attachments")) {
        const head = body.subarray(0, 400).toString("latin1");
        const filename = /filename="([^"]+)"/.exec(head)[1];
        const type = /Content-Type: ([^\r\n]+)/i.exec(head)[1];
        const id = `att${daemon.uploads.length + 1}`;
        daemon.uploads.push({ id, filename, type, bytes: body.length, task: req.url.split("/")[2] });
        res.end(JSON.stringify({ id, filename }));
      } else if (req.url.endsWith("/test-cases")) {
        if (req.url.includes("task-down")) { res.statusCode = 503; return res.end("{}"); }
        res.end(JSON.stringify([{ id: "tc1", key: "TC-1", title: "Total without coupon", status: "pending", runs: [] }]));
      } else if (req.url.endsWith("/runs")) {
        daemon.runs.push({ url: req.url, ...JSON.parse(body.toString()) });
        res.end(JSON.stringify({ id: `run${daemon.runs.length}` }));
      } else if (req.url.endsWith("/reports")) {
        const r = { id: `rep${daemon.reports.length + 1}`, url: req.url, ...JSON.parse(body.toString()) };
        daemon.reports.push(r);
        res.statusCode = 201;
        res.end(JSON.stringify(r));
      } else {
        daemon.comments.push({ url: req.url, body: body.toString() });
        res.statusCode = 404;
        res.end("{}");
      }
    });
  });
  // lib/octopus.js reads OCTOPUS_API at load time, so set it before importing the modules.
  process.env.OCTOPUS_API = origin(api);
  mod = {
    ...(await import("../evidence/tools.js")),
    ...(await import("../evidence/selector.js")),
    ...(await import("../lib/octopus.js")),
    ...(await import("../lib/browser.js")),
  };
  browser = await mod.getBrowser();
});

after(async () => {
  if (skip) return;
  mod.clearEvidenceStores();
  await mod.closeBrowser();
  for (const s of [v1, v2, api]) s.close();
});

async function session() {
  const context = await browser.newContext({ viewport: { width: 900, height: 560 } });
  const page = await context.newPage();
  await page.goto(origin(v1));
  return { context, page, log: [], meta: { task_id: "task-1" } };
}

const textOf = (r) => r.content.filter((c) => c.type === "text").map((c) => c.text).join("\n");
const idIn = (r, key) => new RegExp(`${key}: (\\S+)`).exec(textOf(r))[1];

test("stable selectors prefer what survives a redesign, and match only their element", { skip }, async () => {
  const { context, page } = await session();
  const sel = (css) => page.$(css).then((h) => mod.stableSelector(page, h));
  assert.equal(await sel("[data-testid=pay]"), "[data-testid=\"pay\"]");
  assert.equal(await sel("#btn-8f3a91c2d7"), "role=button[name=\"Apply\"]");
  assert.equal(await sel("[aria-label='Close banner']"), "role=button[name=\"Close banner\"]");
  assert.equal(await sel("#coupon"), "role=textbox[name=\"Coupon\"]");
  const second = await sel("li:nth-child(2)");
  assert.equal(second, "li:nth-of-type(2)");
  assert.equal(await page.locator(second).count(), 1);
  assert.equal(await page.locator(second).evaluate((n) => n === document.querySelectorAll("li")[1]), true);
  await context.close();
});

test("a live take films the actions, keeps secrets out, and publishes to the task", { skip }, async () => {
  const s = await session();
  const h = mod.evidenceHandlers({ requireSession: () => s });
  const call = (name, args) => mod.callerKey.run("run-key", () => h[name]({ session_id: "s", ...args }));

  await call("evidence_start", { title: "Coupon · live" });
  await mod.recordAction(s, "goto", { url: `${origin(v1)}/checkout?x=1`, label: "Open checkout" });
  await mod.recordAction(s, "type", { selector: "#coupon", text: "SAVE10", label: "Type the coupon" });
  await mod.recordAction(s, "type", { selector: "[name=password]", text: "hunter2", secret: "password" });
  await mod.recordAction(s, "type", { selector: "[name=day]", text: "2026-09-26", label: "Pick the 26th" });
  await mod.recordAction(s, "click", { selector: "#btn-8f3a91c2d7", label: "Apply it" });
  assert.equal(await s.page.textContent("#picked"), "2026-09-26", "a date field gets its value whole, not key by key");
  const verdict = await call("evidence_verify", { case: "TC-1", expected: "$100", check: "text", target: "#total" });
  assert.match(textOf(verdict), /^PASS TC-1/);
  await call("evidence_verify", { case: "TC-1", expected: "Checkout", check: "text", target: "h1" });
  await call("evidence_verify", { case: "TC-1a", expected: "visible", check: "visible", target: "#total" });
  await call("evidence_note", { text: "No discount on v1", level: "issue" });
  await call("evidence_shot", { name: "total", title: "Total", spotlight: [{ target: "#total", note: "unchanged" }] });

  const done = await call("evidence_finish", {});
  assert.ok(done.content.some((c) => c.type === "image" && c.mimeType === "image/jpeg"), "contact sheet comes back inline");
  assert.equal(daemon.uploads.length, 0, "nothing reaches the task before publish");

  const take = JSON.parse(textOf(await call("evidence_get_take", { take_id: idIn(done, "take_id") })));
  assert.equal(take.base_url, origin(v1));
  assert.deepEqual(take.steps[0], { kind: "goto", url: "/" }, "the page the take started on is written first");
  assert.equal(take.steps[1].url, "/checkout?x=1", "same-origin navigation is stored as a path");
  assert.equal(take.steps[3].secret, "password");
  assert.doesNotMatch(JSON.stringify(take), /hunter2/);
  assert.doesNotMatch(JSON.stringify(s.log), /hunter2/);
  assert.equal(take.steps[5].target, "#btn-8f3a91c2d7", "a selector the agent gave is kept as given");

  const pub = await call("evidence_publish", { render_id: idIn(done, "render_id") });
  assert.match(textOf(pub), /Report posted to the Reports tab \(rep1\)\./);
  assert.match(textOf(pub), /Recorded runs: TC-1 pass/);
  assert.match(textOf(pub), /Not test cases on this task: TC-1a/);
  const kinds = daemon.uploads.map((u) => u.type).sort();
  assert.deepEqual(kinds, ["application/json", "image/png", "text/vtt", "video/mp4"]);
  assert.ok(daemon.uploads.every((u) => u.task === "task-1"));
  assert.deepEqual([...daemon.keys], ["run-key"], "the caller's key, and only it, reaches the daemon");
  assert.deepEqual(daemon.comments, [], "the evidence never lands in the comment thread");
  const [report] = daemon.reports;
  assert.equal(report.url, "/tasks/task-1/reports");
  assert.equal(report.kind, "evidence");
  assert.equal(report.title, "Coupon · live · pass");
  assert.match(report.body, /^`http:\/\/127\.0\.0\.1:\d+` · 900×560 · /, "the title is not repeated as a heading");
  assert.match(report.body, /!\[coupon-live\.mp4\]\(\/task-attachments\/att\d\)/);
  assert.match(report.body, /⚠️ No discount on v1/);
  assert.match(report.body, /Replay it: \[coupon-live\.take\.json\]/);
  const idOf = (type) => daemon.uploads.find((u) => u.type === type).id;
  assert.deepEqual(daemon.runs, [{
    url: "/tasks/task-1/test-cases/TC-1/runs",
    verdict: "pass",
    observed: "$100; Checkout",
    video_attachment_id: idOf("video/mp4"),
    take_attachment_id: idOf("application/json"),
    report_id: "rep1",
    capture_attachment_ids: [idOf("image/png")],
  }], "two verdicts of TC-1 make one run; TC-1a is not a case of the task");
  await s.context.close();
});

test("a publish whose test case runs cannot be recorded still delivers, and says so", { skip }, async () => {
  const h = mod.evidenceHandlers({ requireSession: () => { throw new Error("no session needed"); } });
  const call = (name, args) => mod.callerKey.run("run-key", () => h[name](args));
  const r = await call("evidence_render", {
    take: { version: 1, title: "Down", base_url: origin(v1), viewport: { width: 800, height: 500 },
      steps: [{ kind: "goto", url: "/" }, { kind: "verify", case: "TC-1", expected: "$100", check: "text", target: "#total" }] },
    pace: 0.3,
    task_id: "task-down",
  });
  const reports = daemon.reports.length;
  const out = textOf(await call("evidence_publish", { render_id: idIn(r, "render_id") }));
  assert.equal(daemon.reports.length, reports + 1, "the report is posted before the runs are recorded");
  const { id } = daemon.reports.at(-1);
  assert.match(out, new RegExp(`Report posted to the Reports tab \\(${id}\\)`));
  assert.match(out, /Could not record the test case runs: .*HTTP 503/);
  assert.match(out, new RegExp(`task_test_case_run and report_id=${id}\\.`));
  assert.doesNotMatch(out, /Recorded runs/);
});

test("a take replays against two origins into one side-by-side video", { skip }, async () => {
  const h = mod.evidenceHandlers({ requireSession: () => { throw new Error("no session needed"); } });
  const r = await h.evidence_render({
    take: {
      version: 1, title: "Coupon · compare", base_url: origin(v1), viewport: { width: 800, height: 500 },
      steps: [
        { kind: "goto", url: "/", wait_for: "@total" },
        { kind: "use", flow: "apply" },
        { kind: "verify", case: "TC-1", expected: "$90", check: "text", target: "@total", timeout_ms: 300 },
        { kind: "beat", ms: 300 },
      ],
    },
    library: { targets: { total: "#total" }, flows: { apply: { steps: [{ kind: "click", target: "role=button[name=\"Apply\"]" }] } } },
    compare_base_url: origin(v2),
    pace: 0.3,
  });
  const out = textOf(r);
  assert.match(out, /compare · .* · 3 chapters/, "the unlabelled beat stays in the verify's chapter");
  assert.match(out, /FAIL \[Before\] TC-1: expected "\$90", observed "\$100"/);
  assert.match(out, /PASS \[After\] TC-1/);
  assert.ok(r.content.some((c) => c.type === "image"));
});

test("a replay refuses to repeat a take that changes data, and stays on its hosts", { skip }, async () => {
  const h = mod.evidenceHandlers({ requireSession: () => { throw new Error("unused"); } });
  const base = { version: 1, title: "guarded", base_url: origin(v1), viewport: { width: 800, height: 500 } };
  await assert.rejects(
    h.evidence_render({ take: { ...base, steps: [{ kind: "goto", url: "/" }, { kind: "click", target: "[data-testid=pay]", side_effect: true }] } }),
    /changes data/,
  );
  const r = await h.evidence_render({
    take: { ...base, steps: [{ kind: "goto", url: "/" }, { kind: "click", target: "text=Leave" }, { kind: "note", text: "never" }] },
    pace: 0.3,
  });
  assert.match(textOf(r), /STEP FAILED/);
  assert.match(textOf(r), /elsewhere\.test/);
  assert.doesNotMatch(textOf(r), /never/);
});
