import { describe, expect, it } from "vitest";

import {
  SANDBOX_DEFAULTS,
  anyAgentUsesSandbox,
  checkPriorities,
  checkSandboxForm,
  killMessage,
  portLinks,
  queueColumns,
  refusedField,
  sandboxColumns,
  sandboxOwner,
  sandboxOwnerName,
  settingNumber,
  taskHref,
  toUnit,
} from "./sandboxes";
import { tableRowHTML } from "./ui/table";

describe("sandboxOwner", () => {
  it("tells a task from a prewarm and an Evidence batch", () => {
    expect(sandboxOwner("t-123")).toEqual({ kind: "task", id: "t-123" });
    expect(sandboxOwner("prewarm:p1")).toEqual({ kind: "prewarm", id: "p1" });
    expect(sandboxOwner("evidence:b9")).toEqual({ kind: "evidence", id: "b9" });
  });

  it("names the owner by the task's title, else its id", () => {
    expect(sandboxOwnerName("t-123", "Migrar pagos al proveedor nuevo")).toBe(
      "Migrar pagos al proveedor nuevo",
    );
    expect(sandboxOwnerName("t-123")).toBe("t-123");
  });

  it("names a prewarm by its project and an Evidence batch by what it is", () => {
    expect(sandboxOwnerName("prewarm:p1", undefined, "webshop")).toBe("Prewarm of webshop");
    expect(sandboxOwnerName("prewarm:p1")).toBe("Prewarm of p1");
    expect(sandboxOwnerName("evidence:b9", "ignored")).toBe("Evidence batch");
  });

  it("links a task the way the board opens it", () => {
    expect(taskHref("a b")).toBe("/tasks/#task-a%20b");
  });
});

describe("settingNumber", () => {
  it("keeps a stored 0 instead of reading it as unset", () => {
    expect(settingNumber(0, SANDBOX_DEFAULTS.idleEvictSeconds)).toBe(0);
  });

  it("falls back on null, absent and non-numbers", () => {
    expect(settingNumber(null, 3)).toBe(3);
    expect(settingNumber(undefined, 3)).toBe(3);
    expect(settingNumber("5", 3)).toBe(3);
  });
});

describe("toUnit", () => {
  it("shows seconds as minutes and ms as seconds", () => {
    expect(toUnit(1800, 60)).toBe("30");
    expect(toUnit(120_000, 1000)).toBe("120");
  });

  it("keeps one decimal for a value saved in odd seconds", () => {
    expect(toUnit(90, 60, "en")).toBe("1.5");
    expect(toUnit(30, 60, "es")).toBe("0,5");
  });

  it("writes no thousands separator, so the form reads its own value back", () => {
    expect(toUnit(1_200_000, 60, "es")).toBe("20000");
    const r = checkSandboxForm({ max: "3", idleMinutes: toUnit(30, 60, "es"), waitSeconds: "0" });
    expect(r.ok).toBe(true);
  });
});

describe("checkSandboxForm", () => {
  it("converts to the units the daemon stores", () => {
    expect(checkSandboxForm({ max: "4", idleMinutes: "1.5", waitSeconds: "2" })).toEqual({
      ok: true,
      values: { max: 4, idleEvictSeconds: 90, acquireBlockMs: 2000 },
    });
  });

  it("accepts a decimal comma", () => {
    const r = checkSandboxForm({ max: "1", idleMinutes: "0,5", waitSeconds: "0" });
    expect(r.ok && r.values.idleEvictSeconds).toBe(30);
  });

  it("names every bad field at once", () => {
    expect(checkSandboxForm({ max: "0", idleMinutes: "", waitSeconds: "-1" })).toEqual({
      ok: false,
      problems: { max: "min1", idleMinutes: "required", waitSeconds: "min0" },
    });
  });

  it("wants a whole number of sandboxes and of seconds", () => {
    expect(checkSandboxForm({ max: "2.5", idleMinutes: "1", waitSeconds: "1" })).toEqual({
      ok: false,
      problems: { max: "integer" },
    });
    expect(checkSandboxForm({ max: "x", idleMinutes: "1", waitSeconds: "1" })).toEqual({
      ok: false,
      problems: { max: "number" },
    });
    expect(checkSandboxForm({ max: "2", idleMinutes: "1", waitSeconds: "1.5" })).toEqual({
      ok: false,
      problems: { waitSeconds: "integer" },
    });
  });
});

describe("checkPriorities", () => {
  it("reads whole numbers, negatives included", () => {
    expect(checkPriorities({ backlog: "5", done: "-2", review: " 0 " })).toEqual({
      ok: true,
      values: { backlog: 5, done: -2, review: 0 },
    });
  });

  it("names an emptied or bad box instead of saving 0", () => {
    expect(checkPriorities({ backlog: "", done: "e", review: "1.5", dev: "3" })).toEqual({
      ok: false,
      problems: { backlog: "required", done: "number", review: "integer" },
    });
  });
});

describe("refusedField", () => {
  it("maps the refused key to its field and the code to a problem", () => {
    expect(
      refusedField({
        error: "sandbox.idle_evict_seconds tiene que ser un entero no negativo",
        code: "api.settings.errors.non_negative_int",
        setting: "sandbox.idle_evict_seconds",
      }),
    ).toEqual({ field: "idleMinutes", problem: "whole_min0" });
    expect(
      refusedField({ code: "api.settings.errors.sandbox_max_positive", setting: "sandbox.max" }),
    ).toEqual({ field: "max", problem: "min1" });
  });

  it("falls back to a plain refusal for a code it does not know", () => {
    expect(refusedField({ code: "x", setting: "sandbox.acquire_block_ms" })).toEqual({
      field: "waitSeconds",
      problem: "refused",
    });
  });

  it("is null for a refusal that names no field of the form", () => {
    expect(refusedField({ code: "x", setting: "chat.max" })).toBeNull();
    expect(refusedField({ error: "boom" })).toBeNull();
    expect(refusedField(undefined)).toBeNull();
  });
});

describe("anyAgentUsesSandbox", () => {
  it("finds thulu-sandbox on any stage, with or without a suffix", () => {
    expect(
      anyAgentUsesSandbox([
        { name: "a", mcp_by_stage: { build: ["thulu-github@write"] } },
        { name: "b", mcp_by_stage: { review: ["thulu-sandbox@write+exec"] } },
      ]),
    ).toBe(true);
  });

  it("is false when no agent ticks it", () => {
    expect(anyAgentUsesSandbox([{ name: "a", mcp_by_stage: { build: ["thulu-github"] } }])).toBe(
      false,
    );
    expect(anyAgentUsesSandbox([{ name: "a" }])).toBe(false);
    expect(anyAgentUsesSandbox([])).toBe(false);
  });
});

describe("portLinks", () => {
  const sb = {
    task_id: "t",
    owner_id: "t",
    status: "ready",
    idle_seconds: 0,
    host_ports: { "5173": 41001, "3000": 41000 },
    urls: { "5173": "http://h:41001", "3000": "http://h:41000" },
    publishes_ports: true,
  };

  it("lists the addresses lowest port first", () => {
    expect(portLinks(sb)).toEqual([
      { port: 3000, url: "http://h:41000" },
      { port: 5173, url: "http://h:41001" },
    ]);
  });

  it("has nothing to open on Kubernetes", () => {
    expect(portLinks({ ...sb, publishes_ports: false })).toEqual([]);
  });
});

describe("killMessage", () => {
  it("says the same thing from the task and from the sandbox page, with the volume only on Kubernetes", () => {
    const docker = killMessage("Migrar pagos al proveedor nuevo", true);
    const kube = killMessage("Migrar pagos al proveedor nuevo", false);
    expect(docker).toContain("Migrar pagos al proveedor nuevo");
    expect(docker).toContain("next command");
    expect(docker).not.toContain("Kubernetes");
    expect(kube.startsWith(docker)).toBe(true);
    expect(kube).toContain("Kubernetes");
  });
});

describe("sandboxColumns and queueColumns", () => {
  it("give each cell its hook class, its alignment and a named actions column", () => {
    const cols = sandboxColumns();
    expect(cols).toHaveLength(6);
    expect(cols.map((c) => c.cls)).toEqual([
      "owner",
      "ctn",
      "status-cell",
      "idle",
      "ports",
      undefined,
    ]);
    expect(cols[5].hideLabel).toBe(true);
    const row = tableRowHTML(cols, { cells: ["", "", "", "", "", ""] }, { stack: true });
    expect(row).toContain('<td class="num mono cell-shrink idle" data-label="Idle">');
    expect(row).toContain('<td class="cell-shrink"></td>');
  });

  it("number the queue in a right-aligned column", () => {
    const cols = queueColumns();
    expect(cols).toHaveLength(6);
    expect(cols[0]).toMatchObject({ num: true, cls: "pos" });
    expect(cols.map((c) => c.cls)).toContain("wait");
  });
});
