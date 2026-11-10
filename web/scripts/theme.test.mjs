// @vitest-environment jsdom
//
// Light and dark: the guards that keep the two themes whole.
//
// 1. tokens.css writes the light theme twice (explicit choice, and "System"
//    on a light OS). The two bodies must match, and the light theme must
//    redefine every colour the dark one defines, or a page shows a dark
//    value on a white page.
// 2. Every text and control pair meets WCAG AA in the light theme (text
//    4.5:1, control edges, focus ring and chart marks 3:1), including the
//    stage colours that come from data and reach labels through
//    --mix-data-ink. The dark theme is checked for the pairs DESIGN.md says
//    pass; the ones it lists as known misses are not asserted here.
// 3. public/boot/theme.js applies the stored choice to <html> and the meta
//    tags, before a router swap and after it, and follows the system.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { beforeEach, describe, expect, it } from "vitest";

const web = join(dirname(fileURLToPath(import.meta.url)), "..");
const tokensCSS = readFileSync(join(web, "src/styles/tokens.css"), "utf8");
const bootJS = readFileSync(join(web, "public/boot/theme.js"), "utf8");
const boardEditorTS = readFileSync(join(web, "src/lib/board-editor.ts"), "utf8");
const boardTypesTS = readFileSync(join(web, "src/lib/board-types.ts"), "utf8");

function blockAfter(css, selector) {
  const at = css.indexOf(selector);
  if (at < 0) throw new Error(`no ${selector} in tokens.css`);
  const open = css.indexOf("{", at);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    if (css[i] === "}" && --depth === 0) return css.slice(open + 1, i);
  }
  throw new Error(`unclosed ${selector}`);
}

function decls(body) {
  const out = new Map();
  for (const part of body.replace(/\/\*[\s\S]*?\*\//g, "").split(";")) {
    const m = part.match(/^\s*(--[\w-]+)\s*:\s*([\s\S]+?)\s*$/);
    if (m) out.set(m[1], m[2].replace(/\s+/g, " ").replace(/\( /g, "(").replace(/ \)/g, ")"));
  }
  return out;
}

const dark = decls(blockAfter(tokensCSS, ":root {"));
const light = decls(blockAfter(tokensCSS, ':root[data-theme="light"]'));
const lightMedia = decls(
  blockAfter(blockAfter(tokensCSS, "@media (prefers-color-scheme: light)"), ":root:not("),
);

function parseColor(v) {
  const hex = v.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i);
  if (hex) {
    let h = hex[1];
    if (h.length === 3) h = [...h].map((c) => c + c).join("");
    return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)).concat(1);
  }
  const rgb = v.match(/^rgba?\(([^)]+)\)$/);
  if (rgb) {
    const [r, g, b, a = "1"] = rgb[1].split(",").map((s) => s.trim());
    return [Number(r), Number(g), Number(b), Number(a)];
  }
  throw new Error(`not a colour: ${v}`);
}

function color(theme, name) {
  let v = theme.get(name);
  for (let i = 0; v?.startsWith("var(") && i < 8; i++) v = theme.get(v.slice(4, -1).trim());
  if (!v) throw new Error(`${name} has no value`);
  return parseColor(v);
}

function stack(layers) {
  return layers.reduce((under, top) => {
    const a = top[3];
    return [0, 1, 2].map((i) => top[i] * a + under[i] * (1 - a)).concat(1);
  });
}

function luminance([r, g, b]) {
  const lin = (c) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

function ratio(fg, bg) {
  const [a, b] = [luminance(fg), luminance(bg)].sort((x, y) => y - x);
  return (a + 0.05) / (b + 0.05);
}

function mix(a, b, p) {
  return [0, 1, 2].map((i) => a[i] * p + b[i] * (1 - p)).concat(1);
}

function measure(theme, fg, bgs) {
  const bg = stack(bgs.map((b) => (typeof b === "string" ? color(theme, b) : b)));
  const f = typeof fg === "string" ? color(theme, fg) : fg;
  return ratio(stack([bg, f]), bg);
}

function expectPairs(theme, pairs, label) {
  const fails = [];
  for (const [fg, bgs, min] of pairs) {
    const r = measure(theme, fg, bgs);
    if (r < min) fails.push(`${fg} on ${bgs.join(" + ")}: ${r.toFixed(2)} < ${min}`);
  }
  expect(fails, `${label} pairs under WCAG AA`).toEqual([]);
}

const SURFACES = [
  "--color-bg",
  "--color-surface",
  "--color-surface-hover",
  "--color-surface-raised",
  "--color-surface-strong",
];
const STATUS = [
  ["--color-success", "--color-success-bg"],
  ["--color-warning", "--color-warning-bg"],
  ["--color-danger", "--color-danger-bg"],
  ["--color-special", "--color-special-bg"],
  ["--color-notice", "--color-notice-bg"],
  ["--color-accent", "--color-accent-soft"],
];
const TAGS = ["gray", "brown", "orange", "yellow", "green", "blue", "purple", "pink", "red"];
const hexes = (src) => [...src.matchAll(/"(#[0-9a-f]{6})"/gi)].map((m) => m[1]);
const DATA_COLOURS = [...new Set([...hexes(boardEditorTS), ...hexes(boardTypesTS)])];

describe("tokens.css light theme", () => {
  it("writes the same body for data-theme=light and for a light OS", () => {
    expect([...lightMedia.entries()]).toEqual([...light.entries()]);
  });

  it("redefines every colour, shadow and glow of the dark theme", () => {
    const missing = [...dark.keys()].filter((k) => !light.has(k));
    expect(missing).toEqual([]);
  });

  it("states its colour scheme next to its colours", () => {
    expect(blockAfter(tokensCSS, ':root[data-theme="light"]')).toMatch(/color-scheme: light;/);
    expect(blockAfter(tokensCSS, ":root {")).toMatch(/color-scheme: dark;/);
  });

  it("matches the address bar colours in public/boot/theme.js", () => {
    const bar = bootJS.match(/BAR = \{ dark: "(#[0-9a-f]+)", light: "(#[0-9a-f]+)" \}/i);
    expect(bar).not.toBeNull();
    expect(dark.get("--color-bg")).toBe(bar[1]);
    expect(light.get("--color-bg")).toBe(bar[2]);
  });
});

describe("light theme contrast", () => {
  it("text reads on every surface", () => {
    const pairs = [];
    for (const fg of ["--color-text", "--color-text-secondary", "--color-text-muted"]) {
      for (const bg of SURFACES) pairs.push([fg, [bg], 4.5]);
      pairs.push([fg, ["--color-surface", "--color-well"], 4.5]);
      pairs.push([fg, ["--color-surface", "--color-tint-3"], 4.5]);
      pairs.push([fg, ["--color-surface", "--color-accent-soft"], 4.5]);
    }
    expectPairs(light, pairs, "text");
  });

  it("links, the primary button and its key hint read", () => {
    expectPairs(
      light,
      [
        ["--color-accent", ["--color-bg"], 4.5],
        ["--color-accent", ["--color-surface"], 4.5],
        ["--color-accent", ["--color-surface-hover"], 4.5],
        ["--color-text-on-accent", ["--color-accent"], 4.5],
        ["--color-text-on-accent", ["--color-accent-hover"], 4.5],
        ["--color-kbd-on-accent-fg", ["--color-accent", "--color-shade"], 4.5],
      ],
      "accent",
    );
  });

  it("status colours read as text, alone and on their own tint", () => {
    const pairs = [];
    for (const [fg, tint] of STATUS) {
      for (const bg of ["--color-bg", "--color-surface", "--color-surface-raised"]) {
        pairs.push([fg, [bg], 4.5]);
        pairs.push([fg, [bg, tint], 4.5]);
      }
    }
    pairs.push(["--color-info", ["--color-surface"], 4.5]);
    pairs.push(["--color-info", ["--color-surface-raised"], 4.5]);
    pairs.push(["--color-danger-strong", ["--color-surface-raised", "--color-danger-bg"], 4.5]);
    expectPairs(light, pairs, "status");
  });

  it("tag chips read", () => {
    const pairs = TAGS.map((t) => [`--tag-${t}-fg`, ["--color-surface", `--tag-${t}-bg`], 4.5]);
    expectPairs(light, pairs, "tag");
  });

  it("control edges, the focus ring and chart marks reach 3:1", () => {
    const pairs = [];
    for (const bg of ["--color-bg", "--color-surface", "--color-surface-raised"]) {
      pairs.push(["--color-border-control", [bg], 3]);
      pairs.push(["--color-field-border", [bg], 3]);
      pairs.push(["--color-field-focus-border", [bg], 3]);
      pairs.push(["--color-accent", [bg], 3]);
      for (let i = 1; i <= 8; i++) pairs.push([`--viz-${i}`, [bg], 3]);
    }
    pairs.push(["--color-field-border", ["--color-field"], 3]);
    expectPairs(light, pairs, "UI");
  });

  it("stage and type colours from data read once mixed with the text colour", () => {
    expect(DATA_COLOURS.length).toBeGreaterThanOrEqual(10);
    const p = parseFloat(light.get("--mix-data-ink")) / 100;
    const text = color(light, "--color-text");
    const fails = [];
    for (const hex of DATA_COLOURS) {
      const raw = parseColor(hex);
      const ink = mix(raw, text, p);
      const tint = [...raw.slice(0, 3), 0.15];
      for (const bg of ["--color-surface", "--color-surface-raised", "--color-bg"]) {
        const r = measure(light, ink, [bg, tint]);
        if (r < 4.5) fails.push(`${hex} label on ${bg}: ${r.toFixed(2)}`);
      }
    }
    expect(fails).toEqual([]);
  });
});

describe("dark theme contrast (the pairs DESIGN.md lists as passing)", () => {
  it("keeps body text, links and status text readable", () => {
    const pairs = [];
    for (const bg of SURFACES) {
      pairs.push(["--color-text", [bg], 4.5]);
      pairs.push(["--color-text-secondary", [bg], 4.5]);
    }
    pairs.push(["--color-text-muted", ["--color-bg"], 4.5]);
    pairs.push(["--color-text-muted", ["--color-surface"], 4.5]);
    for (const bg of ["--color-bg", "--color-surface", "--color-surface-raised"]) {
      pairs.push(["--color-accent", [bg], 4.5]);
      pairs.push(["--color-success", [bg], 4.5]);
      pairs.push(["--color-warning", [bg], 4.5]);
      pairs.push(["--color-special", [bg], 4.5]);
      pairs.push(["--color-notice", [bg], 4.5]);
      pairs.push(["--color-accent", [bg], 3]);
    }
    pairs.push(["--color-text-on-accent", ["--color-accent"], 4.5]);
    pairs.push(["--color-danger-strong", ["--color-surface-raised"], 4.5]);
    expectPairs(dark, pairs, "dark");
  });

  it("lightens stage and type colours from data enough to read as labels", () => {
    const p = parseFloat(dark.get("--mix-data-ink")) / 100;
    const text = color(dark, "--color-text");
    const fails = [];
    for (const hex of DATA_COLOURS) {
      const raw = parseColor(hex);
      const ink = mix(raw, text, p);
      const tint = [...raw.slice(0, 3), 0.15];
      for (const bg of ["--color-bg", "--color-surface"]) {
        const r = measure(dark, ink, [bg, tint]);
        if (r < 4.5) fails.push(`${hex} label on ${bg}: ${r.toFixed(2)}`);
      }
    }
    expect(fails).toEqual([]);
  });
});

describe("chip fill", () => {
  it("stands apart from the panel in light, and muted text reads on it", () => {
    expect(light.get("--color-fill-chip")).not.toBe(light.get("--color-surface"));
    expect(color(light, "--color-fill-chip")).not.toEqual(color(light, "--color-surface"));
    expectPairs(
      light,
      [
        ["--color-text-muted", ["--color-fill-chip"], 4.5],
        ["--color-text-secondary", ["--color-fill-chip"], 4.5],
      ],
      "chip",
    );
  });
});

function boot({ stored, systemLight = false } = {}) {
  const listeners = [];
  const mq = {
    matches: systemLight,
    addEventListener: (_type, fn) => listeners.push(fn),
  };
  window.matchMedia = () => mq;
  delete window.octopusTheme;
  if (stored === undefined) localStorage.removeItem("octopus.theme");
  else localStorage.setItem("octopus.theme", stored);
  new Function(bootJS)();
  return {
    flipSystem(toLight) {
      mq.matches = toLight;
      for (const fn of listeners) fn();
    },
  };
}

const metaContent = (doc, name) =>
  doc.head.querySelector(`meta[name="${name}"]`)?.getAttribute("content");

describe("public/boot/theme.js", () => {
  beforeEach(() => {
    document.documentElement.removeAttribute("data-theme");
    document.head.innerHTML = '<meta name="color-scheme" content="light dark">';
  });

  it("leaves data-theme off for System and colours the bar from the OS", () => {
    boot({ systemLight: true });
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(metaContent(document, "color-scheme")).toBe("light dark");
    expect(metaContent(document, "theme-color")).toBe(light.get("--color-bg"));
  });

  it("sets data-theme and narrows color-scheme for an explicit choice", () => {
    boot({ stored: "light", systemLight: false });
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(metaContent(document, "color-scheme")).toBe("light");
    expect(metaContent(document, "theme-color")).toBe(light.get("--color-bg"));

    boot({ stored: "dark", systemLight: true });
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(metaContent(document, "theme-color")).toBe(dark.get("--color-bg"));
  });

  it("treats junk in storage as System", () => {
    boot({ stored: "sepia" });
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(metaContent(document, "theme-color")).toBe(dark.get("--color-bg"));
  });

  it("writes the theme onto the incoming page before a router swap, and after it", () => {
    boot({ stored: "light" });
    const next = document.implementation.createHTMLDocument("next");
    const before = new Event("astro:before-swap");
    before.newDocument = next;
    document.dispatchEvent(before);
    expect(next.documentElement.dataset.theme).toBe("light");
    expect(metaContent(next, "theme-color")).toBe(light.get("--color-bg"));

    document.documentElement.removeAttribute("data-theme");
    document.dispatchEvent(new Event("astro:after-swap"));
    expect(document.documentElement.dataset.theme).toBe("light");
  });

  it("follows the OS when it switches while the page is open", () => {
    const page = boot({ systemLight: false });
    expect(metaContent(document, "theme-color")).toBe(dark.get("--color-bg"));
    page.flipSystem(true);
    expect(metaContent(document, "theme-color")).toBe(light.get("--color-bg"));
  });

  it("exposes apply() for the theme control", () => {
    boot({});
    localStorage.setItem("octopus.theme", "dark");
    window.octopusTheme.apply();
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
