import { describe, expect, it } from "vitest";
import {
  fmtDur,
  fmtLatency,
  fmtNum,
  fmtPct,
  fmtUSD,
  fmtWhen,
  presetRange,
  toInstant,
} from "./metrics-format";

describe("fmtUSD", () => {
  it("prints a bare $0 for nothing", () => {
    expect(fmtUSD(0)).toBe("$0");
    expect(fmtUSD(NaN)).toBe("$0");
  });
  it("keeps four decimals below a cent", () => {
    expect(fmtUSD(0.0004)).toBe("$0.0004");
  });
  it("keeps three decimals below a dollar", () => {
    expect(fmtUSD(0.125)).toBe("$0.125");
  });
  it("prints cents above a dollar", () => {
    expect(fmtUSD(12.4)).toBe("$12.40");
  });
});

describe("fmtNum", () => {
  it("rounds small counts", () => {
    expect(fmtNum(0)).toBe("0");
    expect(fmtNum(41.6)).toBe("42");
  });
  it("abbreviates thousands and millions", () => {
    expect(fmtNum(1_200)).toBe("1.2k");
    expect(fmtNum(3_400_000)).toBe("3.4M");
  });
  it("gives an em dash when there is no number", () => {
    expect(fmtNum(Infinity)).toBe("—");
    expect(fmtNum(NaN)).toBe("—");
  });
});

describe("fmtPct", () => {
  it("prints one decimal", () => {
    expect(fmtPct(0.873)).toBe("87.3%");
    expect(fmtPct(0)).toBe("0.0%");
  });
  it("gives an em dash for an undefined ratio", () => {
    expect(fmtPct(NaN)).toBe("—");
  });
});

describe("fmtDur", () => {
  it("has no answer for zero or negative", () => {
    expect(fmtDur(0)).toBe("—");
    expect(fmtDur(-5)).toBe("—");
  });
  it("switches unit with magnitude", () => {
    expect(fmtDur(1_500)).toBe("1.5s");
    expect(fmtDur(45_000)).toBe("45s");
    expect(fmtDur(90_000)).toBe("1.5m");
    expect(fmtDur(3_600_000)).toBe("1.0h");
    expect(fmtDur(48 * 3_600_000)).toBe("2.0d");
  });
});

describe("fmtLatency", () => {
  it("keeps sub-second detail that fmtDur would round away", () => {
    expect(fmtDur(39)).toBe("0.0s");
    expect(fmtLatency(39)).toBe("39.0ms");
    expect(fmtLatency(0.5)).toBe("0.50ms");
    expect(fmtLatency(250)).toBe("250ms");
  });
  it("hands seconds and above to fmtDur", () => {
    expect(fmtLatency(1_500)).toBe("1.5s");
  });
  it("refuses negatives", () => {
    expect(fmtLatency(-1)).toBe("—");
  });
});

describe("fmtWhen", () => {
  it("has an em dash for absent and unparseable input", () => {
    expect(fmtWhen()).toBe("—");
    expect(fmtWhen("not a date")).toBe("—");
  });
  it("renders a real timestamp", () => {
    expect(fmtWhen("2026-07-31T17:49:00Z")).not.toBe("—");
  });
});

describe("toInstant", () => {
  it("resolves a bare wall clock against the local zone", () => {
    const local = new Date(2026, 6, 31, 17, 49);
    expect(toInstant("2026-07-31T17:49")).toBe(local.toISOString());
  });
  it("returns an empty string for junk, so the query param is dropped", () => {
    expect(toInstant("")).toBe("");
    expect(toInstant("tomorrow")).toBe("");
  });
});

describe("presetRange", () => {
  const now = new Date(2026, 6, 31, 12, 0, 0);
  it("ends at now", () => {
    expect(presetRange("7d", now).to.getTime()).toBe(now.getTime());
  });
  it("walks back by the named span", () => {
    expect(presetRange("24h", now).from).toEqual(new Date(2026, 6, 30, 12, 0, 0));
    expect(presetRange("7d", now).from).toEqual(new Date(2026, 6, 24, 12, 0, 0));
    expect(presetRange("90d", now).from).toEqual(new Date(2026, 4, 2, 12, 0, 0));
  });
  it("falls back to 30 days for anything it does not know", () => {
    expect(presetRange("30d", now).from).toEqual(new Date(2026, 6, 1, 12, 0, 0));
    expect(presetRange("nonsense", now).from).toEqual(presetRange("30d", now).from);
  });
});
