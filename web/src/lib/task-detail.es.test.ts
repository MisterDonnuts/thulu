import { describe, expect, it, vi } from "vitest";

vi.mock("virtual:octopus-messages", async () => {
  const { loadMessages } = await import("../../i18n.vite.mjs");
  return { locale: "es", sourceLocale: "en", locales: ["en", "es"], messages: loadMessages("es") };
});

const { runsLine, runsTitle } = await import("./task-detail");

describe("the runs line in a Spanish page", () => {
  it("writes the tokens with the same decimal comma as the cost", () => {
    expect(runsLine(2, 550_400, 0.34)).toBe("2 corridas, 550,4k tokens, $0,34");
    expect(runsLine(5, 1_250_000)).toBe("5 corridas, 1,3M tokens");
  });

  it("marks an estimated cost like Usage and names the estimated part in the tooltip", () => {
    expect(runsLine(2, 550_400, 0.34, 0.21)).toBe("2 corridas, 550,4k tokens, ≈$0,34");
    expect(runsTitle(0.34, 0.21)).toContain("$0,21");
    expect(runsLine(2, 550_400, 0.34, 0.34)).toBe("2 corridas, 550,4k tokens, ≈$0,34");
  });
});
