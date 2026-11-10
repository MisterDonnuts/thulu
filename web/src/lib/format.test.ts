import { describe, expect, it } from "vitest";

import {
  costHTML,
  estimatedCost,
  fmtCount,
  fmtDecimal,
  fmtPct,
  fmtTokens,
  fmtUsd,
  fmtUsdCompact,
  usd,
} from "./format";

describe("fmtUsd", () => {
  it("always prints two decimals", () => {
    expect(fmtUsd(0)).toBe("$0.00");
    expect(fmtUsd(12.4)).toBe("$12.40");
    expect(fmtUsd(1234.567)).toBe("$1,234.57");
  });

  it("reads NaN and undefined-ish numbers as zero", () => {
    expect(fmtUsd(NaN)).toBe("$0.00");
  });
});

describe("usd", () => {
  it("writes the amount with the UI language's separators", () => {
    expect(usd(2.13, 2)).toBe("$2.13");
    expect(usd(0.636, 3)).toBe("$0.636");
    expect(usd(12_400, 2)).toBe("$12,400.00");
  });
});

describe("fmtUsdCompact", () => {
  it("keeps four decimals under a dollar so per-turn costs survive", () => {
    expect(fmtUsdCompact(0.0004)).toBe("$0.0004");
    expect(fmtUsdCompact(0.42)).toBe("$0.4200");
  });

  it("switches to cents at a dollar and to thousands at a thousand", () => {
    expect(fmtUsdCompact(1)).toBe("$1.00");
    expect(fmtUsdCompact(999.994)).toBe("$999.99");
    expect(fmtUsdCompact(1000)).toBe("$1.0k");
    expect(fmtUsdCompact(12_400)).toBe("$12.4k");
  });

  it("does not print Infinity", () => {
    expect(fmtUsdCompact(0)).toBe("$0.00");
    expect(fmtUsdCompact(Infinity)).toBe("$0.00");
    expect(fmtUsdCompact(NaN)).toBe("$0.00");
  });
});

describe("fmtTokens", () => {
  it("counts raw below a thousand", () => {
    expect(fmtTokens(0)).toBe("0");
    expect(fmtTokens(999)).toBe("999");
  });

  it("abbreviates thousands and millions with one decimal", () => {
    expect(fmtTokens(1000)).toBe("1.0k");
    expect(fmtTokens(12_345)).toBe("12.3k");
    expect(fmtTokens(3_400_000)).toBe("3.4M");
  });
});

describe("decimal mark", () => {
  it("writes Spanish numbers with a comma, like the money beside them", () => {
    expect(fmtDecimal(2.25, 1, "es")).toBe("2,3");
    expect(fmtTokens(2_200_000, "es")).toBe("2,2M");
    expect(fmtTokens(412_000, "es")).toBe("412,0k");
    expect(fmtCount(3_450_000, "es")).toBe("3,45M");
    expect(fmtCount(1_500, "es")).toBe("1,5k");
    expect(fmtPct(0.873, "es")).toBe("87,3%");
  });

  it("keeps the point in English", () => {
    expect(fmtDecimal(2.25, 1, "en")).toBe("2.3");
    expect(fmtTokens(2_200_000, "en")).toBe("2.2M");
  });
});

describe("fmtCount", () => {
  it("goes one magnitude further than fmtTokens", () => {
    expect(fmtCount(1_200_000_000)).toBe("1.20B");
    expect(fmtCount(3_450_000)).toBe("3.45M");
    expect(fmtCount(1_500)).toBe("1.5k");
  });

  it("groups small numbers instead of abbreviating them", () => {
    expect(fmtCount(999)).toBe((999).toLocaleString());
  });
});

describe("fmtPct", () => {
  it("turns a ratio into a percentage with one decimal", () => {
    expect(fmtPct(0)).toBe("0.0%");
    expect(fmtPct(0.873)).toBe("87.3%");
    expect(fmtPct(1)).toBe("100.0%");
  });
});

describe("estimatedCost", () => {
  it("leaves a reported cost alone", () => {
    expect(estimatedCost(4.73)).toEqual({ text: "$4.73", title: "" });
    expect(estimatedCost(4.73, 0)).toEqual({ text: "$4.73", title: "" });
  });

  it("marks a cost that is partly an estimate and says how much", () => {
    const c = estimatedCost(4.73, 2.92);
    expect(c.text).toBe("≈$4.73");
    expect(c.title).toContain("$2.92");
  });

  it("writes both amounts with the formatter it is given", () => {
    const c = estimatedCost(4.73, 2.92, fmtUsd);
    expect(c.text).toBe("≈$4.73");
    expect(c.title).toContain("$2.92");
    expect(estimatedCost(0.5, 0, fmtUsd).text).toBe("$0.50");
  });
});

describe("costHTML", () => {
  it("is the bare amount when nothing was estimated", () => {
    expect(costHTML(1.61)).toBe("$1.61");
  });

  it("wraps an estimated cost in an .est span with the tooltip", () => {
    const html = costHTML(2.92, 2.92);
    expect(html).toMatch(/^<span class="est" title="[^"]*\$2\.92[^"]*">≈\$2\.92<\/span>$/);
  });
});

describe("unpriced sessions", () => {
  it("says a cost with no price is missing, not $0", () => {
    const c = estimatedCost(0, 0, fmtUsdCompact, 1);
    expect(c.text).toBe("no price");
    expect(c.title).toContain("1 session");
  });

  it("keeps the amount and adds a visible note when only part has no price", () => {
    const c = estimatedCost(0.1038, 0.1038, fmtUsdCompact, 1);
    expect(c.text).toBe("≈$0.1038 · 1 without price");
    expect(c.title).toContain("$0.1038");
    expect(c.title).toContain("no price");
  });

  it("marks each part in a table cell", () => {
    expect(costHTML(0, 0, fmtUsdCompact, 2)).toMatch(
      /^<span class="unpriced" title="2 sessions[^"]*">no price<\/span>$/,
    );
    const html = costHTML(1.61, 0, fmtUsdCompact, 1);
    expect(html).toMatch(
      /^\$1\.61 <span class="unpriced" title="[^"]+">· 1 without price<\/span>$/,
    );
    const both = costHTML(2.92, 2.92, fmtUsdCompact, 1);
    expect(both).toContain('<span class="est"');
    expect(both).toContain('<span class="unpriced"');
  });
});

describe("fmtDecimal", () => {
  it("writes decimals with the UI language's separator", () => {
    expect(fmtDecimal(10.344, 2)).toBe("10.34");
    expect(fmtDecimal(0.05, 2)).toBe("0.05");
    expect(fmtDecimal(NaN, 1)).toBe("0.0");
  });
});
