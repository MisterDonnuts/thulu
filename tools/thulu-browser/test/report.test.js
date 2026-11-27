import assert from "node:assert/strict";
import { test } from "node:test";

import { caseVerdicts, runVerdict } from "../evidence/cases.js";
import { matches, defaultMatch } from "../evidence/checks.js";
import { pickChapters } from "../evidence/contact-sheet.js";
import { autoLabel } from "../evidence/director.js";
import { slug } from "../evidence/publish.js";
import { plannedChecks } from "../evidence/render.js";
import { buildReport, chaptersVtt, clock } from "../evidence/report.js";
import { ExpiringStore } from "../evidence/store.js";

const run = (over = {}) => ({ baseUrl: "http://app.test/", verdicts: [], notes: [], failures: [], shots: [], ...over });
const report = (over = {}) => buildReport({
  title: "SHOP-1 · coupon", mode: "replay", chapters: [], duration: 12_300, viewport: { width: 1440, height: 900 },
  runs: [run()], attachments: { shots: [] }, ...over,
});

test("the context line names the mode in words", () => {
  assert.match(report({ mode: "compare" }).body, / · before\/after\n/);
  assert.match(report({ mode: "director" }).body, / · director\n/);
});

test("clock and WebVTT timestamps", () => {
  assert.equal(clock(83_400), "1:23.4");
  assert.equal(clock(-5), "0:00.0");
  assert.equal(
    chaptersVtt([{ step: 0, label: "Open", start: 0, end: 1500 }, { step: 1, label: "", start: 1500, end: 3_725_010 }]),
    "WEBVTT\n\n1\n00:00:00.000 --> 00:00:01.500\nOpen\n\n2\n00:00:01.500 --> 01:02:05.010\nStep 2\n",
  );
});

test("a passing report embeds the video and tables the verdicts", () => {
  const { title, body: md } = report({
    runs: [run({ verdicts: [{ step: 1, case: "TC-1", expected: "$90", observed: "$90", pass: true, measured: true }] })],
    chapters: [{ step: 1, label: "Apply", start: 2000, end: 4000 }],
    attachments: { video: { id: "v1", filename: "shop-1.mp4" }, take: { id: "t1", filename: "shop-1.take.json" }, shots: [] },
  });
  assert.equal(title, "SHOP-1 · coupon · pass");
  assert.match(md, /^`http:\/\/app\.test\/` · 1440×900 · 0:12\.3 · replay · 1\/1 checks pass\n/,
    "the Reports card shows the title, so the body opens on the context line");
  assert.doesNotMatch(md, /^## /m);
  assert.match(md, /1\/1 checks pass/);
  assert.match(md, /!\[shop-1\.mp4\]\(\/task-attachments\/v1\)/);
  assert.match(md, /\| pass \| TC-1 \| \$90 \| \$90 \| 0:02\.0 \|/);
  assert.doesNotMatch(md, /¹/);
  assert.match(md, /Replay it: \[shop-1\.take\.json\]\(\/task-attachments\/t1\)/);
});

test("a report marks failures, unmeasured verdicts, findings and missing spots", () => {
  const { title, body: md } = report({
    runs: [run({
      verdicts: [{ step: 0, case: "TC|2", expected: "a", observed: "b", pass: false, measured: false }],
      notes: [{ text: "Header overlaps the menu", level: "issue" }, { text: "Build 42", level: "fact" }],
      failures: [{ step: 3, kind: "click", label: "Save", error: "timeout" }],
      shots: [{ name: "menu", title: "Menu", missing: ["#gone"] }],
    })],
    attachments: { shots: [{ annotated: { id: "s1", filename: "menu.png" }, clean: { id: "s2", filename: "menu-clean.png" } }] },
  });
  assert.equal(title, "SHOP-1 · coupon · fail");
  assert.match(md, /TC\\\|2/);
  assert.match(md, /b ¹/);
  assert.match(md, /⚠️ Header overlaps the menu/);
  assert.match(md, /Step 4 \(Save\) could not be played: timeout/);
  assert.match(md, /- Build 42/);
  assert.match(md, /not found on screen: `#gone`/);
  assert.match(md, /!\[menu\.png\]\(\/task-attachments\/s1\)/);
  assert.match(md, /\[menu-clean\.png\]\(\/task-attachments\/s2\)/);
});

test("a compare report puts both sides of each case in one row", () => {
  const v = (pass, observed) => ({ step: 2, case: "TC-1", expected: "$90", observed, pass, measured: true });
  const { title, body: md } = report({
    mode: "compare",
    runs: [run({ label: "Before", verdicts: [v(false, "$100")] }), run({ label: "After", verdicts: [v(true, "$90")] })],
  });
  assert.match(md, /\| Case \| Expected \| Before \| After \|/);
  assert.match(md, /\| TC-1 \| \$90 \| fail · \$100 \| pass · \$90 \|/);
  assert.equal(title, "SHOP-1 · coupon · pass", "the before side failing is the bug being fixed, not a failed check");
  assert.match(md, /After: 1\/1 checks pass/);
});

test("a report of a take that asks for no timing has no timing or page-error sections", () => {
  const { body } = report({ runs: [run({ metrics: [{ step: 0, response_ms: 300, vitals: { lcp: 900 } }], findings: [] })] });
  assert.doesNotMatch(body, /### Timing|### Page errors/);
  assert.doesNotMatch(body.split("\n")[0], /throttling/);
});

test("a throttled take reports its profile and a timing table; page errors are listed by step", () => {
  const { body } = report({
    chapters: [{ step: 0, label: "Open the cart", start: 0, end: 1000 }, { step: 1, label: "Pay", start: 1000, end: 3200 }],
    runs: [run({
      profile: { network: "slow-4g", cpu_throttle: 4 },
      metrics: [
        { step: 0, label: "Open the cart", kind: "goto", response_ms: 1830, dead: false,
          vitals: { ttfb: 20, fcp: 900, lcp: 1830, cls: 0.02, inp: null } },
        { step: 1, label: "Pay", kind: "click", response_ms: null, dead: true, vitals: { inp: 16, inp_bound: true } },
      ],
      findings: [{ step: 1, kind: "http", status: 500, method: "POST", url: "http://app.test/api/pay", count: 2 }],
    })],
  });
  assert.match(body.split("\n")[0], / · 1440×900 · slow-4g · CPU 4x · 0:12\.3 · replay/);
  assert.match(body, /### Page errors\n\n- `0:01\.0` Pay: 500 POST http:\/\/app\.test\/api\/pay ×2\n/);
  assert.match(body, /### Timing\n\nLab numbers/);
  assert.match(body, /\| Open the cart \| 1\.8 s \| 20 ms \| 900 ms \| 1\.8 s \| 0\.02 \| — \|/);
  assert.match(body, /\| Pay \| none \(dead\) \| — \| — \| — \| — \| 16 ms \|/);
});

test("a perf check on both sides of a before/after shows the difference", () => {
  const v = (value, pass) => ({ step: 1, case: "TC-4", expected: "2500", observed: `${value} ms`, pass, measured: true,
    metric: "lcp", value });
  const { body } = report({
    mode: "compare",
    runs: [run({ label: "Before", verdicts: [v(1400, true)] }), run({ label: "After", verdicts: [v(1820, true)] })],
  });
  assert.match(body, /\| TC-4 \| 2500 \| pass · 1400 ms \| pass · 1820 ms \(\+420 ms\) \|/);
  assert.doesNotMatch(body, /### Timing/, "no measured steps, no table");

  const metrics = (lcp) => [{ step: 0, label: "Open", response_ms: lcp, vitals: { lcp } }];
  const { body: timed } = report({
    mode: "compare",
    runs: [run({ label: "Before", verdicts: [v(1400, true)], metrics: metrics(1400) }),
      run({ label: "After", verdicts: [v(1820, true)], metrics: metrics(1820) })],
  });
  assert.match(timed, /### Timing[\s\S]*\*\*Before\*\*[\s\S]*\*\*After\*\*/, "a perf check asks for the timing section");
});

test("verdicts become one run per case, pass only if every check of the case passes", () => {
  const v = (c, pass, observed) => ({ step: 0, case: c, expected: "x", observed, pass, measured: true });
  assert.deepEqual(caseVerdicts([run({ verdicts: [v("TC-1", true, "$90"), v("TC-2", false, "Error 500"), v("TC-1", false, "3 items"), v(" ", true, "")] })]), [
    { key: "TC-1", verdict: "fail", observed: "$90; 3 items" },
    { key: "TC-2", verdict: "fail", observed: "Error 500" },
  ]);
  assert.deepEqual(caseVerdicts([run()]), []);
});

test("in a before/after only the after side becomes runs", () => {
  const v = (pass, observed) => ({ step: 2, case: "TC-1", expected: "$90", observed, pass, measured: true });
  const runs = [run({ label: "Before", verdicts: [v(false, "$100")] }), run({ label: "After", verdicts: [v(true, "$90")] })];
  assert.deepEqual(caseVerdicts(runs), [{ key: "TC-1", verdict: "pass", observed: "$90" }]);
});

test("a replay cut short by a failed step never records the checks it did not reach as passing", () => {
  const v = (step, c, pass) => ({ step, case: c, expected: "x", observed: `seen ${step}`, pass, measured: true });
  const planned = [{ step: 2, case: "TC-1" }, { step: 5, case: "TC-1" }, { step: 6, case: "TC-2" }, { step: 7, case: "TC-3" }];
  const failures = [{ step: 3, kind: "click", error: "navigation left the allowed hosts" }];
  const r = run({ planned, failures, verdicts: [v(2, "TC-1", true)] });
  assert.deepEqual(caseVerdicts([r]), [
    { key: "TC-1", verdict: "blocked", observed: "seen 2; not reached: step 4 failed (navigation left the allowed hosts)" },
    { key: "TC-2", verdict: "blocked", observed: "not reached: step 4 failed (navigation left the allowed hosts)" },
    { key: "TC-3", verdict: "blocked", observed: "not reached: step 4 failed (navigation left the allowed hosts)" },
  ]);
  const failedFirst = run({ planned, failures, verdicts: [v(2, "TC-1", false)] });
  assert.equal(caseVerdicts([failedFirst])[0].verdict, "fail", "a failed check outweighs one that never ran");
});

test("cut waits show on the context line, in the chapters and in the VTT", () => {
  const chapters = [{ step: 0, label: "Open", start: 0, end: 1500, waitMs: 3200 }, { step: 1, label: "Save", start: 1500, end: 2500 }];
  const { body } = report({
    runs: [run({ gaps: [{ step: 0, ms: 3200, reason: "nav", cut: true }, { step: 0, ms: 900, reason: "check", cut: false }] })],
    chapters,
  });
  assert.match(body, /^`http:\/\/app\.test\/` · 1440×900 · 0:12\.3 · 3\.2 s of waiting cut \(1 wait\) · replay\n/);
  assert.match(body, /- `0:00\.0` Open · ⏱ 3\.2 s\n- `0:01\.5` Save\n/);
  assert.match(chaptersVtt(chapters), /\nOpen · ⏱ 3\.2 s\n\n2\n00:00:01\.500 --> 00:00:02\.500\nSave\n$/);
  assert.match(chaptersVtt([{ step: 0, label: "Open", start: 0, end: 1, waitLeft: 900, waitRight: 0 }]), /Open · ⏱ 900 ms \| —/);
  assert.match(report({ runs: [run({ cutWaits: false, gaps: [] })] }).body, / · 0:12\.3 · waits filmed in real time · replay\n/);
});

test("an a11y verdict without a case is tabled by its label", () => {
  const { body } = report({
    runs: [run({ verdicts: [{ step: 0, case: "", label: "Accessibility scan", expected: "no violation serious or worse",
      observed: "1 critical (label)", pass: false, measured: true }] })],
  });
  assert.match(body, /\| fail \| Accessibility scan \| no violation serious or worse \| 1 critical \(label\) \| — \|/);
});

test("think notes get their own section with the second of the video", () => {
  const { body } = report({
    runs: [run({ notes: [{ step: 1, level: "think", text: "No idea where the cart is" }, { step: 0, level: "fact", text: "Logged in" }] })],
    chapters: [{ step: 0, label: "Open", start: 0, end: 2000 }, { step: 1, label: "Look around", start: 2000, end: 4000 }],
  });
  assert.match(body, /### Think-aloud\n\n- `0:02\.0` No idea where the cart is\n/);
  assert.doesNotMatch(body.split("### Think-aloud")[0], /No idea/, "a think note is not a fact or a finding");
});

test("a long observed is cut to what the daemon takes, the whole text goes in note", () => {
  const long = "a".repeat(200);
  const [c] = caseVerdicts([run({ verdicts: [1, 2].map(() => ({ case: "TC-1", pass: true, observed: long })) })]);
  assert.equal([...c.observed].length, 280);
  assert.ok(c.observed.endsWith("…"));
  assert.equal(c.note, `${long}; ${long}`);
  const [short] = caseVerdicts([run({ verdicts: [{ case: "TC-1", pass: true, observed: "$90" }] })]);
  assert.equal(short.note, undefined, "a short observed needs no note");
});

test("match modes", () => {
  assert.ok(matches("  Total: $90 ", "Total: $90"));
  assert.ok(!matches("Total: $90", "$90"));
  assert.ok(matches("Total: $90", "$90", "contains"));
  assert.ok(matches("Order #123", "#\\d+", "regex"));
  assert.ok(!matches("x", "(", "regex"));
  assert.equal(defaultMatch("url"), "contains");
  assert.equal(defaultMatch("text"), "equals");
});

test("labels written for steps that have none", () => {
  assert.equal(autoLabel({ kind: "press", key: "Enter" }), "Press Enter");
  assert.equal(autoLabel({ kind: "verify", case: "TC-9" }), "Verify TC-9");
});

test("file names from titles", () => {
  assert.equal(slug("SHOP-142 · Canción de Ñandú!"), "shop-142-cancion-de-nandu");
  assert.equal(slug("···"), "evidence");
});

test("the contact sheet spreads a long take over at most twelve cells", () => {
  const many = Array.from({ length: 30 }, (_, i) => ({ step: i }));
  const picked = pickChapters(many);
  assert.equal(picked.length, 12);
  assert.equal(picked[0].step, 0);
  assert.equal(picked.at(-1).step, 29);
  assert.equal(pickChapters(many.slice(0, 5)).length, 5);
});

test("the store expires, caps its size and cleans up what it drops", () => {
  const dropped = [];
  const store = new ExpiringStore({ ttlMs: 1000, max: 2, onEvict: (v) => dropped.push(v) });
  const a = store.put("a");
  store.put("b");
  store.put("c");
  assert.deepEqual(dropped, ["a"]);
  assert.throws(() => store.require(a, "render"), /render '.+' not found or expired/);
  store.sweep(Date.now() + 5000);
  assert.deepEqual(dropped.sort(), ["a", "b", "c"]);
});

test("an a11y step that never judged keeps the run from passing on its other checks", () => {
  const steps = [
    { kind: "goto", url: "/" },
    { kind: "verify", case: "TC-1", expected: "Cart" },
    { kind: "a11y" },
    { kind: "a11y", case: "TC-2" },
  ];
  const planned = plannedChecks(steps);
  assert.deepEqual(planned, [{ step: 1, case: "TC-1" }, { step: 2, case: "" }, { step: 3, case: "TC-2" }]);
  const passed = { step: 1, case: "TC-1", expected: "Cart", observed: "Cart", pass: true, measured: true };
  const a11y = (step, c) => ({ step, case: c, expected: "0", observed: "none", pass: true, measured: true });
  // axe threw on step 2 (its scope matched nothing): a failure, no verdict.
  const failures = [{ step: 2, kind: "a11y", error: "axe failed" }];
  assert.equal(runVerdict([run({ verdicts: [passed, a11y(3, "TC-2")], failures, planned })]), "blocked");
  assert.equal(runVerdict([run({ verdicts: [passed, a11y(2, ""), a11y(3, "TC-2")], planned })]), "pass");
  assert.deepEqual(caseVerdicts([run({ verdicts: [passed], failures: [{ step: 3, kind: "a11y", error: "axe failed" }], planned })])
    .map((c) => [c.key, c.verdict]), [["TC-2", "blocked"], ["TC-1", "pass"]]);
});
