import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

async function load() {
  vi.resetModules();
  return import("./api");
}

let status: number;

beforeEach(() => {
  status = 200;
  vi.stubGlobal("fetch", () =>
    Promise.resolve({
      ok: status >= 200 && status < 300,
      status,
      json: () => Promise.resolve(status >= 400 ? { error: "nope" } : {}),
      text: () => Promise.resolve(""),
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("apiWriteCount", () => {
  it("counts each write the daemon accepts, and no read", async () => {
    const { api, apiWriteCount } = await load();
    expect(apiWriteCount()).toBe(0);
    await api.get("/tasks");
    expect(apiWriteCount()).toBe(0);
    await api.post("/tasks", { title: "x" });
    await api.patch("/tasks/1", { title: "y" });
    await api.put("/boards/b", {});
    await api.del("/tasks/1");
    await api.putText("/types/a/prompt", "hi");
    expect(apiWriteCount()).toBe(5);
  });

  it("leaves a rejected write out", async () => {
    const { api, apiWriteCount } = await load();
    status = 409;
    await expect(api.post("/tasks", {})).rejects.toThrow("nope");
    expect(apiWriteCount()).toBe(0);
  });

  it("leaves live-stream subscriptions out", async () => {
    const { api, apiWriteCount } = await load();
    await api.control("/events/s1/subscribe", { topics: ["tasks"] });
    expect(apiWriteCount()).toBe(0);
  });
});
