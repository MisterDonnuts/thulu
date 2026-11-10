function lineAt(text, at) {
  let n = 1;
  for (let i = 0; i < at; i++) if (text.charCodeAt(i) === 10) n++;
  return n;
}

function stripComments(css) {
  return css.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
}

function closeOf(css, open) {
  let depth = 0;
  let quote = "";
  for (let i = open; i < css.length; i++) {
    const c = css[i];
    if (quote) {
      if (c === "\\") i++;
      else if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "'") quote = c;
    else if (c === "{") depth++;
    else if (c === "}" && --depth === 0) return i;
  }
  return css.length;
}

export function splitTop(s, sep) {
  const out = [];
  let depth = 0;
  let quote = "";
  let from = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (quote) {
      if (c === "\\") i++;
      else if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "'") quote = c;
    else if (c === "(") depth++;
    else if (c === ")") depth--;
    else if (c === sep && depth === 0) {
      out.push(s.slice(from, i));
      from = i + 1;
    }
  }
  out.push(s.slice(from));
  return out;
}

export function unwrapGlobal(sel) {
  let out = sel;
  for (let at = out.indexOf(":global("); at >= 0; at = out.indexOf(":global(")) {
    let depth = 0;
    let end = at + 7;
    for (; end < out.length; end++) {
      if (out[end] === "(") depth++;
      else if (out[end] === ")" && --depth === 0) break;
    }
    out = out.slice(0, at) + out.slice(at + 8, end) + out.slice(end + 1);
  }
  return out;
}

function declarations(body) {
  const out = [];
  for (const part of splitTop(body, ";")) {
    const colon = part.indexOf(":");
    if (colon < 0) continue;
    const prop = part.slice(0, colon).trim().toLowerCase();
    let value = part.slice(colon + 1).trim();
    if (!prop) continue;
    const important = /!\s*important\s*$/i.test(value);
    if (important) value = value.replace(/!\s*important\s*$/i, "").trim();
    out.push({ prop, value, important });
  }
  return out;
}

const RULE_HOLDERS = /^@(media|supports|layer|container|document|scope)\b/i;

export function parseRules(css, lineOffset = 0) {
  const src = stripComments(css);
  const rules = [];
  const walk = (from, to, context) => {
    let i = from;
    while (i < to) {
      const open = src.indexOf("{", i);
      const semi = src.indexOf(";", i);
      if (open < 0 || open >= to) return;
      // A statement at-rule (`@import …;`, `@layer a, b;`) before the next block.
      if (semi >= 0 && semi < open && src.slice(i, semi).trim().startsWith("@")) {
        i = semi + 1;
        continue;
      }
      const prelude = src.slice(i, open).trim();
      const close = closeOf(src, open);
      if (prelude.startsWith("@")) {
        if (RULE_HOLDERS.test(prelude))
          walk(open + 1, close, [...context, prelude.replace(/\s+/g, " ")]);
      } else if (prelude) {
        const selectors = splitTop(prelude, ",")
          .map((s) => unwrapGlobal(s).replace(/\s+/g, " ").trim())
          .filter(Boolean);
        const at = i + src.slice(i, open).search(/\S/);
        rules.push({
          selectors,
          decls: declarations(src.slice(open + 1, close)),
          context,
          line: lineAt(src, at) + lineOffset,
        });
      }
      i = close + 1;
    }
  };
  walk(0, src.length, []);
  return rules;
}

export function styleBlocks(astro) {
  const out = [];
  const re = /<style\b[^>]*>([\s\S]*?)<\/style>/g;
  for (let m = re.exec(astro); m; m = re.exec(astro)) {
    const start = m.index + m[0].indexOf(">") + 1;
    out.push({ css: m[1], lineOffset: lineAt(astro, start) - 1 });
  }
  return out;
}

export function rulesOf(path, text) {
  if (path.endsWith(".astro")) {
    return styleBlocks(text).flatMap((b) => parseRules(b.css, b.lineOffset));
  }
  return parseRules(text);
}
