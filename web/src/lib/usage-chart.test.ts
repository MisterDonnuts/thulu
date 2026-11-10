import { describe, expect, it } from "vitest";

import {
  DAY_MS,
  fmtUsageDay,
  usageChartSummary,
  usageDays,
  usageSeries,
  type UsageBucket,
} from "./usage-chart";

const b = (bucket: string, cost: number, tokens = 0): UsageBucket => ({
  bucket,
  cost_usd: cost,
  total_tokens: tokens,
});

describe("usageDays", () => {
  it("fills the days the daemon left out with zeros, across the filter's window", () => {
    const got = usageDays([b("2026-10-02", 1.5, 100), b("2026-10-04T00:00:00Z", 2, 50)], {
      from: "2026-10-01T10:00:00.000Z",
      to: "2026-10-05T09:00:00.000Z",
    });
    expect(got).toEqual([
      { t: "2026-10-01T00:00:00.000Z", cost: 0, tokens: 0 },
      { t: "2026-10-02T00:00:00.000Z", cost: 1.5, tokens: 100 },
      { t: "2026-10-03T00:00:00.000Z", cost: 0, tokens: 0 },
      { t: "2026-10-04T00:00:00.000Z", cost: 2, tokens: 50 },
      { t: "2026-10-05T00:00:00.000Z", cost: 0, tokens: 0 },
    ]);
  });

  it("reads the last 30 days the daemon falls back to when the filter is open", () => {
    const now = Date.parse("2026-10-31T12:00:00Z");
    const got = usageDays([b("2026-10-20", 3)], {}, now);
    expect(got).toHaveLength(31);
    expect(got[0].t).toBe("2026-10-01T00:00:00.000Z");
    expect(got.at(-1)?.t).toBe("2026-10-31T00:00:00.000Z");
    expect(got.find((d) => d.cost > 0)?.t).toBe("2026-10-20T00:00:00.000Z");
  });

  it("widens the window to a bucket outside it and draws a lone day with its neighbours", () => {
    const got = usageDays([b("2026-09-30", 1)], { from: "2026-10-01", to: "2026-10-01" });
    expect(got.map((d) => d.t.slice(0, 10))).toEqual(["2026-09-30", "2026-10-01"]);
  });

  it("has no days without buckets, and skips a bucket it cannot read", () => {
    expect(usageDays([])).toEqual([]);
    expect(usageDays([b("not a date", 1)])).toEqual([]);
  });
});

describe("usageSeries", () => {
  it("reads the metric the control picked", () => {
    const day = { t: "2026-10-01T00:00:00.000Z", cost: 2, tokens: 9 };
    expect(usageSeries("cost").value(day)).toBe(2);
    expect(usageSeries("tokens").value(day)).toBe(9);
    expect(usageSeries("cost").label).toBe("Cost");
    expect(usageSeries("tokens").color).toBe("var(--viz-1)");
  });
});

describe("fmtUsageDay", () => {
  it("prints the UTC day whatever the browser's zone", () => {
    expect(fmtUsageDay(Date.parse("2026-10-03T00:00:00Z"))).toBe("Oct 03");
    expect(fmtUsageDay(Date.parse("2026-10-03T00:00:00Z") + DAY_MS - 1)).toBe("Oct 03");
  });
});

describe("usageChartSummary", () => {
  it("says the metric, how many days and the total", () => {
    const days = usageDays([b("2026-10-01", 1.25), b("2026-10-02", 0.75)], {
      from: "2026-10-01",
      to: "2026-10-02",
    });
    expect(usageChartSummary(days, "cost", (v) => `$${v.toFixed(2)}`)).toBe(
      "Cost over 2 days: $2.00 in total",
    );
    expect(usageChartSummary(days.slice(0, 1), "tokens", String)).toBe(
      "Tokens over 1 day: 0 in total",
    );
  });
});
