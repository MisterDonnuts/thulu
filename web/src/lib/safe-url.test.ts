import { describe, expect, it } from "vitest";
import { safeUrl, sameOriginPath } from "./safe-url";

describe("safeUrl for links", () => {
  it.each([
    "https://github.com/o/r/pull/1",
    "http://localhost:8585/tasks/",
    "mailto:security@example.com",
    "/tasks/?id=1",
    "#section",
    "  https://padded.example.com  ",
  ])("keeps %s", (u) => {
    expect(safeUrl(u)).toBe(u.trim());
  });

  it.each([
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    " javascript:alert(1)",
    "java\tscript:alert(1)",
    "java\nscript:alert(1)",
    "\u0001javascript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "vbscript:msgbox(1)",
    "file:///etc/passwd",
    "//evil.example.com/x",
    "/\\evil.example.com",
    "\\\\evil.example.com",
    "/\t/evil.example.com",
    "relative/path",
    "",
  ])("refuses %j", (u) => {
    expect(safeUrl(u)).toBe("");
  });

  it("refuses non-strings", () => {
    expect(safeUrl(undefined)).toBe("");
    expect(safeUrl(null)).toBe("");
    expect(safeUrl({ toString: () => "https://x" })).toBe("");
  });
});

describe("safeUrl for media", () => {
  it("keeps http(s) and attachment paths", () => {
    expect(safeUrl("https://example.com/a.png", "media")).toBe("https://example.com/a.png");
    expect(safeUrl("/task-attachments/abc_1", "media")).toBe("/task-attachments/abc_1");
    expect(safeUrl("/task-attachments/abc?download=1", "media")).toBe(
      "/task-attachments/abc?download=1",
    );
  });

  it("refuses other same-origin paths, mailto and fragments", () => {
    expect(safeUrl("/api/tasks/1/delete", "media")).toBe("");
    expect(safeUrl("/task-attachments/../auth/logout", "media")).toBe("");
    expect(safeUrl("mailto:a@b.c", "media")).toBe("");
    expect(safeUrl("#x", "media")).toBe("");
    expect(safeUrl("data:image/svg+xml,<svg onload=alert(1)>", "media")).toBe("");
  });
});

describe("sameOriginPath", () => {
  const origin = "https://thulu.example.com";

  it("keeps a path on this origin", () => {
    expect(sameOriginPath("/tasks/?id=1#c", origin)).toBe("/tasks/?id=1#c");
    expect(sameOriginPath("https://thulu.example.com/settings/", origin)).toBe("/settings/");
  });

  it.each([
    "//evil.example.com",
    "/\\evil.example.com",
    "https://evil.example.com/",
    "javascript:alert(1)",
  ])("falls back for %j", (u) => {
    expect(sameOriginPath(u, origin)).toBe("/");
  });

  it("falls back when there is nothing", () => {
    expect(sameOriginPath(null, origin, "/tasks/")).toBe("/tasks/");
  });
});
