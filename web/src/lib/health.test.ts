import { describe, expect, it } from "vitest";

import {
  daysUntil,
  expiryState,
  fmtBytes,
  checkLabel,
  fmtGiB,
  healthBadge,
  healthStatTone,
  healthTilesHTML,
  healthTone,
  healthView,
  isEE,
} from "./health";

describe("fmtBytes", () => {
  it("steps through the units", () => {
    expect(fmtBytes(842)).toBe("842 B");
    expect(fmtBytes(1400)).toBe("1.4 KB");
    expect(fmtBytes(2_100_000)).toBe("2.1 MB");
    expect(fmtBytes(3_200_000_000)).toBe("3.2 GB");
  });

  it("writes the decimal mark of the reader's language", () => {
    expect(fmtBytes(48_200_000, "es")).toBe("48,2 MB");
    expect(fmtBytes(48_200_000, "en")).toBe("48.2 MB");
  });

  it("stays in bytes right up to the boundary", () => {
    expect(fmtBytes(999)).toBe("999 B");
    expect(fmtBytes(1000)).toBe("1.0 KB");
  });
});

describe("healthTone", () => {
  it("reads anything that is not ok or warn as a failure", () => {
    expect(healthTone("ok")).toBe("ok");
    expect(healthTone("warn")).toBe("warn");
    expect(healthTone("degraded")).toBe("err");
    expect(healthTone("")).toBe("err");
  });
});

describe("healthBadge", () => {
  it("accepts both spellings of a healthy daemon", () => {
    expect(healthBadge("ok")).toEqual({ label: "OK", tone: "ok" });
    expect(healthBadge("healthy")).toEqual({ label: "OK", tone: "ok" });
  });

  it("names a status that is not healthy, and shows an unknown one verbatim", () => {
    expect(healthBadge("degraded")).toEqual({ label: "Degraded", tone: "danger" });
    expect(healthBadge("melting")).toEqual({ label: "melting", tone: "danger" });
  });
});

describe("healthView", () => {
  it("puts uptime first, then the checks", () => {
    const v = healthView({
      status: "ok",
      uptime: "3d 4h",
      checks: {
        db: { status: "ok", latency: "2ms" },
        queue: { status: "warn", error: "backed up" },
      },
    });
    expect(v.tiles).toEqual([
      { name: "Uptime", value: "3d 4h", detail: "", tone: "ok" },
      { name: "db", value: "OK", detail: "2ms", tone: "ok" },
      { name: "queue", value: "Warning", detail: "backed up", tone: "warn" },
    ]);
  });

  it("reads checks sent as an array too", () => {
    const v = healthView({ status: "ok", checks: [{ name: "db", status: "ok", latency: "2ms" }] });
    expect(v.tiles).toEqual([{ name: "db", value: "OK", detail: "2ms", tone: "ok" }]);
  });

  it("names an array entry that forgot to say what it is", () => {
    const v = healthView({ status: "ok", checks: [{ status: "ok" }] });
    expect(v.tiles[0].name).toBe("?");
  });

  it("prefers the error over the latency as the detail line", () => {
    const v = healthView({
      status: "ok",
      checks: { db: { status: "err", latency: "2ms", error: "refused" } },
    });
    expect(v.tiles[0].detail).toBe("refused");
  });

  it("formats a memory number as bytes and leaves the rest alone", () => {
    const v = healthView({
      status: "ok",
      system: { alloc_mem: 2_100_000, goroutines: 42, go: "1.25" },
    });
    expect(v.system).toEqual([
      { name: "alloc_mem", value: "2.1 MB", detail: "", tone: "ok" },
      { name: "Goroutines", value: "42", detail: "", tone: "ok" },
      { name: "go", value: "1.25", detail: "", tone: "ok" },
    ]);
  });

  it("names the daemon's checks and puts free disk, one decimal, on the disk check", () => {
    const v = healthView({
      status: "degraded",
      checks: { "db:audit": { status: "fail" }, docker: { status: "ok" }, disk: { status: "ok" } },
      system: { go_version: "go1.26.6", disk_free_gb: 41.987654321 },
    });
    expect(v.tiles.map((x) => [x.name, x.value, x.detail])).toEqual([
      ["Database: audit", "Failing", ""],
      ["Docker", "OK", ""],
      ["Disk", "OK", "42.0 GiB free"],
    ]);
    expect(v.system).toEqual([{ name: "Go version", value: "go1.26.6", detail: "", tone: "ok" }]);
  });

  it("keeps the low-disk error next to the free space", () => {
    const v = healthView({
      status: "degraded",
      checks: { disk: { status: "fail", error: "less than 1 GiB free" } },
      system: { disk_free_gb: 0.4 },
    });
    expect(v.tiles[0].detail).toBe("less than 1 GiB free (0.4 GiB free)");
  });

  it("shows free disk as a system number when there is no disk check", () => {
    const v = healthView({ status: "ok", system: { disk_free_gb: 3 } });
    expect(v.system).toEqual([{ name: "Free disk", value: "3.0 GiB", detail: "", tone: "ok" }]);
  });

  it("formats uptime from seconds like the rest of the page, and keeps Go's text from older daemons", () => {
    expect(healthView({ status: "ok", uptime: "16m8s", uptime_seconds: 968 }).tiles[0].value).toBe(
      "16m 8s",
    );
    expect(healthView({ status: "ok", uptime: "16m8s" }).tiles[0].value).toBe("16m8s");
  });

  it("keeps an unknown check or store name as sent", () => {
    expect(checkLabel("queue")).toBe("queue");
    expect(checkLabel("db:newstore")).toBe("Database: newstore");
    expect(fmtGiB(3)).toBe("3.0 GiB");
  });

  it("distinguishes an empty system object from no system at all", () => {
    expect(healthView({ status: "ok", system: {} }).hasSystem).toBe(true);
    expect(healthView({ status: "ok" }).hasSystem).toBe(false);
  });

  it("returns nothing for an empty payload", () => {
    expect(healthView({ status: "ok" })).toEqual({ tiles: [], system: [], hasSystem: false });
  });
});

describe("isEE", () => {
  it("accepts the edition in any case and nothing else", () => {
    expect(isEE({ edition: "ee" })).toBe(true);
    expect(isEE({ edition: "EE" })).toBe(true);
    expect(isEE({ edition: "ce" })).toBe(false);
    expect(isEE({})).toBe(false);
  });
});

describe("daysUntil / expiryState", () => {
  const now = Date.parse("2026-01-01T00:00:00Z");

  it("counts whole days forward", () => {
    expect(daysUntil("2026-01-11T00:00:00Z", now)).toBe(10);
  });

  it("returns null for a date it cannot parse", () => {
    expect(daysUntil("soon", now)).toBeNull();
    expect(expiryState("soon", now)).toEqual({ days: null, expired: false, warn: false });
  });

  it("warns inside a month and reports an expired license", () => {
    expect(expiryState("2026-01-20T00:00:00Z", now)).toEqual({
      days: 19,
      expired: false,
      warn: true,
    });
    expect(expiryState("2026-06-01T00:00:00Z", now).warn).toBe(false);
    expect(expiryState("2025-12-01T00:00:00Z", now).expired).toBe(true);
  });
});

describe("healthTilesHTML", () => {
  it("draws a named grid of small tiles, a failed check in the danger tone", () => {
    const html = healthTilesHTML(
      [
        { name: "Uptime", value: "3h", detail: "", tone: "ok" },
        { name: "Database", value: "down", detail: "<timeout>", tone: "err" },
      ],
      "Checks",
    );
    expect(html.startsWith('<div class="stat-grid" role="group" aria-label="Checks">')).toBe(true);
    expect(html).toContain('<div class="stat stat-sm" data-tone="ok">');
    expect(html).toContain('<div class="stat stat-sm" data-tone="danger">');
    expect(html).toContain("&lt;timeout&gt;");
    expect(html.split("stat-sub").length).toBe(2);
  });

  it("maps every check tone onto a stat tone", () => {
    expect(healthStatTone("ok")).toBe("ok");
    expect(healthStatTone("warn")).toBe("warn");
    expect(healthStatTone("err")).toBe("danger");
  });
});
