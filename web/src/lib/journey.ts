// The journey chart at the top of Activity, drawn as the Task View Lab draws it: one node per board
// column with how many times the task went there, the column it is in now with a halo and a Now
// badge, forward hops as arrows (a hop that skipped columns as a dashed arc underneath), returns as
// half-circles underneath (warn when an agent sent it back, violet when a person did) labelled with
// the rounds they opened, and badges over a column for its questions, stops and failed runs. Every
// node and arrow carries a native tooltip.

import { t } from "../i18n";
import type { RunInfo } from "./activity";
import type { Visit } from "./rounds";
import { esc, icoSvg, sectionHeadHTML } from "./ui";

export interface JourneyColumn {
  id: string;
  label: string;
  color: string;
  agent: string;
}

export interface JourneyQuestion {
  stage?: string;
  blocking?: boolean;
}

export interface JourneyInput {
  columns: readonly JourneyColumn[];
  visits: readonly Visit[];
  runs: readonly RunInfo[];
  questions: readonly JourneyQuestion[];
  // The stage each stop for a person happened in (task_blocked events).
  stops: readonly string[];
  // The column the task is in now, and whether an agent runs there.
  current: string;
  running: boolean;
  now: number;
  // The drawer's width: the chart is as wide as the drawer leaves, never under 780px.
  width: number;
  // Why each round after the first started, by round: "who · reason".
  whys?: ReadonlyMap<number, string>;
}

export type BadgeKind = "now" | "run" | "q" | "blk" | "fail";

export interface JourneyBadge {
  kind: BadgeKind;
  icon: string;
  text: string;
  x: number;
  w: number;
}

export interface JourneyNode {
  col: JourneyColumn;
  x: number;
  visits: number;
  ms: number;
  questions: number;
  waited: number;
  stops: number;
  fails: number;
  moved: number;
  now: boolean;
  lines: string[];
  badges: JourneyBadge[];
}

export interface JourneyHop {
  from: string;
  to: string;
  back: boolean;
  person: boolean;
  rounds: number[];
  whys: string[];
}

export interface JourneyModel {
  W: number;
  running: boolean;
  height: number;
  Y: number;
  R: number;
  nodes: JourneyNode[];
  hops: JourneyHop[];
  x: (id: string) => number;
  idx: (id: string) => number;
}

export const JOURNEY_MIN_W = 780;
const PAD_X = 58;
const Y = 92;
const R = 13;
const FAILED = new Set(["failed", "not_started", "out_of_turns"]);

// A column's label in lines of at most 13 characters, broken at spaces.
export function labelLines(label: string): string[] {
  const lines: string[] = [];
  for (const w of label.split(" ").filter(Boolean)) {
    const l = lines.at(-1);
    if (l && (l + " " + w).length <= 13) lines[lines.length - 1] = l + " " + w;
    else lines.push(w);
  }
  return lines;
}

// What happened in one column: visits, time, questions (and how many held the task), stops,
// failed runs and moves a person made out of it.
function tally() {
  return { visits: 0, ms: 0, questions: 0, waited: 0, stops: 0, fails: 0, moved: 0 };
}

export function journeyModel(input: JourneyInput): JourneyModel | null {
  const cols = input.columns;
  const n = cols.length;
  if (!n) return null;
  const inner = input.width - (input.width < 700 ? 34 : 50);
  const W = Math.max(inner, JOURNEY_MIN_W);
  const step = n > 1 ? (W - PAD_X * 2) / (n - 1) : 0;
  const idx = (id: string) => cols.findIndex((c) => c.id === id);
  const x = (id: string) => (n > 1 ? PAD_X + idx(id) * step : W / 2);

  const per = new Map(cols.map((c) => [c.id, tally()]));
  for (const v of input.visits) {
    const p = per.get(v.stage);
    if (!p) continue;
    p.visits++;
    const end = v.until ? Date.parse(v.until) : input.now;
    const start = Date.parse(v.at);
    if (isFinite(end) && isFinite(start)) p.ms += Math.max(0, end - start);
    if (v.next?.byPerson) p.moved++;
  }
  for (const q of input.questions) {
    const p = per.get(q.stage ?? "");
    if (!p) continue;
    p.questions++;
    if (q.blocking) p.waited++;
  }
  for (const s of input.stops) {
    const p = per.get(s);
    if (p) p.stops++;
  }
  for (const r of input.runs) {
    const p = per.get(r.stage);
    if (p && FAILED.has(r.state)) p.fails++;
  }

  // Transitions between consecutive visits, merged by their ends and by who made a return.
  const byKey = new Map<string, JourneyHop>();
  input.visits.forEach((v, i) => {
    if (!i) return;
    const prev = input.visits[i - 1];
    if (idx(prev.stage) < 0 || idx(v.stage) < 0 || prev.stage === v.stage) return;
    const back = idx(v.stage) < idx(prev.stage);
    const key = `${prev.stage}>${v.stage}>${back && v.byPerson ? "p" : "a"}`;
    let h = byKey.get(key);
    if (!h) {
      h = { from: prev.stage, to: v.stage, back, person: v.byPerson, rounds: [], whys: [] };
      byKey.set(key, h);
    }
    h.rounds.push(v.round);
    const why = v.round !== prev.round ? input.whys?.get(v.round) : undefined;
    if (why) h.whys.push(`R${v.round}: ${why}`);
  });
  const hops = [...byKey.values()];
  const backs = hops.filter((h) => h.back);
  const maxDepth = Math.max(40, ...backs.map((h) => 22 + (idx(h.from) - idx(h.to)) * 13 + 10));
  const height = Y + R + maxDepth + 28;

  const nodes: JourneyNode[] = cols.map((col) => {
    const p = per.get(col.id) ?? tally();
    const now = col.id === input.current;
    const raw: Array<[BadgeKind, string, string]> = [];
    if (now)
      raw.push(
        input.running
          ? ["run", "play", t("lib.journey.now")]
          : ["now", "user", t("lib.journey.now")],
      );
    if (p.questions) raw.push(["q", "help", String(p.questions)]);
    if (p.stops) raw.push(["blk", "stop", String(p.stops)]);
    if (p.fails) raw.push(["fail", "x", String(p.fails)]);
    const widths = raw.map(([, , text]) => text.length * 6.5 + 24);
    let bx = x(col.id) - (widths.reduce((a, b) => a + b, 0) + (raw.length - 1) * 3) / 2;
    const badges = raw.map(([kind, icon, text], j) => {
      const b = { kind, icon, text, x: bx, w: widths[j] };
      bx += widths[j] + 3;
      return b;
    });
    return { col, x: x(col.id), ...p, now, lines: labelLines(col.label), badges };
  });

  return { W, running: input.running, height, Y, R, nodes, hops, x, idx };
}

// "15 min" under an hour, "2 h 05" above.
export function spanText(ms: number): string {
  const m = Math.round(ms / 60000);
  return m < 60
    ? t("lib.journey.minutes", { m })
    : t("lib.journey.hours", { h: Math.floor(m / 60), mm: String(m % 60).padStart(2, "0") });
}

const HEX = /^#[0-9a-f]{3,8}$/i;
const n2 = (v: number) => String(Math.round(v * 100) / 100);

function nodeTip(nd: JourneyNode): string {
  const lines = [nd.col.agent ? `${nd.col.label} · ${nd.col.agent}` : nd.col.label];
  lines.push(
    nd.visits
      ? t("lib.journey.tip.visited", { count: nd.visits, time: spanText(nd.ms) })
      : t("lib.journey.tip.unseen"),
  );
  if (nd.questions)
    lines.push(t("lib.journey.tip.questions", { count: nd.questions, waited: nd.waited }));
  if (nd.stops) lines.push(t("lib.journey.tip.stops", { count: nd.stops }));
  if (nd.fails) lines.push(t("lib.journey.tip.fails", { count: nd.fails }));
  if (nd.moved) lines.push(t("lib.journey.tip.moved", { count: nd.moved }));
  return lines.join("\n");
}

function forwardTip(h: JourneyHop, label: (id: string) => string): string {
  const rounds = h.rounds.map((r) => `R${r}`).join(", ");
  const base = t("lib.journey.tip.hop", {
    from: label(h.from),
    to: label(h.to),
    count: h.rounds.length,
    rounds,
  });
  return h.person ? `${base} · ${t("lib.journey.tip.by_person")}` : base;
}

export function journeySvgHTML(m: JourneyModel): string {
  const { W, height, nodes } = m;
  const label = (id: string) => nodes.find((nd) => nd.col.id === id)?.col.label ?? id;
  let svg = "";
  for (const h of m.hops.filter((h) => !h.back)) {
    const x1 = m.x(h.from);
    const x2 = m.x(h.to);
    const tip = forwardTip(h, label);
    if (m.idx(h.to) - m.idx(h.from) === 1) {
      const cnt =
        h.rounds.length > 1
          ? `<text x="${n2((x1 + x2) / 2)}" y="${Y - 6}" class="cnt">×${h.rounds.length}</text>`
          : "";
      svg += `<g class="hop fwd"><title>${esc(tip)}</title><line x1="${n2(x1 + R + 3)}" y1="${Y}" x2="${n2(x2 - R - 5)}" y2="${Y}" marker-end="url(#ah-fwd)"/>${cnt}</g>`;
    } else {
      const rx = (x2 - x1) / 2;
      const ry = 18;
      const skipped = nodes
        .slice(m.idx(h.from) + 1, m.idx(h.to))
        .map((nd) => nd.col.label)
        .join(", ");
      svg += `<g class="hop fwd skip"><title>${esc(`${tip} · ${t("lib.journey.tip.skipped", { stages: skipped })}`)}</title><path d="M ${n2(x1 + 3)} ${Y + R + 2} A ${n2(rx)} ${ry} 0 0 0 ${n2(x2 - 3)} ${Y + R + 2}" marker-end="url(#ah-fwd)"/><text x="${n2((x1 + x2) / 2)}" y="${Y + R + ry + 14}" class="cnt">${esc(t("lib.journey.skipped", { count: h.rounds.length }))}</text></g>`;
    }
  }
  // Returns, widest first so the narrow ones draw on top.
  const seen = new Map<string, number>();
  const span = (h: JourneyHop) => m.idx(h.from) - m.idx(h.to);
  for (const h of m.hops.filter((h) => h.back).sort((a, b) => span(b) - span(a))) {
    const x1 = m.x(h.from);
    const x2 = m.x(h.to);
    const key = h.from + ">" + h.to;
    const nth = (seen.get(key) ?? -1) + 1;
    seen.set(key, nth);
    const ry = 22 + span(h) * 13 + nth * 9;
    const rx = (x1 - x2) / 2;
    const cls = h.person ? "person" : "agent";
    const head = h.person
      ? t("lib.journey.tip.back_person", { to: label(h.to), count: h.rounds.length })
      : t("lib.journey.tip.back_agent", {
          from: label(h.from),
          to: label(h.to),
          count: h.rounds.length,
        });
    const tip = [head, ...h.whys].join("\n");
    const n = h.rounds.length;
    svg += `<g class="hop back ${cls}"><title>${esc(tip)}</title><path d="M ${n2(x1 - 3)} ${Y + R + 2} A ${n2(rx)} ${ry} 0 0 1 ${n2(x2 + 3)} ${Y + R + 2}" marker-end="url(#ah-${cls})"/><g transform="translate(${n2((x1 + x2) / 2)} ${Y + R + 2 + ry})"><rect x="-${10 + n * 13}" y="-9" width="${20 + n * 26}" height="18" rx="9" class="lblbg ${cls}"/><text y="4" class="arc-lbl ${cls}">${h.rounds.map((r) => "R" + r).join(" · ")}</text></g></g>`;
  }
  for (const nd of nodes) {
    const cls = [
      "node",
      nd.now ? "now" : "",
      nd.now ? (m.running ? "running" : "waiting") : "",
      nd.visits ? "" : "unseen",
    ]
      .filter(Boolean)
      .join(" ");
    const color = HEX.test(nd.col.color) ? ` style="--sc:${nd.col.color}"` : "";
    const lines = nd.lines
      .map((l, j) => `<text x="${n2(nd.x)}" y="${16 + j * 13}" class="nl">${esc(l)}</text>`)
      .join("");
    const halo = nd.now ? `<circle cx="${n2(nd.x)}" cy="${Y}" r="${R + 5}" class="halo"/>` : "";
    const badges = nd.badges
      .map(
        (b) =>
          `<g class="bdg ${b.kind}" transform="translate(${n2(b.x)} ${Y - R - 26})"><rect width="${n2(b.w)}" height="16" rx="8"/><use href="#i-${b.icon}" x="4" y="2" width="12" height="12"/><text x="18" y="11.5">${esc(b.text)}</text></g>`,
      )
      .join("");
    svg += `<g class="${cls}"><title>${esc(nodeTip(nd))}</title>${lines}${halo}<circle cx="${n2(nd.x)}" cy="${Y}" r="${R}" class="nc"${color}/><text x="${n2(nd.x)}" y="${Y + 4}" class="nv">${nd.visits || ""}</text>${badges}</g>`;
  }
  const markers = ["fwd", "agent", "person"]
    .map(
      (k) =>
        `<marker id="ah-${k}" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" class="ah ${k}"/></marker>`,
    )
    .join("");
  return `<svg viewBox="0 0 ${n2(W)} ${height}" width="${n2(W)}" height="${height}" role="img" aria-label="${esc(t("lib.journey.label"))}"><defs>${markers}</defs>${svg}</svg>`;
}

function legendHTML(): string {
  const bi = (kind: string, icon: string) =>
    `<span class="ar-bdg-i ${kind}">${icoSvg(icon, { size: "sm" })}</span>`;
  return `<div class="ar-legend ar-jl"><span><b class="ar-lg-n">7</b> ${esc(t("lib.journey.legend.times"))}</span><span><span class="ar-lg-line agent"></span> ${esc(t("lib.journey.legend.agent_back"))}</span><span><span class="ar-lg-line person"></span> ${esc(t("lib.journey.legend.person_back"))}</span><span>${bi("q", "help")} ${esc(t("lib.journey.legend.questions"))}</span><span>${bi("blk", "stop")} ${esc(t("lib.journey.legend.stops"))}</span><span>${bi("fail", "x")} ${esc(t("lib.journey.legend.fails"))}</span><span>${esc(t("lib.journey.legend.round"))}</span></div>`;
}

// The chart in its card, with the legend under it. Empty when the board has no columns to draw.
export function journeyHTML(input: JourneyInput): string {
  const m = journeyModel(input);
  if (!m) return "";
  const head = sectionHeadHTML({
    title: t("lib.journey.title"),
    level: 4,
    asideHTML: `<span class="tv-sec-r">${esc(t("lib.journey.hint"))}</span>`,
  });
  return `<div>${head}<div class="ar-card ar-journey"><div class="ar-jscroll">${journeySvgHTML(m)}</div>${legendHTML()}</div></div>`;
}
