const FILE_PATH_KINDS = new Set(["file", "design", "proto", "board", "slides", "deck"]);

// Validating the key shape keeps path fragments or query strings from being spliced into API paths.
const KEY_RE = /^[A-Za-z0-9]{8,128}$/;

export function normalizeNodeId(id) {
  const s = String(id ?? "").trim();
  if (!s) return "";
  return s.includes(":") ? s : s.replace(/-/g, ":");
}

export function parseFileRef(input) {
  const raw = String(input ?? "").trim();
  if (!raw) return null;
  if (KEY_RE.test(raw)) return { fileKey: raw, nodeId: "" };
  let url;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (!/(^|\.)figma\.com$/i.test(url.hostname)) return null;
  const parts = url.pathname.split("/").filter(Boolean);
  const kind = parts.findIndex((p) => FILE_PATH_KINDS.has(p));
  if (kind === -1 || !KEY_RE.test(parts[kind + 1] ?? "")) return null;
  let fileKey = parts[kind + 1];
  const branch = parts.indexOf("branch", kind + 2);
  if (branch !== -1 && KEY_RE.test(parts[branch + 1] ?? "")) fileKey = parts[branch + 1];
  return { fileKey, nodeId: normalizeNodeId(url.searchParams.get("node-id")) };
}

export function parseTeamRef(input) {
  const raw = String(input ?? "").trim();
  if (/^\d+$/.test(raw)) return raw;
  const m = raw.match(/figma\.com\/.*?\bteam\/(\d+)/i);
  return m ? m[1] : null;
}

const round = (n, digits = 2) => {
  const f = 10 ** digits;
  return Math.round(Number(n) * f) / f;
};

const hex2 = (v) =>
  Math.round(Math.max(0, Math.min(1, Number(v) || 0)) * 255)
    .toString(16)
    .padStart(2, "0");

export function colorHex(c, opacity = 1) {
  if (!c) return null;
  const a = (c.a ?? 1) * (opacity ?? 1);
  const base = `#${hex2(c.r)}${hex2(c.g)}${hex2(c.b)}`;
  return a >= 0.999 ? base : base + hex2(a);
}

function paint(p) {
  if (!p || p.visible === false) return null;
  if (p.type === "SOLID") return colorHex(p.color, p.opacity);
  if (p.type?.startsWith("GRADIENT_")) {
    return {
      gradient: p.type.slice("GRADIENT_".length).toLowerCase(),
      stops: (p.gradientStops ?? []).map((s) => `${colorHex(s.color, p.opacity)} ${round(s.position)}`),
    };
  }
  if (p.type === "IMAGE") return { image: p.scaleMode?.toLowerCase() ?? "fill" };
  return { type: p.type?.toLowerCase() };
}

function paints(list) {
  if (!Array.isArray(list)) return undefined;
  const out = list.map(paint).filter(Boolean);
  return out.length ? out : undefined;
}

function effect(e) {
  if (!e || e.visible === false) return null;
  const out = { type: e.type?.toLowerCase(), radius: e.radius };
  if (e.color) out.color = colorHex(e.color);
  if (e.offset) out.offset = [round(e.offset.x), round(e.offset.y)];
  if (e.spread) out.spread = e.spread;
  return out;
}

function textStyle(s) {
  if (!s) return undefined;
  const out = {
    font: s.fontFamily,
    weight: s.fontWeight,
    size: s.fontSize,
    line_height: s.lineHeightPx !== undefined ? round(s.lineHeightPx) : undefined,
    letter_spacing: s.letterSpacing ? round(s.letterSpacing) : undefined,
    align: s.textAlignHorizontal && s.textAlignHorizontal !== "LEFT" ? s.textAlignHorizontal.toLowerCase() : undefined,
    case: s.textCase && s.textCase !== "ORIGINAL" ? s.textCase.toLowerCase() : undefined,
    italic: s.italic || undefined,
  };
  return prune(out);
}

function layout(n) {
  if (!n.layoutMode || n.layoutMode === "NONE") return undefined;
  const pad = [n.paddingTop, n.paddingRight, n.paddingBottom, n.paddingLeft].map((v) => v ?? 0);
  return prune({
    mode: n.layoutMode.toLowerCase(),
    wrap: n.layoutWrap === "WRAP" || undefined,
    gap: n.itemSpacing || undefined,
    padding: pad.some(Boolean) ? pad : undefined,
    main: n.primaryAxisAlignItems?.toLowerCase(),
    cross: n.counterAxisAlignItems?.toLowerCase(),
  });
}

function sizing(n) {
  const h = n.layoutSizingHorizontal?.toLowerCase();
  const v = n.layoutSizingVertical?.toLowerCase();
  return h || v ? prune({ h, v }) : undefined;
}

function box(n) {
  const b = n.absoluteBoundingBox;
  if (!b) return undefined;
  return [round(b.x, 1), round(b.y, 1), round(b.width, 1), round(b.height, 1)];
}

function prune(o) {
  const out = {};
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== null) out[k] = v;
  return Object.keys(out).length ? out : undefined;
}

export function summarizeNode(n, { depth = 2, styles = {}, budget = { left: Infinity, omitted: 0 }, textCap = 500 } = {}) {
  budget.left -= 1;
  const out = { id: n.id, name: n.name, type: n.type?.toLowerCase() };
  if (n.visible === false) out.hidden = true;
  const extra = prune({
    box: box(n),
    layout: layout(n),
    sizing: sizing(n),
    fills: paints(n.fills),
    strokes: paints(n.strokes),
    stroke_weight: n.strokes?.length ? n.strokeWeight : undefined,
    radius: n.rectangleCornerRadii ?? (n.cornerRadius || undefined),
    effects: Array.isArray(n.effects) && n.effects.length ? n.effects.map(effect).filter(Boolean) : undefined,
    opacity: n.opacity !== undefined && n.opacity < 1 ? round(n.opacity) : undefined,
    clips: n.clipsContent || undefined,
    component: n.componentId,
    text: typeof n.characters === "string" ? clipText(n.characters, textCap) : undefined,
    text_style: n.type === "TEXT" ? textStyle(n.style) : undefined,
    styles: styleNames(n.styles, styles),
  });
  Object.assign(out, extra);
  const kids = Array.isArray(n.children) ? n.children : [];
  if (kids.length) {
    if (depth > 0) {
      out.children = [];
      for (const child of kids) {
        if (budget.left <= 0) {
          budget.omitted += countNodes(child);
          continue;
        }
        out.children.push(summarizeNode(child, { depth: depth - 1, styles, budget, textCap }));
      }
    } else {
      out.child_count = kids.length;
    }
  }
  return out;
}

function clipText(s, cap) {
  return s.length <= cap ? s : `${s.slice(0, cap)}…`;
}

function styleNames(refs, table) {
  if (!refs || typeof refs !== "object") return undefined;
  const out = {};
  for (const [kind, id] of Object.entries(refs)) out[kind] = table?.[id]?.name ?? id;
  return Object.keys(out).length ? out : undefined;
}

function countNodes(n) {
  let total = 1;
  for (const c of n?.children ?? []) total += countNodes(c);
  return total;
}

export function summarizeTree(document, { maxNodes = 500 } = {}) {
  const budget = { left: maxNodes, omitted: 0 };
  const walk = (n) => {
    budget.left -= 1;
    const out = { id: n.id, name: n.name, type: n.type?.toLowerCase() };
    if (n.visible === false) out.hidden = true;
    const kids = Array.isArray(n.children) ? n.children : [];
    if (!kids.length) return out;
    const shown = [];
    for (const c of kids) {
      if (budget.left <= 0) {
        budget.omitted += countNodes(c);
        continue;
      }
      shown.push(walk(c));
    }
    if (shown.length) out.children = shown;
    else out.child_count = kids.length;
    return out;
  };
  const pages = (document?.children ?? []).map(walk);
  return { pages, omitted_nodes: budget.omitted };
}

export function summarizeLibrary(file, cap = 100) {
  const components = Object.entries(file?.components ?? {}).map(([id, c]) =>
    prune({ id, name: c.name, description: c.description || undefined }),
  );
  const styles = {};
  for (const [id, s] of Object.entries(file?.styles ?? {})) {
    const kind = (s.styleType ?? "other").toLowerCase();
    (styles[kind] ??= []).push({ id, name: s.name });
  }
  for (const kind of Object.keys(styles)) styles[kind] = styles[kind].slice(0, cap);
  return {
    components: components.slice(0, cap),
    components_total: components.length,
    styles,
  };
}

function commentAnchor(meta) {
  if (!meta) return undefined;
  if (meta.node_id) return { node: meta.node_id };
  if (meta.x !== undefined) return { x: round(meta.x, 0), y: round(meta.y, 0) };
  return undefined;
}

export function summarizeComments(list, { includeResolved = false, limit = 50, textCap = 2000 } = {}) {
  const all = Array.isArray(list) ? list : [];
  const short = (c) =>
    prune({
      id: c.id,
      author: c.user?.handle,
      at: c.created_at,
      message: clipText(String(c.message ?? ""), textCap),
    });
  const threads = new Map();
  for (const c of all) {
    if (c.parent_id) continue;
    threads.set(c.id, {
      ...short(c),
      anchor: commentAnchor(c.client_meta),
      resolved_at: c.resolved_at || undefined,
      replies: [],
    });
  }
  for (const c of all) {
    if (c.parent_id && threads.has(c.parent_id)) threads.get(c.parent_id).replies.push(short(c));
  }
  let out = [...threads.values()];
  const resolved = out.filter((th) => th.resolved_at).length;
  if (!includeResolved) out = out.filter((th) => !th.resolved_at);
  out.sort((a, b) => String(b.at).localeCompare(String(a.at)));
  for (const th of out) {
    th.replies.sort((a, b) => String(a.at).localeCompare(String(b.at)));
    if (!th.replies.length) delete th.replies;
    if (!th.anchor) delete th.anchor;
    if (!th.resolved_at) delete th.resolved_at;
  }
  return {
    threads: out.slice(0, limit),
    total: out.length,
    resolved_hidden: includeResolved ? 0 : resolved,
  };
}
