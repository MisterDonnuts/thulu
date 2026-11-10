// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { fileRowEl, fileRowHTML, paramRowEl, paramRowHTML } from "./init-script";

describe("paramRowEl", () => {
  it("is the row paramRowHTML writes", () => {
    const row = paramRowEl({ name: "API_URL", required: true }, 3, false);
    expect(paramRowHTML({ name: "API_URL", required: true }, 3, false)).toContain(
      'data-param-row="3"',
    );
    expect(row.dataset.paramRow).toBe("3");
    expect(row.querySelector<HTMLInputElement>('[data-p="name"]')?.value).toBe("API_URL");
    expect(row.querySelector<HTMLInputElement>('[data-p="required"]')?.checked).toBe(true);
    expect(row.querySelector("[data-param-del]")).not.toBeNull();
  });

  it("keeps a name as text", () => {
    const row = paramRowEl({ name: '"><img src=x onerror=alert(1)>' }, 0, false);
    expect(row.querySelector("img")).toBeNull();
    expect(row.querySelector<HTMLInputElement>('[data-p="name"]')?.value).toBe(
      '"><img src=x onerror=alert(1)>',
    );
  });
});

describe("fileRowEl", () => {
  it("is the row fileRowHTML writes, with an empty file", () => {
    const row = fileRowEl({ path: "", content: "" }, 1, false);
    expect(row.matches('[data-file-row="1"]')).toBe(true);
    expect(row.querySelector<HTMLInputElement>('[data-f="path"]')?.value).toBe("");
    expect(row.querySelector<HTMLTextAreaElement>('[data-f="content"]')?.value).toBe("");
    expect(row.querySelector("[data-file-del]")).not.toBeNull();
    expect(fileRowHTML({ path: "", content: "" }, 1, false)).toContain('data-file-row="1"');
  });

  it("keeps a leading newline of the content", () => {
    const row = fileRowEl({ path: "a.sh", content: "\nset -e\n" }, 0, false);
    expect(row.querySelector<HTMLTextAreaElement>('[data-f="content"]')?.value).toBe("\nset -e\n");
  });

  it("keeps content as text", () => {
    const row = fileRowEl({ path: "x", content: "</textarea><img src=x>" }, 0, false);
    expect(row.querySelector("img")).toBeNull();
    expect(row.querySelector<HTMLTextAreaElement>('[data-f="content"]')?.value).toBe(
      "</textarea><img src=x>",
    );
  });
});
