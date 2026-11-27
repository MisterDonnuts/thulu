// The app's waits in a filmed replay: cut and marked by default, filmed with cut_waits:false; and an
// accessibility step judged inside the take.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import http from "node:http";
import { after, before, test } from "node:test";

import { chromiumMissing } from "./chromium.js";

async function missing() {
  const reason = await chromiumMissing();
  if (reason) return reason;
  try { execFileSync(process.env.FFMPEG_PATH || "ffmpeg", ["-version"], { stdio: "ignore" }); } catch { return "ffmpeg is not installed"; }
  return false;
}
const skip = await missing();

const SLOW_MS = 1500;
const page = `<!doctype html><html lang="en"><title>Slow</title>
<body style="font:16px sans-serif;padding:32px">
  <main>
    <h1>Report</h1>
    <p id="ready">Loaded</p>
    <form id="unlabelled"><input id="q" type="text"></form>
  </main>
</body></html>`;

let app, mod;

before(async () => {
  if (skip) return;
  app = await new Promise((resolve) => {
    const s = http.createServer((req, res) => {
      if (req.url === "/favicon.ico") { res.statusCode = 404; return res.end(); }
      res.setHeader("content-type", "text/html");
      // The page answers late: the wait the video should cut and mark.
      setTimeout(() => res.end(page), req.url.startsWith("/slow") ? SLOW_MS : 0);
    }).listen(0, "127.0.0.1", () => resolve(s));
  });
  mod = {
    ...(await import("../evidence/render.js")),
    ...(await import("../evidence/schema.js")),
    ...(await import("../evidence/bundle.js")),
    ...(await import("../evidence/report.js")),
    ...(await import("../lib/browser.js")),
  };
});

after(async () => {
  if (skip) return;
  await mod.closeBrowser();
  app.close();
});

const origin = () => `http://127.0.0.1:${app.address().port}`;
const take = (over = {}) => mod.parseTake({
  version: 1, title: "Slow report", base_url: origin(), viewport: { width: 640, height: 400 },
  steps: [
    { kind: "goto", url: "/slow", label: "Open the report" },
    { kind: "beat", ms: 200 },
  ],
  ...over,
});

test("a slow navigation is cut from the video and marked on its chapter", { skip }, async () => {
  const b = await mod.renderTake(take(), { library: {}, pace: 0.3 });
  const [run] = b.runs;
  const nav = run.gaps.find((g) => g.reason === "nav");
  assert.ok(nav, `a nav gap was recorded: ${JSON.stringify(run.gaps)}`);
  assert.equal(nav.cut, true);
  assert.equal(nav.step, 0);
  assert.ok(nav.ms >= SLOW_MS - 300, `the gap counts the app's wait, got ${nav.ms} ms`);
  const chapter = b.chapters.find((c) => c.step === 0);
  assert.ok(chapter.waitMs >= SLOW_MS - 300, "the chapter carries the wait cut from it");
  assert.match(mod.chaptersVtt(b.chapters), /Open the report · ⏱ \d/);
  assert.match(mod.describeBundle("r1", b), /of waiting cut from the video \(1 wait/);
  assert.ok(b.duration < SLOW_MS, `the cut video is shorter than the wait (${b.duration} ms)`);
});

test("cut_waits:false films the wait in real time and says so", { skip }, async () => {
  const b = await mod.renderTake(take({ cut_waits: false }), { library: {}, pace: 0.3 });
  const [run] = b.runs;
  assert.equal(run.cutWaits, false);
  assert.ok(run.gaps.every((g) => !g.cut), "nothing is cut");
  assert.ok(b.duration >= SLOW_MS - 300, `the video holds the wait (${b.duration} ms)`);
  assert.equal(b.chapters.find((c) => c.step === 0).waitMs, undefined, "nothing cut, nothing marked");
  assert.match(mod.describeBundle("r2", b), /Waits filmed in real time/);
});

test("an a11y step is a check: it fails on an unlabelled input and keeps the summary", { skip }, async () => {
  const b = await mod.renderTake(take({
    steps: [
      { kind: "goto", url: "/" },
      { kind: "a11y", case: "TC-9", include: ["#unlabelled"] },
    ],
  }), { library: {}, pace: 0.3 });
  const [v] = b.runs[0].verdicts;
  assert.equal(v.case, "TC-9");
  assert.equal(v.pass, false);
  assert.equal(v.measured, true);
  assert.ok(v.a11y.failing_ids.includes("label"), `axe names the label rule: ${v.a11y.failing_ids}`);
  assert.equal(v.expected, "no violation serious or worse");
  assert.match(v.observed, /label/);
  assert.match(mod.describeBundle("r3", b), /FAIL TC-9/);
});
