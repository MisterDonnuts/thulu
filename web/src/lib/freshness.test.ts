import { describe, expect, it, vi } from "vitest";

import { freshState, keepFresh, staleNotice } from "./freshness";

describe("keepFresh", () => {
  it("returns the fetched value and clears staleness on success", async () => {
    const f = freshState();
    const got = await keepFresh({
      current: ["old"],
      fetch: () => Promise.resolve(["new"]),
      freshness: f,
      now: () => 1000,
    });
    expect(got).toEqual(["new"]);
    expect(f.stale).toBe(false);
    expect(f.failures).toBe(0);
    expect(f.lastSuccess).toBe(1000);
  });

  it("keeps the previous value when the fetch fails", async () => {
    const f = freshState();
    const current = ["task-1", "task-2"];
    await expect(
      keepFresh({ current, fetch: () => Promise.reject(new Error("boom")), freshness: f }),
    ).rejects.toThrow("boom");
    expect(current).toEqual(["task-1", "task-2"]);
    expect(f.stale).toBe(true);
    expect(f.failures).toBe(1);
    expect(f.lastError).toBe("boom");
  });

  it("re-throws so the caller's poller can back off", async () => {
    const f = freshState();
    const fetch = vi.fn(() => Promise.reject(new Error("down")));
    await expect(keepFresh({ current: [], fetch, freshness: f })).rejects.toThrow("down");
    expect(fetch).toHaveBeenCalledOnce();
  });

  it("counts consecutive failures and resets on recovery", async () => {
    const f = freshState();
    const fail = () => Promise.reject(new Error("nope"));
    for (const _ of [0, 1, 2]) {
      await expect(keepFresh({ current: 0, fetch: fail, freshness: f })).rejects.toThrow();
    }
    expect(f.failures).toBe(3);
    expect(f.stale).toBe(true);

    const got = await keepFresh({
      current: 0,
      fetch: () => Promise.resolve(42),
      freshness: f,
      now: () => 5,
    });
    expect(got).toBe(42);
    expect(f.failures).toBe(0);
    expect(f.stale).toBe(false);
    expect(f.lastSuccess).toBe(5);
  });

  it("handles a rejection that is not an Error", async () => {
    const f = freshState();
    const notAnError: unknown = { toString: () => "weird failure" };
    await expect(
      keepFresh({
        current: null,
        fetch: () => Promise.reject(notAnError as Error),
        freshness: f,
      }),
    ).rejects.toBeTruthy();
    expect(f.lastError).toBe("weird failure");
    expect(f.stale).toBe(true);
  });

  it("does not mark stale when the fetch resolves to an empty result", async () => {
    const f = freshState();
    const got = await keepFresh({
      current: [1, 2],
      fetch: () => Promise.resolve([]),
      freshness: f,
    });
    expect(got).toEqual([]);
    expect(f.stale).toBe(false);
  });
});

describe("staleNotice", () => {
  it("is empty while fresh", () => {
    expect(staleNotice(freshState())).toBe("");
  });

  it("says so plainly when nothing ever loaded", () => {
    const f = { stale: true, failures: 1, lastError: "x", lastSuccess: 0 };
    expect(staleNotice(f)).toBe("Can't reach the daemon.");
  });

  it("reports the age of the data in seconds, minutes and hours", () => {
    const base = 1_700_000_000_000;
    const f = { stale: true, failures: 1, lastError: "x", lastSuccess: base };
    expect(staleNotice(f, base + 30_000)).toContain("30s ago");
    expect(staleNotice(f, base + 5 * 60_000)).toContain("5m ago");
    expect(staleNotice(f, base + 3 * 3_600_000)).toContain("3h ago");
  });

  it("never reports a negative age when clocks disagree", () => {
    const f = { stale: true, failures: 1, lastError: "x", lastSuccess: 10_000 };
    expect(staleNotice(f, 0)).toContain("0s ago");
  });
});
