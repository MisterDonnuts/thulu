import { describe, expect, it, vi } from "vitest";

vi.mock("virtual:octopus-messages", async () => {
  const { loadMessages } = await import("../../i18n.vite.mjs");
  return { locale: "es", sourceLocale: "en", locales: ["en", "es"], messages: loadMessages("es") };
});

const { fmtDur, fmtLatency, fmtNum, fmtPct } = await import("./metrics-format");
const format = await import("./format");

describe("project metrics in a Spanish page", () => {
  it("write decimals with a comma", () => {
    expect(fmtPct(0.746)).toBe("74,6%");
    expect(fmtNum(13_700_000)).toBe("13,7M");
    expect(fmtDur(1_500)).toBe("1,5s");
    expect(fmtLatency(39.5)).toBe("39,5ms");
  });

  it("match the usage report's tokens and percentages", () => {
    expect(format.fmtTokens(6_090_000)).toBe("6,1M");
    expect(format.fmtPct(0.473)).toBe("47,3%");
  });
});
