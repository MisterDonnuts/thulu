import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { frameQueue } from "./framequeue";

let frames: Array<() => void> = [];
let now = 0;
const noop = () => undefined;

beforeEach(() => {
  frames = [];
  now = 0;
  vi.stubGlobal("requestAnimationFrame", (cb: () => void) => frames.push(cb));
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    frames[id - 1] = noop;
  });
  vi.spyOn(performance, "now").mockImplementation(() => now);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function runFrame() {
  const f = frames.shift();
  f?.();
}

describe("frameQueue", () => {
  it("draws nothing until a frame, then everything that fits, with one after per frame", () => {
    const drawn: number[] = [];
    let afters = 0;
    const q = frameQueue<number>({ each: (n) => drawn.push(n), after: () => afters++ });
    for (let i = 0; i < 5; i++) q.push(i);
    expect(drawn).toEqual([]);
    expect(frames).toHaveLength(1);
    runFrame();
    expect(drawn).toEqual([0, 1, 2, 3, 4]);
    expect(afters).toBe(1);
    expect(frames).toHaveLength(0);
  });

  it("spreads a backlog that outlasts the budget over several frames, in order", () => {
    const drawn: number[] = [];
    let afters = 0;
    const q = frameQueue<number>({
      each: (n) => {
        drawn.push(n);
        now += 3;
      },
      after: () => afters++,
    });
    for (let i = 0; i < 7; i++) q.push(i);
    runFrame();
    expect(drawn).toEqual([0, 1, 2]);
    runFrame();
    expect(drawn).toEqual([0, 1, 2, 3, 4, 5]);
    runFrame();
    expect(drawn).toEqual([0, 1, 2, 3, 4, 5, 6]);
    expect(afters).toBe(3);
    expect(frames).toHaveLength(0);
  });

  it("clear drops what wasn't drawn; later pushes still draw", () => {
    const drawn: string[] = [];
    const q = frameQueue<string>({ each: (s) => drawn.push(s) });
    q.push("old");
    q.clear();
    q.push("new");
    runFrame();
    expect(drawn).toEqual(["new"]);
  });

  it("stop cancels the pending frame and ignores later pushes", () => {
    const drawn: string[] = [];
    const q = frameQueue<string>({ each: (s) => drawn.push(s) });
    q.push("a");
    q.stop();
    q.push("b");
    runFrame();
    expect(drawn).toEqual([]);
  });

  it("an item that throws doesn't stall the ones behind it", () => {
    const drawn: string[] = [];
    vi.spyOn(console, "error").mockImplementation(noop);
    const q = frameQueue<string>({
      each: (s) => {
        if (s === "bad") throw new Error("boom");
        drawn.push(s);
      },
    });
    q.push("bad");
    q.push("good");
    runFrame();
    expect(drawn).toEqual(["good"]);
  });
});
