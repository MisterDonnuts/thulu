import assert from "node:assert/strict";
import { test } from "node:test";

import { withLocale } from "../i18n.js";
import { countByKind, describeFinding, groupFindings, ignorePatterns, shapeFinding } from "../lib/page-findings.js";
import { scrubber } from "../lib/scrub.js";

test("noise is dropped: the favicon, Chromium's echo of a 4xx, aborted and host-guarded requests", () => {
  assert.equal(shapeFinding({ kind: "http", status: 404, method: "GET", url: "http://app.test/favicon.ico" }), null);
  assert.equal(shapeFinding({ kind: "console", text: "Failed to load resource: the server responded with a status of 500" }), null);
  assert.equal(shapeFinding({ kind: "failed", text: "net::ERR_ABORTED", url: "http://app.test/x" }), null);
  assert.equal(shapeFinding({ kind: "failed", text: "net::ERR_BLOCKED_BY_CLIENT", url: "https://elsewhere.test/" }), null);
  assert.ok(shapeFinding({ kind: "failed", text: "net::ERR_CONNECTION_REFUSED", url: "http://api.test/x" }));
});

test("a finding loses the URL's query, masks secrets, is cut at 300 characters and honors ignore patterns", () => {
  const scrub = scrubber(["hunter2-secret"]);
  const f = shapeFinding({ kind: "http", status: 500, method: "POST", url: "http://app.test/api/cart?token=abc#x" }, { scrub });
  assert.deepEqual(f, { kind: "http", status: 500, method: "POST", url: "http://app.test/api/cart" });
  const c = shapeFinding({ kind: "console", text: `login failed for hunter2-secret ${"x".repeat(400)}` }, { scrub });
  assert.ok(c.text.startsWith("login failed for ••• "));
  assert.equal(c.text.length, 300);
  const ignore = ignorePatterns(["analytics\\.example", "^ResizeObserver"]);
  assert.equal(shapeFinding({ kind: "failed", text: "net::ERR_FAILED", url: "https://analytics.example/c" }, { ignore }), null);
  assert.equal(shapeFinding({ kind: "console", text: "ResizeObserver loop limit exceeded" }, { ignore }), null);
  assert.throws(() => ignorePatterns(["("]), /not a valid regular expression/);
});

test("findings belong to the step running when they arrived, fold repeats and stop at the caps", () => {
  const http500 = { kind: "http", status: 500, method: "POST", url: "http://app.test/api/cart" };
  const entries = [
    { at: 50, kind: "console", text: "before any step" },
    { at: 110, ...http500 },
    { at: 120, ...http500 },
    { at: 250, kind: "pageerror", text: "TypeError: x is undefined" },
    { at: 260, ...http500 },
  ];
  const { findings, dropped } = groupFindings(entries, [{ step: 1, start: 100 }, { step: 2, start: 200 }]);
  assert.deepEqual(findings, [
    { step: 1, ...http500, count: 2 },
    { step: 2, kind: "pageerror", text: "TypeError: x is undefined", count: 1 },
    { step: 2, ...http500, count: 1 },
  ], "a 500 that lands late stays with the step it arrived in; nothing before the first step");
  assert.equal(dropped, 0);

  const many = Array.from({ length: 60 }, (_, i) => ({ at: 100 + i, kind: "console", text: `error ${i}` }));
  const capped = groupFindings(many, [{ step: 1, start: 100 }]);
  assert.equal(capped.findings.length, 50);
  assert.equal(capped.dropped, 10);
  const run = groupFindings(many, [{ step: 1, start: 100 }], { perStep: 50, perRun: 20 });
  assert.equal(run.findings.length, 20);
});

test("a finding reads as one line, and counts add up repeats", () => {
  withLocale("en", () => {
    assert.equal(describeFinding({ kind: "http", status: 500, method: "POST", url: "http://app.test/api", count: 3 }),
      "500 POST http://app.test/api ×3");
    assert.equal(describeFinding({ kind: "failed", method: "GET", url: "http://api.test/x", text: "net::ERR_FAILED", count: 1 }),
      "GET http://api.test/x failed: net::ERR_FAILED");
    assert.equal(describeFinding({ kind: "console", text: "boom", count: 1 }), "Console error: boom");
    assert.equal(describeFinding({ kind: "pageerror", text: "TypeError: x", count: 1 }), "Uncaught error: TypeError: x");
  });
  assert.deepEqual(countByKind([{ kind: "http", count: 2 }, { kind: "console", count: 1 }]),
    { http: 2, failed: 0, console: 1, pageerror: 0 });
});
