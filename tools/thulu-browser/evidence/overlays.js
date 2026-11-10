// These functions are serialized into the page (evaluate/addInitScript): they cannot reference
// anything outside their own body.

export const CINEMA = "__octopusCinema";

/**
 * Installed with addInitScript (and evaluated once on the current document), so
 * it is present on every page the take visits.
 * @param {{ redactCss?: string[], badge?: string }} opts
 */
export function installCinema(opts) {
  const NS = "__octopusCinema";
  if (window[NS]) return;
  const ACCENT = "#00C2FF";
  const TONE = { focus: ACCENT, ok: "#3ED598", warn: "#FFB020", bad: "#FF4D4F" };
  let ui = null;
  const state = { caption: "", badge: opts && opts.badge ? opts.badge : "" };

  // i18n-ignore CSS
  const css = `
    :host { all: initial; }
    * { box-sizing: border-box; }
    .root { position: fixed; inset: 0; z-index: 2147483646; pointer-events: none;
      font: 500 15px/1.4 ui-sans-serif, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
    .cursor { position: absolute; left: 0; top: 0; width: 26px; height: 26px; margin: -3px 0 0 -3px; opacity: 0;
      transition: transform .45s cubic-bezier(.4,0,.2,1), opacity .2s; will-change: transform;
      filter: drop-shadow(0 2px 3px rgba(0,0,0,.4)); }
    .ring { position: absolute; border-radius: 10px; opacity: 0; border: 3px solid ${ACCENT};
      box-shadow: 0 0 0 3px rgba(0,194,255,.28); transition: opacity .18s, left .3s, top .3s, width .3s, height .3s; }
    .dot { position: absolute; width: 14px; height: 14px; margin: -7px 0 0 -7px; border-radius: 50%;
      background: rgba(0,194,255,.55); border: 2px solid ${ACCENT}; }
    .caption { position: absolute; left: 50%; bottom: 28px; transform: translateX(-50%); max-width: 76%; opacity: 0;
      transition: opacity .2s; background: rgba(6,12,24,.94); color: #EAF2FF; padding: 10px 16px 12px;
      border-radius: 10px; border-left: 4px solid ${ACCENT}; box-shadow: 0 12px 34px rgba(0,0,0,.45); }
    .track { height: 3px; margin-top: 9px; background: rgba(255,255,255,.16); border-radius: 2px; overflow: hidden; display: none; }
    .bar { height: 100%; width: 0; background: ${ACCENT}; }
    .verdict { position: absolute; right: 24px; top: 24px; width: 380px; opacity: 0; transform: translateY(-8px);
      transition: opacity .2s, transform .2s; background: #0B1220; color: #EAF2FF; border-radius: 12px;
      padding: 14px 16px; box-shadow: 0 16px 40px rgba(0,0,0,.5); border-top: 4px solid ${TONE.ok}; }
    .verdict.show { opacity: 1; transform: none; }
    .verdict .head { display: flex; gap: 10px; align-items: center; font-weight: 700; font-size: 16px; }
    .verdict .mark { width: 26px; height: 26px; border-radius: 50%; display: grid; place-items: center; color: #06101F; }
    .verdict dl { margin: 10px 0 0; display: grid; grid-template-columns: auto 1fr; gap: 4px 10px; font-size: 13px; }
    .verdict dt { opacity: .65; } .verdict dd { margin: 0; word-break: break-word; }
    .badge { position: absolute; left: 16px; top: 16px; padding: 6px 12px; border-radius: 8px;
      background: rgba(6,12,24,.9); color: #EAF2FF; font-weight: 700; letter-spacing: .3px; border-left: 4px solid ${ACCENT}; }
    .hidden { display: none; }`;

  function mount() {
    if (ui && ui.host.isConnected) return ui;
    const host = document.createElement("octopus-cinema");
    const shadow = host.attachShadow({ mode: "closed" });
    shadow.innerHTML = `<style>${css}</style><div class="root">
      <div class="ring"></div>
      <div class="cursor"><svg width="26" height="26" viewBox="0 0 24 24"><path d="M5 2l14 9-6.5 1.3L9.8 19z"
        fill="#fff" stroke="#0B1220" stroke-width="1.6" stroke-linejoin="round"/></svg></div>
      <div class="caption"><div class="text"></div><div class="track"><div class="bar"></div></div></div>
      <div class="verdict"><div class="head"><span class="mark"></span><span class="title"></span></div><dl></dl></div>
      <div class="badge hidden"></div></div>`;
    (document.documentElement || document).appendChild(host);
    const $ = (s) => shadow.querySelector(s);
    ui = { host, root: $(".root"), cursor: $(".cursor"), ring: $(".ring"), caption: $(".caption"), text: $(".text"),
      track: $(".track"), bar: $(".bar"), verdict: $(".verdict"), badge: $(".badge") };
    if (state.badge) api.badge(state.badge);
    if (state.caption) api.caption(state.caption);
    return ui;
  }

  const api = {
    moveTo(x, y, from) {
      const u = mount();
      if (u.cursor.style.opacity !== "1") {
        const start = from || { x: window.innerWidth / 2, y: window.innerHeight * 0.6 };
        u.cursor.style.transition = "none";
        u.cursor.style.transform = `translate(${start.x}px, ${start.y}px)`;
        void u.cursor.offsetWidth;
        u.cursor.style.transition = "";
      }
      u.cursor.style.opacity = "1";
      u.cursor.style.transform = `translate(${x}px, ${y}px)`;
    },
    highlight(box) {
      const u = mount();
      const pad = 5;
      Object.assign(u.ring.style, { left: `${box.x - pad}px`, top: `${box.y - pad}px`,
        width: `${box.width + pad * 2}px`, height: `${box.height + pad * 2}px`, opacity: "1" });
    },
    clear() { mount().ring.style.opacity = "0"; },
    pulse(x, y) {
      const u = mount();
      const dot = document.createElement("div");
      dot.className = "dot";
      Object.assign(dot.style, { left: `${x}px`, top: `${y}px` });
      u.root.appendChild(dot);
      dot.animate([{ transform: "scale(.4)", opacity: 1 }, { transform: "scale(3.2)", opacity: 0 }],
        { duration: 520, easing: "cubic-bezier(.2,.7,.3,1)" }).onfinish = () => dot.remove();
    },
    caption(text) {
      const u = mount();
      state.caption = text || "";
      u.text.textContent = state.caption;
      u.caption.style.opacity = state.caption ? "1" : "0";
      u.track.style.display = "none";
    },
    // The screencast only sends frames when the screen changes; the animated bar keeps a deliberate
    // pause from being cut as dead time.
    beat(ms) {
      const u = mount();
      u.track.style.display = "block";
      u.bar.animate([{ width: "0%" }, { width: "100%" }], { duration: ms, easing: "linear", fill: "forwards" });
    },
    // Strings arrive already translated: this runs in the page and cannot reach the i18n catalog.
    verdict(v) {
      const u = mount();
      const color = v.pass ? TONE.ok : TONE.bad;
      u.verdict.style.borderTopColor = color;
      const mark = u.verdict.querySelector(".mark");
      mark.style.background = color;
      mark.textContent = v.pass ? "✓" : "✕";
      u.verdict.querySelector(".title").textContent = v.title;
      const dl = u.verdict.querySelector("dl");
      dl.replaceChildren();
      for (const [k, val] of v.rows) {
        if (!val) continue;
        const dt = document.createElement("dt"); dt.textContent = k;
        const dd = document.createElement("dd"); dd.textContent = val;
        dl.append(dt, dd);
      }
      u.verdict.classList.add("show");
    },
    unverdict() { mount().verdict.classList.remove("show"); },
    badge(text) {
      const u = mount();
      state.badge = text || "";
      u.badge.textContent = state.badge;
      u.badge.classList.toggle("hidden", !state.badge);
    },
    hide() { mount().root.classList.add("hidden"); },
    show() { mount().root.classList.remove("hidden"); },
  };
  window[NS] = api;

  const redact = (opts && opts.redactCss) || [];
  if (redact.length) {
    const addStyle = () => {
      const style = document.createElement("style");
      style.textContent = `${redact.join(",")} { filter: blur(10px) !important; }`; // i18n-ignore CSS
      (document.head || document.documentElement).appendChild(style);
    };
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", addStyle, { once: true });
    else addStyle();
  }
}

export function cinemaCall([method, args]) {
  const api = window.__octopusCinema;
  if (api && typeof api[method] === "function") api[method](...args);
}

/**
 * The annotation drawn over a capture: a dim veil with holes over the
 * spotlight, numbered frames and notes, redaction blocks, and a header with
 * what is shown and where. Standalone, so it works on a page without cinema.
 *
 * Nothing it draws covers anything else it draws. Every piece is measured as
 * rendered and placed where it touches no frame, number, note or panel and
 * stays whole on screen. A note with no free place next to its element goes to
 * the nearest free place on screen, under its number, and only with none left
 * to the list at the bottom; the header and the list move to the other edge of
 * the screen when they would hide a highlighted element.
 *
 * Returns { crowded: true } when even that failed (the list or a number had to
 * cover a highlighted element): the shot asks for too much on one screen.
 * @param {{ regions: {box:{x:number,y:number,width:number,height:number}, note?:string, tone?:string}[],
 *           redactions: {x:number,y:number,width:number,height:number}[],
 *           title?: string, context: string[], notes: string[], dim: boolean }} input
 * @returns {{ crowded: boolean }}
 */
export function drawAnnotation(input) {
  const TONE = { focus: "#00C2FF", ok: "#3ED598", warn: "#FFB020", bad: "#FF4D4F" };
  document.querySelectorAll("octopus-annotation").forEach((n) => n.remove());
  const host = document.createElement("octopus-annotation");
  const shadow = host.attachShadow({ mode: "open" });
  const W = window.innerWidth;
  const H = window.innerHeight;
  const PAD = 6;
  const GAP = 8;
  const EDGE = 12;
  const NUM = 26;
  const px = (n) => `${Math.round(n)}px`;
  const color = (r) => TONE[r.tone] || TONE.focus;

  // i18n-ignore markup and CSS; the visible text is the caller's
  shadow.innerHTML = `<style>:host{all:initial}*{box-sizing:border-box}
    .root{position:fixed;inset:0;z-index:2147483647;pointer-events:none;
      font:13px/1.45 ui-sans-serif,-apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;color:#0B1220}
    .frame{position:absolute;border-radius:8px;box-shadow:0 0 0 2px rgba(255,255,255,.55),0 8px 28px rgba(0,0,0,.35)}
    .num{position:absolute;width:${NUM}px;height:${NUM}px;border-radius:50%;display:grid;place-items:center;
      font-weight:700;font-size:14px;color:#06101F;box-shadow:0 2px 8px rgba(0,0,0,.35)}
    .bubble{position:absolute;left:0;top:0;width:max-content;max-width:${px(Math.min(320, W - EDGE * 2))};
      background:#fff;border-radius:8px;padding:9px 12px;box-shadow:0 10px 30px rgba(0,0,0,.35);
      white-space:pre-wrap;overflow-wrap:anywhere}
    .redact{position:absolute;background:#0B1220;border-radius:4px}
    .head{position:absolute;left:0;top:0;width:${W}px;min-height:26px;padding:10px 16px;display:flex;gap:16px;
      align-items:baseline;flex-wrap:wrap;background:#060C18;color:#EAF2FF;border-bottom:2px solid #00C2FF}
    .head.bottom{border-bottom:0;border-top:2px solid #00C2FF}
    .head b{font-size:15px;letter-spacing:.2px}.head span{font-size:12px;opacity:.85;font-variant-numeric:tabular-nums}
    .notes{position:absolute;left:16px;top:0;width:${px(W - 32)};padding:10px 14px 10px 32px;border-radius:8px;
      background:rgba(6,12,24,.93);color:#EAF2FF;border-left:4px solid #FFB020;margin:0;font-size:12.5px;
      white-space:pre-wrap;overflow-wrap:anywhere}
    .notes li.ref{list-style:none;margin-left:-14px}</style><div class="root"></div>`;
  const root = shadow.querySelector(".root");
  (document.documentElement || document).appendChild(host);
  const add = (tag, cls, style = {}) => {
    const n = document.createElement(tag);
    n.className = cls;
    Object.assign(n.style, style);
    root.appendChild(n);
    return n;
  };

  const frames = input.regions.map(({ box: a }, i) => {
    let pad = PAD;
    input.regions.forEach(({ box: b }, j) => {
      if (j === i) return;
      const apart = Math.max(b.x - (a.x + a.width), a.x - (b.x + b.width), b.y - (a.y + a.height), a.y - (b.y + b.height));
      pad = Math.min(pad, Math.floor((apart - 2) / 2));
    });
    pad = Math.max(pad, 0);
    return { x: a.x - pad, y: a.y - pad, w: a.width + pad * 2, h: a.height + pad * 2 };
  });
  const hits = (a, b, gap) =>
    a.x < b.x + b.w + gap && b.x < a.x + a.w + gap && a.y < b.y + b.h + gap && b.y < a.y + a.h + gap;
  const onScreen = (a, edge) => a.x >= edge && a.y >= edge && a.x + a.w <= W - edge && a.y + a.h <= H - edge;
  const measure = (n) => { const b = n.getBoundingClientRect(); return { w: b.width, h: b.height }; };

  // Built node by node so no caller value reaches an HTML parser.
  if (input.dim && input.regions.length) {
    const SVG = "http://www.w3.org/2000/svg";
    const svg = (tag, attrs) => {
      const n = document.createElementNS(SVG, tag);
      for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
      return n;
    };
    const mask = svg("mask", { id: "m" });
    mask.append(svg("rect", { width: W, height: H, fill: "white" }));
    for (const f of frames) mask.append(svg("rect", { x: f.x, y: f.y, width: f.w, height: f.h, rx: 8, fill: "black" }));
    const defs = svg("defs", {});
    defs.append(mask);
    const veil = svg("svg", { class: "veil", width: W, height: H, style: "position:absolute;inset:0" }); // i18n-ignore CSS
    veil.append(defs, svg("rect", { width: W, height: H, fill: "rgba(6,12,24,.62)", mask: "url(#m)" }));
    root.append(veil);
  }
  for (const r of input.redactions) {
    add("div", "redact", { left: px(r.x), top: px(r.y), width: px(r.width), height: px(r.height) });
  }
  input.regions.forEach((r, i) => {
    const f = frames[i];
    // i18n-ignore CSS
    add("div", "frame", { left: px(f.x), top: px(f.y), width: px(f.w), height: px(f.h), border: `3px solid ${color(r)}` });
  });
  const nums = input.regions.map((r, i) => {
    const n = add("div", "num", { background: color(r) });
    n.textContent = String(i + 1);
    return n;
  });
  const bubbles = input.regions.map((r, i) => {
    if (!r.note) return null;
    const n = add("div", "bubble", { borderLeft: `4px solid ${color(r)}` }); // i18n-ignore CSS
    const b = document.createElement("b");
    b.style.color = color(r);
    b.textContent = `${i + 1}.`;
    n.append(b, ` ${r.note}`);
    return n;
  });
  let head = null;
  if (input.title || input.context.length) {
    head = add("div", "head");
    if (input.title) { const b = document.createElement("b"); b.textContent = input.title; head.append(b); }
    for (const c of input.context) { const s = document.createElement("span"); s.textContent = c; head.append(s); }
  }
  const list = add("ul", "notes");

  let crowded = false;
  function dock() {
    let top = 0;
    let bottom = H;
    const panels = [];
    const put = (node, x, w, edges, margin) => {
      const { h } = measure(node);
      const at = (edge) => {
        const m = (edge === "top" ? top : H - bottom) ? GAP : margin;
        return edge === "top" ? { x, y: top + m, w, h } : { x, y: bottom - m - h, w, h };
      };
      let edge = edges.find((e) => !frames.some((f) => hits(at(e), f, 0)));
      if (!edge) { crowded = true; edge = edges[0]; }
      const r = at(edge);
      if (edge === "top") top = r.y + r.h; else bottom = r.y;
      node.style.top = px(r.y);
      node.classList.toggle("bottom", edge === "bottom");
      panels.push(r);
    };
    if (head) put(head, 0, W, ["top", "bottom"], 0);
    if (list.childElementCount) put(list, 16, W - 32, ["bottom", "top"], 16);
    return panels;
  }

  function placeNums(blocked) {
    const placed = [];
    frames.forEach((f, i) => {
      const half = NUM / 2;
      const inset = 3 + half;
      const spots = [[f.x, f.y], [f.x + f.w, f.y], [f.x, f.y + f.h], [f.x + f.w, f.y + f.h],
        [f.x + f.w - inset, f.y + inset], [f.x + inset, f.y + inset]]
        .map(([cx, cy]) => ({ x: cx - half, y: cy - half, w: NUM, h: NUM }));
      const free = (b) => onScreen(b, 2) && ![...blocked, ...placed].some((o) => hits(b, o, 2)) &&
        !frames.some((o, j) => j !== i && hits(b, o, 2));
      let b = spots.find(free);
      if (!b) { crowded = true; b = spots[4]; }
      placed.push(b);
      Object.assign(nums[i].style, { left: px(b.x), top: px(b.y) });
    });
    return placed;
  }

  function placeBubbles(blocked, listed) {
    const placed = [];
    const missed = [];
    input.regions.forEach((r, i) => {
      const n = bubbles[i];
      if (!n) return;
      if (listed.includes(i)) { n.style.display = "none"; return; }
      // Measure at the origin: a max-content box near the right edge would shrink and measure
      // wrong.
      Object.assign(n.style, { display: "", left: "0px", top: "0px" });
      const { w, h } = measure(n);
      const f = frames[i];
      const clamp = (x) => Math.min(Math.max(x, EDGE), W - EDGE - w);
      const tops = [f.y, f.y + f.h / 2 - h / 2, f.y + f.h - h];
      const lefts = [f.x, f.x + f.w - w, f.x + f.w / 2 - w / 2].map(clamp);
      const spots = [
        ...tops.map((y) => ({ x: f.x + f.w + GAP, y, w, h })),
        ...tops.map((y) => ({ x: f.x - GAP - w, y, w, h })),
        ...lefts.map((x) => ({ x, y: f.y + f.h + GAP, w, h })),
        ...lefts.map((x) => ({ x, y: f.y - GAP - h, w, h })),
      ];
      const free = (s) => onScreen(s, EDGE) && ![...frames, ...blocked, ...placed].some((o) => hits(s, o, GAP));
      let b = spots.find(free);
      if (!b) {
        const cx = f.x + f.w / 2;
        const cy = f.y + f.h / 2;
        const away = (q) => Math.hypot(q.x + w / 2 - cx, q.y + h / 2 - cy);
        const grid = [];
        for (let y = EDGE; y + h <= H - EDGE; y += 12) for (let x = EDGE; x + w <= W - EDGE; x += 12) grid.push({ x, y, w, h });
        b = grid.sort((p, q) => away(p) - away(q)).find(free);
      }
      if (!b) { n.style.display = "none"; missed.push(i); return; }
      placed.push(b);
      Object.assign(n.style, { left: px(b.x), top: px(b.y) });
    });
    return missed;
  }

  let listed = [];
  for (;;) {
    list.replaceChildren();
    for (const i of listed) {
      const li = document.createElement("li");
      li.className = "ref";
      const b = document.createElement("b");
      b.style.color = color(input.regions[i]);
      b.textContent = `${i + 1}.`;
      li.append(b, ` ${input.regions[i].note}`);
      list.append(li);
    }
    for (const t of input.notes) { const li = document.createElement("li"); li.textContent = t; list.append(li); }
    list.style.display = list.childElementCount ? "" : "none";
    const panels = dock();
    const numBoxes = placeNums(panels);
    const missed = placeBubbles([...panels, ...numBoxes], listed);
    if (!missed.length) break;
    listed = [...listed, ...missed].sort((a, b) => a - b);
    crowded = false;
  }
  return { crowded };
}

export function removeAnnotation() {
  document.querySelectorAll("octopus-annotation").forEach((n) => n.remove());
}
