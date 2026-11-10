import assert from "node:assert/strict";
import { test } from "node:test";

import { Reel, align, concatScript, frameAt } from "../evidence/reel.js";

const seg = (step, label, start, end, ...ts) => ({ step, label, start, end, frames: ts.map((t, i) => ({ file: `${label}${i}.jpg`, ts: t })) });

test("each frame lasts until the next one, the last until its segment ends", () => {
  const reel = new Reel([seg(0, "a", 1000, 2000, 1000, 1300), seg(1, "b", 5000, 5500, 5000)]);
  assert.deepEqual(reel.entries().map((e) => [e.file, e.duration]), [["a0.jpg", 300], ["a1.jpg", 700], ["b0.jpg", 500]]);
  assert.equal(reel.duration, 1500);
});

test("time between segments is cut, and empty segments disappear", () => {
  const reel = new Reel([seg(0, "a", 0, 100, 0), seg(1, "x", 200, 200), seg(1, "b", 90_000, 90_400, 90_000)]);
  assert.equal(reel.segments.length, 2);
  assert.deepEqual(reel.chapters(), [
    { step: 0, label: "a", start: 0, end: 100 },
    { step: 1, label: "b", start: 100, end: 500 },
  ]);
});

test("consecutive segments of one step make one chapter", () => {
  const reel = new Reel([seg(0, "a", 0, 100, 0), seg(0, "a", 500, 700, 500), seg(1, "b", 900, 1000, 900)]);
  assert.deepEqual(reel.chapters().map((c) => [c.step, c.start, c.end]), [[0, 0, 300], [1, 300, 400]]);
});

test("frameAt finds the frame on screen at an output time", () => {
  const reel = new Reel([seg(0, "a", 0, 1000, 0, 400)]);
  assert.equal(reel.frameAt(0), "a0.jpg");
  assert.equal(reel.frameAt(399), "a0.jpg");
  assert.equal(reel.frameAt(400), "a1.jpg");
  assert.equal(reel.frameAt(5000), "a1.jpg");
  assert.equal(frameAt([], 10), undefined);
});

test("align holds the faster side so both reach every step together", () => {
  const before = new Reel([seg(0, "open", 0, 1000, 0), seg(1, "save", 2000, 2300, 2000)]);
  const after = new Reel([seg(0, "open", 0, 400, 0), seg(1, "save", 1000, 1900, 1000)]);
  const { left, right, chapters } = align(before, after);
  const total = (xs) => xs.reduce((s, e) => s + e.duration, 0);
  assert.equal(total(left), total(right));
  assert.deepEqual(chapters.map((c) => [c.step, c.start, c.end]), [[0, 0, 1000], [1, 1000, 1900]]);
});

test("a step one side never reached shows that side frozen", () => {
  const before = new Reel([seg(0, "open", 0, 500, 0)]);
  const after = new Reel([seg(0, "open", 0, 500, 0), seg(1, "save", 600, 900, 600)]);
  const { left, right } = align(before, after);
  assert.equal(left.at(-1).file, "open0.jpg");
  assert.equal(left.at(-1).step, 1);
  assert.equal(left.reduce((s, e) => s + e.duration, 0), right.reduce((s, e) => s + e.duration, 0));
});

test("the concat script quotes paths and repeats the last file", () => {
  const s = concatScript([{ file: "/t/it's.jpg", duration: 250 }, { file: "/t/b.jpg", duration: 1000 }]);
  assert.equal(s, "ffconcat version 1.0\nfile '/t/it'\\''s.jpg'\nduration 0.2500\nfile '/t/b.jpg'\nduration 1.0000\nfile '/t/b.jpg'\n");
  assert.throws(() => concatScript([]), /nothing was filmed/);
});
