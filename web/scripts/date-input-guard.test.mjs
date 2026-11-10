import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// A DateInput's id lands on the visible text field, which holds the moment the way the UI's
// language writes it ("05/10/2026, 13:14"). new Date() reads that as month/day, so pages go
// through readDateInput(), which returns the ISO value kept in the hidden input.
const src = join(dirname(fileURLToPath(import.meta.url)), "..", "src");

function walk(dir, keep) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return walk(p, keep);
    return keep(name) ? [p] : [];
  });
}

const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

function rawDateReads(text) {
  const ids = [...text.matchAll(/<DateInput\b[^>]*\bid="([^"]+)"/g)].map((m) => m[1]);
  const found = [];
  for (const id of ids) {
    const read = new RegExp(
      `(?:\\$|getElementById|querySelector)\\(\\s*["'\`]#?${esc(id)}["'\`]\\s*\\)[^;\\n]*?\\.value(?:AsDate|AsNumber)?\\b`,
    );
    text.split("\n").forEach((line, i) => {
      if (read.test(line)) found.push(`${id}@${i + 1}`);
    });
  }
  return found;
}

describe("date inputs", () => {
  it("flag a page that reads the visible text instead of the ISO value", () => {
    const page = [
      '<DateInput id="f-from" />',
      'const a = toInstant(($("f-from") as HTMLInputElement).value);',
      'const b = toInstant(readDateInput("f-from"));',
    ].join("\n");
    expect(rawDateReads(page)).toEqual(["f-from@2"]);
  });

  it("are read through readDateInput()", () => {
    const found = [];
    for (const file of walk(src, (n) => n.endsWith(".astro"))) {
      for (const hit of rawDateReads(readFileSync(file, "utf8")))
        found.push(`${relative(src, file)}: ${hit}`);
    }
    expect(found).toEqual([]);
  });
});
