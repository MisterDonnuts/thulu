// Never add entries to primitives-guard.exceptions.json for new code; add a variant in
// styles/primitives/ instead.

import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { parseRules, unwrapGlobal } from "./css-rules.mjs";
import { primitiveClasses, redefinitions, subjectClasses } from "./primitives-guard.mjs";

const web = join(dirname(fileURLToPath(import.meta.url)), "..");
const src = join(web, "src");
const primitivesDir = join(src, "styles", "primitives");
const exceptions = JSON.parse(
  readFileSync(join(web, "scripts", "primitives-guard.exceptions.json"), "utf8"),
);

function* sources(dir) {
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) yield* sources(path);
    else if (/\.(css|astro)$/.test(name)) yield path;
  }
}

const primitives = primitiveClasses(
  readdirSync(primitivesDir).map((name) => ({
    path: name,
    text: readFileSync(join(primitivesDir, name), "utf8"),
  })),
);

describe("css-rules", () => {
  it("reads rules inside at-rules, skips keyframes and unwraps :global", () => {
    const rules = parseRules(`
      /* .btn { color: red } */
      @media (max-width: 640px) { :global(.a .btn), .b { color: red } }
      @keyframes spin { from { opacity: 0 } }
      .c { content: "{"; margin: 0 }
    `);
    expect(rules.map((r) => r.selectors)).toEqual([[".a .btn", ".b"], [".c"]]);
    expect(rules[0].context).toEqual(["@media (max-width: 640px)"]);
    expect(rules[0].line).toBe(3);
    expect(rules[1].decls.map((d) => d.prop)).toEqual(["content", "margin"]);
  });

  it("unwraps nested parentheses inside :global", () => {
    expect(unwrapGlobal(":global(.a:not(.b)) .c")).toBe(".a:not(.b) .c");
  });
});

describe("primitives guard", () => {
  const prims = new Set(["btn", "badge"]);

  it("takes the subject's classes, not those inside :not()", () => {
    expect(subjectClasses(".row:hover .btn.primary:not(.x)")).toEqual(["btn", "primary"]);
  });

  it("flags a page rule that restyles a primitive", () => {
    const hits = redefinitions("p.css", ".x .btn { color: red } .badge.mine { padding: 0 }", prims);
    expect(hits.map((h) => h.selector)).toEqual([".x .btn", ".badge.mine"]);
  });

  it("lets a page place a primitive and style what sits inside it", () => {
    const css = `.x .btn { margin-left: auto; flex: none; display: none }
      .badge .ico { width: 10px } .btn-row { color: red }`;
    expect(redefinitions("p.css", css, prims)).toEqual([]);
  });

  it("learns the primitives from the single-compound rules of their sheets", () => {
    const found = primitiveClasses([
      { path: "b.css", text: ".btn { color: red } .btn .ico { width: 1px } a.row:hover { x: y }" },
    ]);
    expect(found.has("btn")).toBe(true);
    expect(found.has("row")).toBe(true);
    expect(found.has("ico")).toBe(false);
  });

  it("finds no redefinition outside styles/primitives beyond the listed exceptions", () => {
    const found = {};
    for (const path of sources(src)) {
      const rel = relative(src, path);
      if (rel.startsWith("styles/primitives/") || rel === "styles/primitives.css") continue;
      const hits = redefinitions(rel, readFileSync(path, "utf8"), primitives);
      if (hits.length) found[rel] = hits;
    }

    const unexpected = [];
    for (const [file, hits] of Object.entries(found)) {
      const allowed = new Set(exceptions[file] ?? []);
      for (const h of hits) {
        if (!allowed.has(h.selector)) unexpected.push(`${file}:${h.line} ${h.selector}`);
      }
    }
    const stale = [];
    for (const [file, selectors] of Object.entries(exceptions)) {
      const seen = new Set((found[file] ?? []).map((h) => h.selector));
      for (const s of selectors) if (!seen.has(s)) stale.push(`${file} ${s}`);
    }

    expect(unexpected, "restyles a primitive: make it a variant in styles/primitives/").toEqual([]);
    expect(stale, "no longer in the code: delete it from primitives-guard.exceptions.json").toEqual(
      [],
    );
  });
});
