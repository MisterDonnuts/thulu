import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const src = join(dirname(fileURLToPath(import.meta.url)), "..", "src");

function sources(dir) {
  return readdirSync(dir).flatMap((name) => {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) return sources(p);
    return /\.(ts|astro)$/.test(name) && !/\.test\.ts$/.test(name) ? [p] : [];
  });
}

describe("confirmations", () => {
  const files = sources(src).map((p) => ({
    path: relative(src, p),
    text: readFileSync(p, "utf8"),
  }));

  it("have no generic fallback left to call", () => {
    for (const f of files) {
      expect(f.text, f.path).not.toMatch(/octopusConfirm|installConfirm/);
      expect(f.text, f.path).not.toMatch(/\bwindow\.confirm\(/);
      expect(f.text, f.path).not.toMatch(/layout\.confirm\./);
    }
  });

  it("ask before canceling a run's wait, with words of their own", () => {
    const page = files.find((f) => f.path === join("pages", "tasks.astro")).text;
    const start = page.indexOf("async function cancelQueuedRun(");
    expect(start).toBeGreaterThan(-1);
    const body = page.slice(start, page.indexOf("/queue/cancel", start));
    expect(body).toContain("confirmDialog(");
    expect(body).toContain("cancelWaitQuestion(");
  });
});
