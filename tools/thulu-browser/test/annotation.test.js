import assert from "node:assert/strict";
import { after, before, test } from "node:test";

import { chromium } from "playwright";

import { drawAnnotation } from "../evidence/overlays.js";
import { chromiumMissing } from "./chromium.js";

const skip = await chromiumMissing();
let browser;

before(async () => { if (!skip) browser = await chromium.launch(); });
after(async () => { await browser?.close(); });

const LONG = "This note is as long as a note can be, so it wraps over several lines and needs a lot of room. ".repeat(3).slice(0, 300);

function page(fields) {
  return `<!doctype html><html><body style="margin:0;font:16px sans-serif;background:#f4f4f4">${fields.map(([id, x, y, w, h]) =>
    `<div id="${id}" style="position:absolute;left:${x}px;top:${y}px;width:${w}px;height:${h}px;background:#fff;border:1px solid #ccc">${id}</div>`).join("")}</body></html>`;
}

async function annotate(viewport, fields, spots, extra = {}) {
  const p = await browser.newPage({ viewport });
  await p.setContent(page(fields));
  const regions = [];
  for (const s of spots) regions.push({ box: await p.locator(`#${s.id}`).boundingBox(), note: s.note, tone: s.tone ?? "focus" });
  const drawn = await p.evaluate(drawAnnotation, { regions, redactions: [], title: "Contacts", context: ["app.test", "2026-10-14 12:00"],
    notes: [], dim: true, ...extra });
  const g = await p.evaluate(() => {
    const root = document.querySelector("octopus-annotation").shadowRoot;
    const box = (n) => { const r = n.getBoundingClientRect(); return { x: r.x, y: r.y, w: r.width, h: r.height }; };
    const shown = (sel) => [...root.querySelectorAll(sel)].filter((n) => getComputedStyle(n).display !== "none");
    return {
      W: innerWidth, H: innerHeight, crowded: false,
      frames: shown(".frame").map(box),
      nums: shown(".num").map(box),
      bubbles: shown(".bubble").map((n) => ({ ...box(n), text: n.textContent,
        clipped: n.scrollHeight > n.clientHeight + 1 || n.scrollWidth > n.clientWidth + 1 })),
      panels: shown(".head, .notes").map(box),
      listed: shown(".notes li").map((n) => n.textContent),
    };
  });
  await p.close();
  return { ...g, crowded: drawn.crowded };
}

const overlap = (a, b) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
const whole = (a, g) => a.x >= 0 && a.y >= 0 && a.x + a.w <= g.W && a.y + a.h <= g.H;

const assertBeside = (g) => assert.deepEqual(g.listed.filter((t) => /^\d+\. /.test(t)), [], "a note with room went to the list");

function assertNotesClean(g) {
  for (const [i, b] of g.bubbles.entries()) {
    assert.ok(whole(b, g), `note "${b.text}" is cut by the screen edge`);
    assert.ok(!b.clipped, `note "${b.text}" is clipped`);
    for (const f of g.frames) assert.ok(!overlap(b, f), `note "${b.text}" covers a highlighted element`);
    for (const n of g.nums) assert.ok(!overlap(b, n), `note "${b.text}" covers a number`);
    for (const p of g.panels) assert.ok(!overlap(b, p), `note "${b.text}" covers the header or the list`);
    for (const o of g.bubbles.slice(i + 1)) assert.ok(!overlap(b, o), `notes "${b.text}" and "${o.text}" overlap`);
  }
}

function assertClean(g, spots, notes = []) {
  assert.equal(g.crowded, false, "the shot reports itself crowded");
  for (const [i, f] of g.frames.entries()) {
    for (const [j, o] of g.frames.entries()) if (j > i) assert.ok(!overlap(f, o), `frames ${i + 1} and ${j + 1} overlap`);
  }
  for (const [i, b] of g.bubbles.entries()) {
    assert.ok(whole(b, g), `note "${b.text}" is cut by the screen edge`);
    assert.ok(!b.clipped, `note "${b.text}" is clipped`);
    for (const f of g.frames) assert.ok(!overlap(b, f), `note "${b.text}" covers a highlighted element`);
    for (const n of g.nums) assert.ok(!overlap(b, n), `note "${b.text}" covers a number`);
    for (const p of g.panels) assert.ok(!overlap(b, p), `note "${b.text}" covers the header or the list`);
    for (const o of g.bubbles.slice(i + 1)) assert.ok(!overlap(b, o), `notes "${b.text}" and "${o.text}" overlap`);
  }
  for (const [i, n] of g.nums.entries()) {
    assert.ok(whole(n, g), `number ${i + 1} is cut by the screen edge`);
    for (const [j, o] of g.nums.entries()) if (j > i) assert.ok(!overlap(n, o), `numbers ${i + 1} and ${j + 1} overlap`);
    for (const [j, f] of g.frames.entries()) if (j !== i) assert.ok(!overlap(n, f), `number ${i + 1} covers element ${j + 1}`);
    for (const p of g.panels) assert.ok(!overlap(n, p), `number ${i + 1} is under the header or the list`);
  }
  for (const [i, p] of g.panels.entries()) {
    for (const f of g.frames) assert.ok(!overlap(p, f), "the header or the list covers a highlighted element");
    for (const o of g.panels.slice(i + 1)) assert.ok(!overlap(p, o), "the header and the list overlap");
  }
  spots.forEach((s, i) => {
    if (!s.note) return;
    const seen = [...g.bubbles.map((b) => b.text), ...g.listed].filter((t) => t === `${i + 1}. ${s.note}`);
    assert.equal(seen.length, 1, `note ${i + 1} shows ${seen.length} times`);
  });
  for (const n of notes) assert.ok(g.listed.includes(n), `list note "${n}" is missing`);
}

test("two fields side by side keep their notes off each other", { skip }, async () => {
  const fields = [["name", 220, 300, 480, 40], ["email", 722, 290, 470, 66], ["list", 1210, 290, 200, 40]];
  const spots = [
    { id: "name", note: "Prefilled from the account: Vald oen14756, CC'd on Reports." },
    { id: "email", note: "This email decides whether it is still the same person." },
  ];
  const g = await annotate({ width: 1440, height: 900 }, fields, spots);
  assertClean(g, spots);
  assertBeside(g);
});

test("fields at fractional positions still get their notes beside them", { skip }, async () => {
  const fields = [["name", 160, 331.21875, 480, 42], ["email", 662, 331.21875, 480, 42], ["list", 1164, 331.21875, 134, 42]];
  const spots = fields.map(([id]) => ({ id, note: `Note for ${id}, placed beside it.` }));
  const g = await annotate({ width: 1600, height: 900 }, fields, spots, { notes: ["A list note."] });
  assertClean(g, spots, ["A list note."]);
  assertBeside(g);
});

test("three fields in a row, all with notes", { skip }, async () => {
  const fields = [["a", 40, 400, 400, 40], ["b", 452, 400, 400, 40], ["c", 864, 400, 400, 40]];
  const spots = fields.map(([id]) => ({ id, note: `Note for ${id}: what this field proves and why it matters here.` }));
  const g = await annotate({ width: 1280, height: 720 }, fields, spots);
  assertClean(g, spots);
  assertBeside(g);
});

test("a packed form puts the notes with no room beside their field elsewhere", { skip }, async () => {
  const fields = [];
  for (let r = 0; r < 3; r++) for (let c = 0; c < 2; c++) fields.push([`f${r}${c}`, 20 + c * 420, 280 + r * 60, 410, 44]);
  const spots = fields.map(([id]) => ({ id, note: `Note for ${id}: what this field shows and why.`, tone: "warn" }));
  const notes = ["First list note.", "Second list note."];
  const g = await annotate({ width: 870, height: 720 }, fields, spots, { notes });
  assertClean(g, spots, notes);
});

test("a shot with no room left says it is crowded, and its notes still cover nothing", { skip }, async () => {
  const fields = [];
  for (let r = 0; r < 3; r++) for (let c = 0; c < 2; c++) fields.push([`f${r}${c}`, 20 + c * 320, 60 + r * 100, 300, 80]);
  const spots = fields.map(([id]) => ({ id, note: LONG }));
  const notes = Array.from({ length: 6 }, () => LONG);
  const g = await annotate({ width: 660, height: 420 }, fields, spots, { notes });
  assert.equal(g.crowded, true);
  assertNotesClean(g);
});

test("a field under the header moves the header down", { skip }, async () => {
  const fields = [["top", 300, 4, 500, 30], ["mid", 300, 300, 500, 40]];
  const spots = [{ id: "top", note: "At the very top." }, { id: "mid", note: "In the middle." }];
  const g = await annotate({ width: 1280, height: 720 }, fields, spots, { notes: ["A list note."] });
  assertClean(g, spots, ["A list note."]);
  assertBeside(g);
});

test("a field at the bottom moves the list up", { skip }, async () => {
  const fields = [["low", 300, 680, 500, 32]];
  const spots = [{ id: "low", note: "At the very bottom." }];
  const notes = ["One list note.", "Another list note."];
  const g = await annotate({ width: 1280, height: 720 }, fields, spots, { notes });
  assertClean(g, spots, notes);
  assertBeside(g);
});

test("a phone screen keeps every note whole", { skip }, async () => {
  const fields = [["user", 16, 200, 358, 44], ["pass", 16, 252, 358, 44], ["go", 16, 320, 358, 48]];
  const spots = fields.map(([id]) => ({ id, note: `The ${id} control, as the person sees it on a phone.` }));
  assertClean(await annotate({ width: 390, height: 844 }, fields, spots), spots);
});

async function veil(viewport, fields, spots, dim) {
  const p = await browser.newPage({ viewport });
  await p.setContent(page(fields));
  const regions = [];
  for (const s of spots) regions.push({ box: await p.locator(`#${s.id}`).boundingBox(), tone: "focus" });
  await p.evaluate(drawAnnotation, { regions, redactions: [], context: [], notes: [], dim });
  const v = await p.evaluate(() => {
    const root = document.querySelector("octopus-annotation").shadowRoot;
    const svg = root.querySelector("svg.veil");
    if (!svg) return null;
    const box = (n) => { const r = n.getBoundingClientRect(); return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) }; };
    const rects = [...svg.querySelectorAll("mask rect")];
    return {
      isSvg: svg instanceof SVGSVGElement,
      size: box(svg),
      holes: rects.slice(1).map((r) => { const b = r.getBBox(); return { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), h: Math.round(b.height) }; }),
      frames: [...root.querySelectorAll(".frame")].map(box),
      masked: svg.querySelector(":scope > rect").getAttribute("mask"),
    };
  });
  await p.close();
  return v;
}

test("the veil cuts one hole per frame, over the frame", { skip }, async () => {
  const fields = [["a", 100, 120, 300, 40], ["b", 100, 220, 300, 40]];
  const v = await veil({ width: 1280, height: 720 }, fields, [{ id: "a" }, { id: "b" }], true);
  assert.ok(v, "no veil drawn");
  assert.equal(v.isSvg, true, "the veil is not an SVG element");
  assert.deepEqual(v.size, { x: 0, y: 0, w: 1280, h: 720 });
  assert.equal(v.masked, "url(#m)");
  assert.deepEqual(v.holes, v.frames);
});

test("no veil without dim", { skip }, async () => {
  const v = await veil({ width: 1280, height: 720 }, [["a", 100, 120, 300, 40]], [{ id: "a" }], false);
  assert.equal(v, null);
});
