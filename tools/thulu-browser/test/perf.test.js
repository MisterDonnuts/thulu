import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import http from "node:http";
import { after, before, test } from "node:test";

import { chromiumMissing } from "./chromium.js";

const skip = await chromiumMissing();

const pages = {
  // The empty icon keeps Chromium from asking for /favicon.ico after load: that request, landing
  // inside a measure on a busy machine, would count as the page responding.
  "/": `<!doctype html><html lang="en"><title>Perf</title><link rel="icon" href="data:,">
<body style="font:16px sans-serif;margin:0">
  <h1>Orders</h1>
  <img src="/big.svg" width="900" height="500" alt="Hero">
  <button id="slow" onclick="setTimeout(() => { document.querySelector('#out').textContent = 'saved' }, 300)">Save</button>
  <button id="inert">Nothing</button>
  <button id="fetch" onclick="fetch('/api/slow').then(r => r.text()).then(t => { document.querySelector('#out').textContent = t })">Load</button>
  <button id="shift" onclick="setTimeout(() => { const d = document.createElement('div'); d.style.height = '300px'; document.body.prepend(d) }, 50)">Shift</button>
  <p id="out"></p>
  <script>
    const ws = new WebSocket('ws://' + location.host + '/ws');
  </script>
</body></html>`,
};

function serve() {
  return new Promise((resolve) => {
    const s = http.createServer((req, res) => {
      if (req.url === "/big.svg") {
        res.setHeader("content-type", "image/svg+xml");
        return res.end(`<svg xmlns="http://www.w3.org/2000/svg" width="900" height="500"><rect width="900" height="500" fill="#4a7"/></svg>`);
      }
      if (req.url === "/api/slow") return setTimeout(() => res.end("loaded"), 400);
      res.setHeader("content-type", "text/html");
      res.end(pages[req.url] ?? pages["/"]);
    });
    // A websocket that opens and stays open, as a live app's would.
    s.on("upgrade", (req, socket) => {
      sockets.add(socket);
      const accept = createHash("sha1").update(`${req.headers["sec-websocket-key"]}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
        .digest("base64");
      socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
      socket.on("error", () => {});
    });
    s.listen(0, "127.0.0.1", () => resolve(s));
  });
}
const sockets = new Set();

let app, base, browser, mod;

before(async () => {
  if (skip) return;
  app = await serve();
  base = `http://127.0.0.1:${app.address().port}`;
  mod = { ...(await import("../lib/perf.js")), ...(await import("../lib/throttle.js")), ...(await import("../lib/browser.js")) };
  browser = await mod.getBrowser();
});

after(async () => {
  if (skip) return;
  await mod.closeBrowser();
  for (const s of sockets) s.destroy();
  app.close();
});

async function open(profile = {}) {
  const context = await browser.newContext({ viewport: { width: 1000, height: 700 } });
  const page = await context.newPage();
  await mod.applyThrottle(page, profile);
  const perf = mod.perfFor(page);
  await perf.ready;
  return { context, page, perf };
}

test("a navigation reports TTFB, FCP and LCP, and a response time", { skip }, async () => {
  const { context, page, perf } = await open();
  const { perf: nav } = await perf.measure(() => page.goto(base, { waitUntil: "load" }));
  assert.equal(typeof nav.response_ms, "number");
  assert.equal(nav.dead, false);
  const v = await perf.settledSnapshot();
  assert.equal(typeof v.ttfb, "number");
  assert.ok(v.fcp > 0, "FCP");
  assert.ok(v.lcp > 0, "the hero image is the LCP");
  assert.equal(v.cls, 0, "nothing shifted yet");
  assert.equal(v.inp, null, "no interaction, no INP");
  await context.close();
});

test("a button that answers after 300 ms has a response of at least 300 ms; an inert one is dead", { skip }, async () => {
  const { context, page, perf } = await open();
  await page.goto(base);
  const { perf: slow } = await perf.measure(() => page.click("#slow"));
  assert.ok(slow.response_ms >= 290, `response ${slow.response_ms}`);
  assert.equal(slow.dead, false);
  const { perf: inert } = await perf.measure(() => page.click("#inert"));
  assert.equal(inert.dead, true);
  assert.equal(inert.response_ms, null);
  const v = await perf.snapshot();
  assert.equal(typeof v.inp, "number", "clicks give an INP, even if only as the lower bound");
  await context.close();
});

test("a fetch counts as part of the response, and the open websocket does not hang the settle", { skip }, async () => {
  const { context, page, perf } = await open();
  await page.goto(base);
  const started = Date.now();
  const { perf: m } = await perf.measure(() => page.click("#fetch"));
  assert.ok(m.response_ms >= 380, `response ${m.response_ms}`);
  assert.equal(m.requests, 1);
  assert.notEqual(m.capped, true);
  assert.ok(Date.now() - started < 4_000);
  await context.close();
});

test("a layout shift shows in CLS, while the evidence overlays do not count as the page responding", { skip }, async () => {
  const { context, page, perf } = await open();
  await page.goto(base);
  const overlay = await perf.measure(() => page.evaluate(() => {
    const el = document.createElement("octopus-cinema");
    document.documentElement.append(el);
    el.setAttribute("data-x", "1");
  }));
  assert.equal(overlay.perf.dead, true, "an overlay alone is not a response");
  assert.equal((await perf.snapshot()).cls, 0);
  // A shift within 500 ms of an input is expected by CLS and does not count, so this one comes from
  // a script, not a click.
  await perf.measure(() => page.evaluate(() => {
    const d = document.createElement("div");
    d.style.height = "300px";
    document.body.prepend(d);
  }));
  assert.ok((await perf.snapshot()).cls > 0);
  await context.close();
});

// Chromium applies the emulated latency before the document's navigation timing starts, so TTFB
// does not show it; the navigation's response time and every subresource do.
test("slow-4g slows the navigation and every request after it", { skip }, async () => {
  const fast = await open();
  await fast.page.goto(base);
  const { perf: quick } = await fast.perf.measure(() => fast.page.click("#fetch"));
  await fast.context.close();

  const { context, page, perf } = await open({ network: "slow-4g", cpu_throttle: 2 });
  const { perf: nav } = await perf.measure(() => page.goto(base, { waitUntil: "load" }));
  assert.ok(nav.response_ms >= 550, `navigation ${nav.response_ms}`);
  // The emulated latency is a floor per request (the 400 ms the server takes count toward it).
  const { perf: slow } = await perf.measure(() => page.click("#fetch"));
  assert.ok(slow.response_ms >= 560 && slow.response_ms > quick.response_ms, `fetch ${slow.response_ms} vs ${quick.response_ms}`);
  await context.close();
});

test("metric values format per locale and rate against web.dev's thresholds", async () => {
  const { formatMetric, describeVitals, metricValue } = await import("../lib/perf.js");
  const { withLocale } = await import("../i18n.js");
  assert.equal(withLocale("en", () => formatMetric("lcp", 3_240)), "3.2 s");
  assert.equal(withLocale("es", () => formatMetric("lcp", 3_240)), "3,2 s");
  assert.equal(withLocale("en", () => formatMetric("response", 420)), "420 ms");
  assert.equal(withLocale("en", () => formatMetric("cls", 0.0421)), "0.042");
  assert.equal(withLocale("en", () => formatMetric("inp", null)), "(no data)");
  assert.equal(withLocale("en", () => describeVitals({ lcp: 4_500, cls: 0.05, inp: 16, inp_bound: true })),
    "LCP 4.5 s (poor) · CLS 0.05 (good) · INP under 16 ms (good)");
  assert.deepEqual(metricValue("response", { action: { response_ms: 300 } }), { value: 300 });
  assert.deepEqual(metricValue("inp", { vitals: { inp: 16, inp_bound: true } }), { value: 16, bound: true });
  assert.deepEqual(metricValue("lcp", { vitals: { lcp: null } }), { value: null });
});
