import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPreferences, preferenceSaver } from "./preferences";

describe("loadPreferences", () => {
  it("returns the daemon's map and an empty one for anything else", async () => {
    const prefs = { "tasks.view": { layout: "list" } };
    await expect(loadPreferences(() => Promise.resolve({ preferences: prefs }))).resolves.toEqual(
      prefs,
    );
    for (const body of [null, {}, { preferences: [] }, { preferences: "x" }])
      await expect(loadPreferences(() => Promise.resolve(body))).resolves.toEqual({});
  });

  it("asks the account's endpoint and lets a failure through", async () => {
    const get = vi.fn(() => Promise.resolve({ preferences: {} }));
    await loadPreferences(get);
    expect(get).toHaveBeenCalledWith("/me/preferences");
    await expect(loadPreferences(() => Promise.reject(new Error("down")))).rejects.toThrow("down");
  });
});

describe("preferenceSaver", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("sends only the last value of a quick run of changes", async () => {
    const put = vi.fn(() => Promise.resolve({}));
    const saver = preferenceSaver<{ n: number }>("tasks.view", { put, delay: 400 });
    saver.save({ n: 1 });
    saver.save({ n: 2 });
    await vi.advanceTimersByTimeAsync(399);
    expect(put).not.toHaveBeenCalled();
    saver.save({ n: 3 });
    await vi.advanceTimersByTimeAsync(400);
    expect(put).toHaveBeenCalledTimes(1);
    expect(put).toHaveBeenCalledWith("/me/preferences/tasks.view", { n: 3 });
  });

  it("flushes a queued value at once and sends nothing when none is queued", async () => {
    const put = vi.fn(() => Promise.resolve({}));
    const saver = preferenceSaver<string>("a/b", { put });
    saver.flush();
    expect(put).not.toHaveBeenCalled();
    saver.save("x");
    saver.flush();
    expect(put).toHaveBeenCalledWith("/me/preferences/a%2Fb", "x");
    await vi.advanceTimersByTimeAsync(1000);
    expect(put).toHaveBeenCalledTimes(1);
  });

  it("reports a failed save", async () => {
    const err = new Error("refused");
    const onError = vi.fn();
    const saver = preferenceSaver<number>("k", {
      put: () => Promise.reject(err),
      onError,
    });
    saver.save(1);
    await vi.advanceTimersByTimeAsync(400);
    expect(onError).toHaveBeenCalledWith(err);
  });
});
