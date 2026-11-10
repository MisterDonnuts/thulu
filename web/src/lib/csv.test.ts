import { describe, expect, it } from "vitest";

import { buildCSV, csvCell } from "./csv";

describe("csvCell", () => {
  it("leaves plain values alone", () => {
    expect(csvCell("claude-sonnet")).toBe("claude-sonnet");
    expect(csvCell(42)).toBe("42");
  });

  it("quotes commas, quotes and newlines", () => {
    expect(csvCell("fix login, then deploy")).toBe('"fix login, then deploy"');
    expect(csvCell('say "hi"')).toBe('"say ""hi"""');
    expect(csvCell("line one\nline two")).toBe('"line one\nline two"');
  });

  it("writes null and undefined as empty", () => {
    expect(csvCell(null)).toBe("");
    expect(csvCell(undefined)).toBe("");
  });
});

describe("buildCSV", () => {
  it("separates sections with a blank line and ends with CRLF", () => {
    const out = buildCSV([
      { title: "By engine", header: ["engine", "cost"], rows: [["claude", 1.5]] },
      { title: "By model", header: ["model", "cost"], rows: [["opus", 2]] },
    ]);
    expect(out).toBe(
      ["By engine", "engine,cost", "claude,1.5", "", "By model", "model,cost", "opus,2"].join(
        "\r\n",
      ) + "\r\n",
    );
  });

  it("escapes through the section title and header too", () => {
    const out = buildCSV([{ title: "Top tasks, by cost", header: ['a"b'], rows: [] }]);
    expect(out).toBe('"Top tasks, by cost"\r\n"a""b"\r\n');
  });

  it("emits just a terminator for no sections", () => {
    expect(buildCSV([])).toBe("\r\n");
  });
});
