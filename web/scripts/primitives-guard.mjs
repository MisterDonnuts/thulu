import { rulesOf } from "./css-rules.mjs";

export const PENDING_PRIMITIVES = ["tabs", "tab", "popover"];

const PLACEMENT =
  /^(margin(-[a-z-]+)?|flex|flex-(grow|shrink|basis)|align-self|justify-self|place-self|order|grid-(area|column|row)(-[a-z]+)?|width|min-width|max-width|position|inset|top|right|bottom|left|z-index)$/;

function isPlacement(decl) {
  if (decl.prop === "display") return decl.value === "none";
  return PLACEMENT.test(decl.prop);
}

export function compounds(sel) {
  const out = [];
  let depth = 0;
  let cur = "";
  for (const c of sel) {
    if (c === "(" || c === "[") depth++;
    if (c === ")" || c === "]") depth--;
    if (depth === 0 && (c === " " || c === ">" || c === "+" || c === "~")) {
      if (cur.trim()) out.push(cur.trim());
      cur = "";
      continue;
    }
    cur += c;
  }
  if (cur.trim()) out.push(cur.trim());
  return out;
}

export function classesOf(compound) {
  const bare = compound.replace(/:[\w-]+\((?:[^()]|\([^()]*\))*\)/g, "");
  return [...bare.matchAll(/\.([\w-]+)/g)].map((m) => m[1]);
}

export function subjectClasses(selector) {
  const c = compounds(selector);
  return c.length ? classesOf(c[c.length - 1]) : [];
}

export function primitiveClasses(sheets) {
  const out = new Set(PENDING_PRIMITIVES);
  for (const { path, text } of sheets) {
    for (const rule of rulesOf(path, text)) {
      for (const sel of rule.selectors) {
        if (compounds(sel).length !== 1) continue;
        const first = classesOf(sel)[0];
        if (first) out.add(first);
      }
    }
  }
  return out;
}

export function redefinitions(path, text, primitives) {
  const out = [];
  for (const rule of rulesOf(path, text)) {
    if (rule.decls.every(isPlacement)) continue;
    for (const selector of rule.selectors) {
      const hit = subjectClasses(selector).filter((c) => primitives.has(c));
      if (hit.length) out.push({ selector, line: rule.line, classes: hit });
    }
  }
  return out;
}
