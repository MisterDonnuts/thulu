import { describe, expect, it, vi } from "vitest";

import {
  ago,
  fmtDateMinute,
  fmtDay,
  fmtMs,
  formatSeconds,
  isoLocal,
  runDuration,
  uptime,
} from "./time";

describe("formatSeconds", () => {
  it("climbs one unit at a time", () => {
    expect(formatSeconds(42)).toBe("42s");
    expect(formatSeconds(192)).toBe("3m 12s");
    expect(formatSeconds(7500)).toBe("2h 5m");
    expect(formatSeconds(100_800)).toBe("1d 4h");
  });
});

describe("ago / uptime", () => {
  it("measures from now", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-20T12:00:00Z"));
    expect(ago("2026-09-20T11:58:00Z")).toBe("2m 0s");
    expect(uptime("2026-09-20T11:58:00Z")).toBe("2m 0s");
    vi.useRealTimers();
  });

  it("never goes negative for a clock that is slightly ahead", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-20T12:00:00Z"));
    expect(ago("2026-09-20T12:00:05Z")).toBe("0s");
    vi.useRealTimers();
  });

  it("gives uptime a dash when there is no timestamp", () => {
    expect(uptime()).toBe("—");
    expect(uptime("not a date")).toBe("—");
  });
});

describe("fmtMs", () => {
  it("prints a dash for nothing to report", () => {
    expect(fmtMs(0)).toBe("—");
    expect(fmtMs(NaN)).toBe("—");
  });

  it("rounds milliseconds, then seconds, then minutes", () => {
    expect(fmtMs(419.6)).toBe("420ms");
    expect(fmtMs(6543)).toBe("6.5s");
    expect(fmtMs(187_000)).toBe("3m07s");
  });

  it("carries a second that rounds up into the minute", () => {
    expect(fmtMs(22 * 60_000 + 59_600)).toBe("23m00s");
  });
});

describe("runDuration", () => {
  it("is a dash while the run is still going", () => {
    expect(runDuration("2026-09-20T12:00:00Z")).toBe("—");
  });

  it("stays in milliseconds under a second", () => {
    expect(runDuration("2026-09-20T12:00:00.000Z", "2026-09-20T12:00:00.840Z")).toBe("840ms");
  });

  it("switches to seconds with one decimal", () => {
    expect(runDuration("2026-09-20T12:00:00Z", "2026-09-20T12:00:12.400Z")).toBe("12.4s");
  });
});

describe("isoLocal", () => {
  it("writes the local clock, not UTC", () => {
    const d = new Date(2026, 8, 5, 9, 4);
    expect(isoLocal(d)).toBe("2026-09-05T09:04");
  });
});

describe("fmtDateMinute", () => {
  it("stops at the minute: a schedule's next run has no seconds to show", () => {
    const out = fmtDateMinute("2026-10-19T09:00:00Z");
    expect(out).toMatch(/\d{2}:\d{2}/);
    expect(out).not.toMatch(/\d{2}:\d{2}:\d{2}/);
  });
});

describe("fmtDay", () => {
  it("writes the day alone, without the time", () => {
    const day = fmtDay("2026-10-20T12:00:00Z");
    expect(day).toMatch(/20/);
    expect(day).not.toMatch(/:/);
  });

  it("hands back what it cannot read", () => {
    expect(fmtDay("soon")).toBe("soon");
  });
});
