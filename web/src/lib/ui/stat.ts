import { t } from "../../i18n";
import { badgeHTML } from "./badge";
import { attrsHTML, esc, type Attrs } from "./dom";
import { icoSvg } from "./icon";
import { meterHTML, type MeterOptions } from "./progress";

export type StatTone = "neutral" | "ok" | "warn" | "danger" | "accent" | "muted";

export type StatSize = "sm" | "md";

const TONES = new Set<string>(["neutral", "ok", "warn", "danger", "accent", "muted"]);
const SIZES = new Set<string>(["sm", "md"]);

export interface StatLook {
  size?: StatSize;
  link?: boolean;
  na?: boolean;
  cls?: string;
}

function check(set: Set<string>, value: string, what: string): void {
  if (!set.has(value)) throw new Error(`stat: no ${what} "${value}"`); // i18n-ignore a programming error
}

export function statClasses(look: StatLook = {}): string {
  const { size = "md", link = false, na = false, cls } = look;
  check(SIZES, size, "size");
  return ["stat", size === "sm" && "stat-sm", link && "stat-link", na && "stat-na", cls]
    .filter(Boolean)
    .join(" ");
}

export function statTone(tone: StatTone = "neutral"): StatTone | undefined {
  check(TONES, tone, "tone");
  return tone === "neutral" ? undefined : tone;
}

export function statIds(id: string | undefined): { label?: string } {
  return id ? { label: `${id}-label` } : {};
}

export type StatMeter = Omit<MeterOptions, "label" | "labelledBy" | "inline"> & { value: number };

export interface StatOptions extends Omit<StatLook, "link"> {
  label: string;
  value?: string | number;
  valueHTML?: string;
  sub?: string;
  subHTML?: string;
  tone?: StatTone;
  href?: string;
  icon?: string;
  meter?: StatMeter;
  sparkHTML?: string;
  stale?: boolean;
  tag?: "div" | "li";
  id?: string;
  attrs?: Attrs;
}

export function statStaleHTML(): string {
  return badgeHTML(t("ui.stat.stale"), "warn", { shape: "pill", size: "sm", cls: "stat-stale" });
}

export function statTileHTML(o: StatOptions): string {
  const link = o.href !== undefined;
  const tag = link ? "a" : (o.tag ?? "div");
  const ids = statIds(o.id);
  const own: Attrs = {
    id: o.id,
    class: statClasses({ size: o.size, link, na: o.na, cls: o.cls }),
    href: o.href,
    "data-tone": statTone(o.tone),
    "data-stale": o.stale === true,
  };
  const icon = o.icon ? `<span class="stat-icon">${icoSvg(o.icon)}</span>` : "";
  const label = `<span${attrsHTML({ class: "stat-label", id: ids.label })}>${esc(o.label)}</span>`;
  const valueText = o.value !== undefined ? esc(String(o.value)) : (o.valueHTML ?? "");
  const value = `<span class="stat-value">${valueText}</span>`;
  const meter = o.meter ? statMeterHTML(o.meter, o.label, ids.label) : "";
  const spark = o.sparkHTML ? `<span class="stat-spark">${o.sparkHTML}</span>` : "";
  const subText = o.sub !== undefined ? esc(o.sub) : o.subHTML;
  const sub = subText ? `<span class="stat-sub">${subText}</span>` : "";
  const stale = o.stale ? statStaleHTML() : "";
  return `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}>${icon}${label}${value}${meter}${spark}${sub}${stale}</${tag}>`;
}

function statMeterHTML(m: StatMeter, label: string, labelId: string | undefined): string {
  const { value, cls, ...rest } = m;
  const name = labelId ? { labelledBy: labelId } : { label };
  return meterHTML(value, { ...rest, ...name, cls: ["stat-meter", cls].filter(Boolean).join(" ") });
}

export type StatLayout = "grid" | "flow" | "strip";

const LAYOUTS = new Set<string>(["grid", "flow", "strip"]);

export interface StatGroupLook {
  layout?: StatLayout;
  cls?: string;
}

export function statGroupClasses(look: StatGroupLook = {}): string {
  const { layout = "grid", cls } = look;
  check(LAYOUTS, layout, "layout");
  return [`stat-${layout}`, cls].filter(Boolean).join(" ");
}

export interface StatGroupOptions extends StatGroupLook {
  label?: string;
  tag?: "div" | "section" | "ul";
  id?: string;
  attrs?: Attrs;
}

export function statGroupRole(
  tag: StatGroupOptions["tag"],
  label: string | undefined,
): "group" | undefined {
  return label && tag !== "section" ? "group" : undefined;
}

export function statGroupHTML(tiles: string[] | string, o: StatGroupOptions = {}): string {
  const tag = o.tag ?? "div";
  const own: Attrs = {
    id: o.id,
    class: statGroupClasses(o),
    role: statGroupRole(tag, o.label),
    "aria-label": o.label,
  };
  const body = Array.isArray(tiles) ? tiles.join("") : tiles;
  return `<${tag}${attrsHTML(own)}${attrsHTML(o.attrs)}>${body}</${tag}>`;
}

export interface StatPatch {
  value?: string | number;
  sub?: string;
  tone?: StatTone;
  stale?: boolean;
}

export function setStat(el: HTMLElement, p: StatPatch): void {
  if (p.value !== undefined) {
    const v = el.querySelector<HTMLElement>(".stat-value");
    if (v) v.textContent = String(p.value);
  }
  if (p.sub !== undefined) {
    let s = el.querySelector<HTMLElement>(".stat-sub");
    if (!s && p.sub) {
      s = document.createElement("span");
      s.className = "stat-sub";
      const mark = el.querySelector(".stat-stale");
      if (mark) mark.before(s);
      else el.append(s);
    }
    if (s) {
      s.textContent = p.sub;
      s.hidden = !p.sub;
    }
  }
  if (p.tone !== undefined) {
    const tone = statTone(p.tone);
    if (tone) el.dataset.tone = tone;
    else delete el.dataset.tone;
  }
  if (p.stale !== undefined) {
    const mark = el.querySelector(".stat-stale");
    if (p.stale) {
      el.dataset.stale = "";
      if (!mark) el.insertAdjacentHTML("beforeend", statStaleHTML());
    } else {
      delete el.dataset.stale;
      mark?.remove();
    }
  }
}
