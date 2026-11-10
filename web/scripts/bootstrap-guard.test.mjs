import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const pages = join(dirname(fileURLToPath(import.meta.url)), "..", "src", "pages");

function bootstrapBody(page) {
  const src = readFileSync(join(pages, page), "utf8");
  const start = src.indexOf("async function bootstrap()");
  expect(start, `${page} has no bootstrap()`).toBeGreaterThan(-1);
  const indent = src.slice(src.lastIndexOf("\n", start) + 1, start);
  const end = src.indexOf(`\n${indent}}\n`, start);
  return src.slice(start, end);
}

describe("page bootstraps", () => {
  for (const page of ["tasks.astro", "agents.astro"]) {
    it(`${page} settles its reads instead of failing on the first one`, () => {
      const body = bootstrapBody(page);
      expect(body).toContain('"/types"');
      expect(body).toContain("Promise.allSettled(");
      expect(body).not.toMatch(/Promise\.all\(/);
    });
  }
});
