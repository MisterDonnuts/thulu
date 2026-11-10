import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { PROGRESS_TONES } from "../src/lib/ui/progress";

const web = join(dirname(fileURLToPath(import.meta.url)), "..");
const css = readFileSync(join(web, "src/styles/primitives/progress.css"), "utf8").replace(
  /\/\*[\s\S]*?\*\//g,
  "",
);

function fillOf(selector) {
  for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = m[1].split(",").map((x) => x.trim());
    if (!selectors.includes(selector)) continue;
    const fill = m[2].match(/--seg-fill:\s*([^;]+);/);
    if (fill) return fill[1].trim();
  }
  return undefined;
}

describe("progress.css tones", () => {
  it.each(PROGRESS_TONES.filter((tone) => tone !== "accent"))(
    "maps %s on the segment and the meter to the same fill",
    (tone) => {
      const seg = fillOf(`.seg-progress-seg[data-tone="${tone}"]`);
      const meter = fillOf(`.meter-bar[data-tone="${tone}"]`);
      expect(seg).toBeDefined();
      expect(meter).toBe(seg);
      if (tone.startsWith("viz-")) expect(meter).toBe(`var(--${tone})`);
    },
  );

  it("falls back to accent for a tone with no rule of its own", () => {
    expect(fillOf("[data-tone].meter-bar")).toBe("var(--accent)");
    expect(fillOf("[data-tone].seg-progress-seg")).toBe("var(--accent)");
  });
});
